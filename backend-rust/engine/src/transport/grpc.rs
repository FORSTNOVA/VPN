//! HTTP/2 gRPC transport wrapper over any `BoxedStream`.

use std::{
    future::Future,
    io,
    pin::Pin,
    task::{Context, Poll},
};

use bytes::{BufMut, Bytes, BytesMut};
use h2::{client, RecvStream, SendStream};
use http::{Request, Response};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

use crate::dialer::BoxedStream;

#[derive(Clone, Debug)]
pub struct GrpcConfig {
    pub service_name: String,
    pub authority: Option<String>,
}

impl Default for GrpcConfig {
    fn default() -> Self {
        Self {
            service_name: "GunService".to_string(),
            authority: None,
        }
    }
}

/// Opens a bidirectional gRPC `Tun` stream over HTTP/2 on the given stream.
pub async fn connect_grpc(
    stream: BoxedStream,
    server_host: &str,
    cfg: &GrpcConfig,
) -> io::Result<BoxedStream> {
    let (mut send_req, conn) = client::handshake(stream)
        .await
        .map_err(|e| io::Error::new(io::ErrorKind::ConnectionAborted, e))?;

    tokio::spawn(async move {
        let _ = conn.await;
    });

    let service = cfg.service_name.trim_matches('/');
    let authority = cfg
        .authority
        .as_deref()
        .filter(|a| !a.is_empty())
        .unwrap_or(server_host);
    let uri = format!("https://{authority}/{service}/Tun");
    let request = Request::post(uri)
        .header("content-type", "application/grpc")
        .header("te", "trailers")
        .body(())
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?;

    let (resp_fut, send_stream) = send_req
        .send_request(request, false)
        .map_err(|e| io::Error::new(io::ErrorKind::BrokenPipe, e))?;

    Ok(Box::new(GrpcByteStream {
        send: send_stream,
        resp_fut: Some(Box::pin(resp_fut)),
        recv: None,
        recv_buf: BytesMut::new(),
        plain_buf: BytesMut::new(),
        eof: false,
    }) as BoxedStream)
}

struct GrpcByteStream {
    send: SendStream<Bytes>,
    resp_fut: Option<Pin<Box<dyn Future<Output = Result<Response<RecvStream>, h2::Error>> + Send>>>,
    recv: Option<RecvStream>,
    recv_buf: BytesMut,
    plain_buf: BytesMut,
    eof: bool,
}

impl GrpcByteStream {
    fn drain_plain(&mut self, out: &mut ReadBuf<'_>) -> bool {
        if self.plain_buf.is_empty() {
            return false;
        }
        let n = self.plain_buf.len().min(out.remaining());
        out.put_slice(&self.plain_buf.split_to(n));
        true
    }

    fn decode_grpc_frames(&mut self) -> io::Result<()> {
        // Standard gRPC length-prefixed message: [compressed: 1B][len: 4B BE][payload]
        // Inside Gun/Xray gRPC, payload is protobuf field 1 (tag 0x0a + varint len + raw bytes).
        while self.recv_buf.len() >= 5 {
            let len = u32::from_be_bytes([
                self.recv_buf[1],
                self.recv_buf[2],
                self.recv_buf[3],
                self.recv_buf[4],
            ]) as usize;
            if self.recv_buf.len() < 5 + len {
                break;
            }
            let _ = self.recv_buf.split_to(5);
            let msg = self.recv_buf.split_to(len);
            if msg.first() == Some(&0x0a) {
                if let Some((data_len, header_len)) = decode_varint(&msg[1..]) {
                    let start = 1 + header_len;
                    let end = start + data_len;
                    if end <= msg.len() {
                        self.plain_buf.extend_from_slice(&msg[start..end]);
                        continue;
                    }
                }
            }
            self.plain_buf.extend_from_slice(&msg);
        }
        Ok(())
    }
}

fn encode_varint(mut v: usize, out: &mut BytesMut) {
    while v >= 0x80 {
        out.put_u8(((v as u8) & 0x7f) | 0x80);
        v >>= 7;
    }
    out.put_u8(v as u8);
}

fn decode_varint(input: &[u8]) -> Option<(usize, usize)> {
    let mut val = 0usize;
    for (i, &b) in input.iter().take(5).enumerate() {
        val |= ((b & 0x7f) as usize) << (i * 7);
        if b & 0x80 == 0 {
            return Some((val, i + 1));
        }
    }
    None
}

impl AsyncRead for GrpcByteStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            if self.drain_plain(out) {
                return Poll::Ready(Ok(()));
            }
            if self.eof {
                return Poll::Ready(Ok(()));
            }
            if let Some(fut) = self.resp_fut.as_mut() {
                match fut.as_mut().poll(cx) {
                    Poll::Ready(Ok(resp)) => {
                        self.resp_fut = None;
                        self.recv = Some(resp.into_body());
                    }
                    Poll::Ready(Err(e)) => {
                        self.resp_fut = None;
                        return Poll::Ready(Err(io::Error::new(
                            io::ErrorKind::ConnectionAborted,
                            e,
                        )));
                    }
                    Poll::Pending => return Poll::Pending,
                }
            }
            let Some(recv) = self.recv.as_mut() else {
                return Poll::Ready(Ok(()));
            };
            match recv.poll_data(cx) {
                Poll::Ready(Some(Ok(chunk))) => {
                    let _ = recv.flow_control().release_capacity(chunk.len());
                    self.recv_buf.extend_from_slice(&chunk);
                    self.decode_grpc_frames()?;
                }
                Poll::Ready(Some(Err(e))) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::ConnectionAborted,
                        e,
                    )));
                }
                Poll::Ready(None) => {
                    self.eof = true;
                    return Poll::Ready(Ok(()));
                }
                Poll::Pending => return Poll::Pending,
            }
        }
    }
}

impl AsyncWrite for GrpcByteStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        if buf.is_empty() {
            return Poll::Ready(Ok(0));
        }
        let mut proto = BytesMut::with_capacity(1 + 5 + buf.len());
        proto.put_u8(0x0a);
        encode_varint(buf.len(), &mut proto);
        proto.extend_from_slice(buf);

        let mut frame = BytesMut::with_capacity(5 + proto.len());
        frame.put_u8(0x00);
        frame.put_u32(proto.len() as u32);
        frame.extend_from_slice(&proto);

        self.send.reserve_capacity(frame.len());
        match self.send.poll_capacity(cx) {
            Poll::Ready(Some(Ok(_))) => {
                self.send
                    .send_data(frame.freeze(), false)
                    .map_err(|e| io::Error::new(io::ErrorKind::BrokenPipe, e))?;
                Poll::Ready(Ok(buf.len()))
            }
            Poll::Ready(Some(Err(e))) => Poll::Ready(Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                format!("gRPC send closed: {e}"),
            ))),
            Poll::Ready(None) => Poll::Ready(Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "gRPC send capacity closed",
            ))),
            Poll::Pending => Poll::Pending,
        }
    }

    fn poll_flush(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let _ = self.send.send_data(Bytes::new(), true);
        Poll::Ready(Ok(()))
    }
}
