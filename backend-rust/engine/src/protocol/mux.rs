//! sing-mux TCP streams carried by a shared Yamux client session.
use crate::dialer::{BoxFuture, BoxedStream, OutboundDialer, OutboundUdpDialer, TargetAddr};
use futures_util::{future::poll_fn, stream, TryStreamExt};
use std::{io, sync::Arc, task::Poll};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    sync::{mpsc, oneshot, Mutex},
};
use tokio_util::{
    compat::{FuturesAsyncReadCompatExt, TokioAsyncReadCompatExt},
    io::{ReaderStream, StreamReader},
};

type Request = oneshot::Sender<io::Result<yamux::Stream>>;
pub struct Mux {
    outbound: Arc<dyn OutboundDialer>,
    active: Mutex<Option<mpsc::Sender<Request>>>,
}
impl Mux {
    pub fn new(outbound: Arc<dyn OutboundDialer>) -> Self {
        Self {
            outbound,
            active: Mutex::new(None),
        }
    }
    async fn session(&self) -> io::Result<mpsc::Sender<Request>> {
        let mut active = self.active.lock().await;
        if let Some(tx) = active.as_ref().filter(|tx| !tx.is_closed()) {
            return Ok(tx.clone());
        }
        let mut io = self
            .outbound
            .dial_tcp(&TargetAddr::Domain("sp.mux.sing-box.arpa".into(), 444))
            .await?;
        io.write_all(&[0, 1]).await?; // sing-mux version 0, Yamux
        io.flush().await?;
        let mut conn = yamux::Connection::new(io.compat(), Default::default(), yamux::Mode::Client);
        let (tx, mut requests) = mpsc::channel::<Request>(32);
        tokio::spawn(async move {
            let mut pending: Option<Request> = None;
            poll_fn(move |cx| loop {
                match conn.poll_next_inbound(cx) {
                    Poll::Ready(Some(Err(e))) => {
                        eprintln!("Yamux connection: {e}");
                        return Poll::Ready(());
                    }
                    Poll::Ready(None) => return Poll::Ready(()),
                    Poll::Ready(Some(Ok(_))) => continue, // client-only tunnel; dropping resets unsolicited streams
                    Poll::Pending => {}
                }
                if pending.is_none() {
                    match requests.poll_recv(cx) {
                        Poll::Ready(Some(request)) => pending = Some(request),
                        Poll::Ready(None) => return Poll::Ready(()),
                        Poll::Pending => return Poll::Pending,
                    }
                }
                match conn.poll_new_outbound(cx) {
                    Poll::Ready(result) => {
                        let _ = pending
                            .take()
                            .unwrap()
                            .send(result.map_err(io::Error::other));
                    }
                    Poll::Pending => return Poll::Pending,
                }
            })
            .await
        });
        *active = Some(tx.clone());
        Ok(tx)
    }
}
impl OutboundDialer for Mux {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        self.outbound.udp()
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let mut header = vec![0, 0]; // big endian TCP flags, then SOCKS address and port
            target.write_socks_addr(&mut header)?;
            let (reply, stream) = oneshot::channel();
            self.session()
                .await?
                .send(reply)
                .await
                .map_err(|_| io::Error::other("Yamux session closed"))?;
            let mut stream = stream.await.map_err(io::Error::other)??.compat();
            stream.write_all(&header).await?;
            stream.flush().await?;
            // The server may reply only after application data. Keep the write half independent.
            let (mut rx, tx) = tokio::io::split(stream);
            let reader = stream::once(async move {
                match rx.read_u8().await? {
                    0 => Ok(ReaderStream::new(rx)),
                    1 => {
                        let mut length = 0usize;
                        for shift in [0, 7, 14] {
                            let byte = rx.read_u8().await?;
                            length |= usize::from(byte & 127) << shift;
                            if byte < 128 && length <= 65535 {
                                let mut message = vec![0; length];
                                rx.read_exact(&mut message).await?;
                                return Err(io::Error::other(format!(
                                    "Mux peer: {}",
                                    String::from_utf8_lossy(&message)
                                )));
                            }
                        }
                        Err(io::Error::other("invalid Mux error length"))
                    }
                    status => Err(io::Error::other(format!("unknown Mux status {status}"))),
                }
            })
            .try_flatten();
            Ok(Box::new(tokio::io::join(StreamReader::new(Box::pin(reader)), tx)) as BoxedStream)
        })
    }
}
