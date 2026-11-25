//! AnyTLS outbound dialer and v2 frame multiplexer over TLS.

use std::{
    io,
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
};

use rand::Rng;
use sha2::{Digest, Sha256};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, ReadBuf, ReadHalf, WriteHalf};

use crate::{
    dialer::{
        BoxFuture, BoxedStream, DirectDialer, OutboundDialer, OutboundUdpDialer, TargetAddr,
        UdpPacketSocket,
    },
    transport::tls::{connect_tls, TlsConfig},
};

const CMD_WASTE: u8 = 0;
const CMD_SYN: u8 = 1;
const CMD_PSH: u8 = 2;
const CMD_FIN: u8 = 3;
const CMD_SETTINGS: u8 = 4;
const CMD_ALERT: u8 = 5;
const CMD_UPDATE_PADDING_SCHEME: u8 = 6;
const CMD_SYNACK: u8 = 7;
const CMD_HEART_REQUEST: u8 = 8;
const CMD_HEART_RESPONSE: u8 = 9;
const CMD_SERVER_SETTINGS: u8 = 10;

const HEADER_LEN: usize = 7;
const MAX_FRAME_DATA: usize = 16 * 1024;
const STREAM_ID: u32 = 1;

#[derive(Clone, Debug)]
pub struct AnyTlsConfig {
    pub server: TargetAddr,
    pub password: String,
    pub tls: TlsConfig,
}

pub struct AnyTlsDialer {
    cfg: AnyTlsConfig,
    password_sha256: [u8; 32],
    underlay: Arc<dyn OutboundDialer>,
}

impl AnyTlsDialer {
    pub fn new(cfg: AnyTlsConfig) -> Self {
        Self::with_underlay(cfg, Arc::new(DirectDialer))
    }

    pub fn with_underlay(cfg: AnyTlsConfig, underlay: Arc<dyn OutboundDialer>) -> Self {
        let password_sha256: [u8; 32] = Sha256::digest(cfg.password.as_bytes()).into();
        Self {
            cfg,
            password_sha256,
            underlay,
        }
    }
}

fn push_frame(out: &mut Vec<u8>, cmd: u8, sid: u32, data: &[u8]) -> io::Result<()> {
    let len = u16::try_from(data.len()).map_err(|_| {
        io::Error::new(io::ErrorKind::InvalidInput, "AnyTLS frame exceeds 65535 bytes")
    })?;
    out.push(cmd);
    out.extend_from_slice(&sid.to_be_bytes());
    out.extend_from_slice(&len.to_be_bytes());
    out.extend_from_slice(data);
    Ok(())
}

const UOT_MAGIC_ADDRESS: &str = "sp.v2.udp-over-tcp.arpa";

impl OutboundDialer for AnyTlsDialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }

    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let raw = self.underlay.dial_tcp(&self.cfg.server).await?;
            let mut tls = connect_tls(raw, &self.cfg.server.host(), &self.cfg.tls).await?;

            let padding_len: usize = rand::thread_rng().gen_range(16..=64);
            let mut init = Vec::with_capacity(32 + 2 + padding_len + 64);
            init.extend_from_slice(&self.password_sha256);
            init.extend_from_slice(&(padding_len as u16).to_be_bytes());
            init.resize(init.len() + padding_len, 0);

            // Advertise v2 client capabilities and open stream 1 with destination SOCKS address.
            push_frame(&mut init, CMD_SETTINGS, 0, b"v=2\nclient=smartvpn-engine/0.3\n")?;
            push_frame(&mut init, CMD_SYN, STREAM_ID, &[])?;

            let mut addr_buf = Vec::with_capacity(64);
            target.write_socks_addr(&mut addr_buf)?;
            push_frame(&mut init, CMD_PSH, STREAM_ID, &addr_buf)?;

            tls.write_all(&init).await?;
            tls.flush().await?;

            Ok(Box::new(AnyTlsStream::new(tls, STREAM_ID)) as BoxedStream)
        })
    }
}

enum FrameReadState {
    Header { buf: [u8; HEADER_LEN], filled: usize },
    Payload { cmd: u8, sid: u32, buf: Vec<u8>, filled: usize },
}

pub struct AnyTlsStream {
    inner: BoxedStream,
    sid: u32,
    read_state: FrameReadState,
    eof: bool,
    plain_buf: Vec<u8>,
    plain_pos: usize,
    write_buf: Vec<u8>,
    write_pos: usize,
    pending_plain_len: usize,
    fin_sent: bool,
}

impl AnyTlsStream {
    fn new(inner: BoxedStream, sid: u32) -> Self {
        Self {
            inner,
            sid,
            read_state: FrameReadState::Header {
                buf: [0u8; HEADER_LEN],
                filled: 0,
            },
            eof: false,
            plain_buf: Vec::new(),
            plain_pos: 0,
            write_buf: Vec::new(),
            write_pos: 0,
            pending_plain_len: 0,
            fin_sent: false,
        }
    }

    fn flush_write_buf(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        while self.write_pos < self.write_buf.len() {
            match Pin::new(&mut self.inner).poll_write(cx, &self.write_buf[self.write_pos..]) {
                Poll::Ready(Ok(0)) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "AnyTLS underlying stream closed",
                    )));
                }
                Poll::Ready(Ok(n)) => self.write_pos += n,
                Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                Poll::Pending => return Poll::Pending,
            }
        }
        self.write_buf.clear();
        self.write_pos = 0;
        Poll::Ready(Ok(()))
    }
}

impl AsyncRead for AnyTlsStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            if self.plain_pos < self.plain_buf.len() {
                let avail = &self.plain_buf[self.plain_pos..];
                let n = avail.len().min(out.remaining());
                out.put_slice(&avail[..n]);
                self.plain_pos += n;
                if self.plain_pos == self.plain_buf.len() {
                    self.plain_buf.clear();
                    self.plain_pos = 0;
                }
                return Poll::Ready(Ok(()));
            }
            if self.eof {
                return Poll::Ready(Ok(()));
            }

            let this = self.as_mut().get_mut();
            match &mut this.read_state {
                FrameReadState::Header { buf, filled } => {
                    while *filled < HEADER_LEN {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return if *filled == 0 {
                                        this.eof = true;
                                        Poll::Ready(Ok(()))
                                    } else {
                                        Poll::Ready(Err(io::Error::new(
                                            io::ErrorKind::UnexpectedEof,
                                            "truncated AnyTLS frame header",
                                        )))
                                    };
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    let cmd = buf[0];
                    let sid = u32::from_be_bytes([buf[1], buf[2], buf[3], buf[4]]);
                    let len = u16::from_be_bytes([buf[5], buf[6]]) as usize;
                    this.read_state = FrameReadState::Payload {
                        cmd,
                        sid,
                        buf: vec![0u8; len],
                        filled: 0,
                    };
                }
                FrameReadState::Payload { cmd, sid, buf, filled } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated AnyTLS frame payload",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    let cmd_val = *cmd;
                    let sid_val = *sid;
                    let payload = std::mem::take(buf);
                    this.read_state = FrameReadState::Header {
                        buf: [0u8; HEADER_LEN],
                        filled: 0,
                    };

                    match cmd_val {
                        CMD_PSH if sid_val == this.sid => {
                            this.plain_buf = payload;
                            this.plain_pos = 0;
                        }
                        CMD_FIN if sid_val == this.sid => {
                            this.eof = true;
                            return Poll::Ready(Ok(()));
                        }
                        CMD_ALERT => {
                            let msg = String::from_utf8_lossy(&payload);
                            return Poll::Ready(Err(io::Error::new(
                                io::ErrorKind::ConnectionRefused,
                                format!("AnyTLS alert: {msg}"),
                            )));
                        }
                        CMD_WASTE
                        | CMD_SYNACK
                        | CMD_UPDATE_PADDING_SCHEME
                        | CMD_HEART_REQUEST
                        | CMD_HEART_RESPONSE
                        | CMD_SERVER_SETTINGS
                        | _ => {}
                    }
                }
            }
        }
    }
}

impl AsyncWrite for AnyTlsStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        if self.write_pos < self.write_buf.len() {
            match self.as_mut().flush_write_buf(cx) {
                Poll::Ready(Ok(())) => {
                    let accepted = self.pending_plain_len;
                    self.pending_plain_len = 0;
                    if accepted > 0 {
                        return Poll::Ready(Ok(accepted));
                    }
                }
                Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                Poll::Pending => return Poll::Pending,
            }
        }
        if buf.is_empty() {
            return Poll::Ready(Ok(0));
        }
        let take = buf.len().min(MAX_FRAME_DATA);
        let sid = self.sid;
        let this = self.as_mut().get_mut();
        this.write_buf.clear();
        this.write_pos = 0;
        push_frame(&mut this.write_buf, CMD_PSH, sid, &buf[..take])?;
        this.pending_plain_len = take;
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => {
                self.pending_plain_len = 0;
                Poll::Ready(Ok(take))
            }
            Poll::Ready(Err(e)) => Poll::Ready(Err(e)),
            Poll::Pending => Poll::Pending,
        }
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => Pin::new(&mut self.inner).poll_flush(cx),
            other => other,
        }
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if !self.fin_sent {
            let sid = self.sid;
            push_frame(&mut self.write_buf, CMD_FIN, sid, &[])?;
            self.fin_sent = true;
        }
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => Pin::new(&mut self.inner).poll_shutdown(cx),
            other => other,
        }
    }
}

impl OutboundUdpDialer for AnyTlsDialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async move {
            let magic = TargetAddr::Domain(UOT_MAGIC_ADDRESS.to_string(), 0);
            let stream = self.dial_tcp(&magic).await?;
            Ok(Box::new(AnyTlsUdpSocket {
                stream: tokio::sync::Mutex::new(Some(stream)),
                state: tokio::sync::Mutex::new(None),
                reader: tokio::sync::Mutex::new(None),
                writer: tokio::sync::Mutex::new(None),
                ready: tokio::sync::Notify::new(),
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}

struct AnyTlsUdpSocket {
    stream: tokio::sync::Mutex<Option<BoxedStream>>,
    state: tokio::sync::Mutex<Option<TargetAddr>>,
    reader: tokio::sync::Mutex<Option<ReadHalf<BoxedStream>>>,
    writer: tokio::sync::Mutex<Option<WriteHalf<BoxedStream>>>,
    ready: tokio::sync::Notify,
}

impl UdpPacketSocket for AnyTlsUdpSocket {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            let mut w_guard = self.writer.lock().await;
            if w_guard.is_none() {
                let mut stream = self.stream.lock().await.take().ok_or_else(|| {
                    io::Error::new(io::ErrorKind::BrokenPipe, "AnyTLS UoT stream already consumed")
                })?;
                let mut hdr = Vec::with_capacity(64);
                hdr.push(0x01); // isConnect = true
                target.write_socks_addr(&mut hdr)?;
                stream.write_all(&hdr).await?;
                stream.flush().await?;
                let (r, w) = tokio::io::split(stream);
                *self.state.lock().await = Some(target.clone());
                *self.reader.lock().await = Some(r);
                *w_guard = Some(w);
                self.ready.notify_waiters();
            }
            let len = u16::try_from(payload.len())
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "UDP payload too large"))?;
            let mut pkt = Vec::with_capacity(2 + payload.len());
            pkt.extend_from_slice(&len.to_be_bytes());
            pkt.extend_from_slice(payload);
            let w = w_guard.as_mut().expect("writer initialized");
            w.write_all(&pkt).await?;
            w.flush().await?;
            Ok(payload.len())
        })
    }

    fn recv_from<'a>(
        &'a self,
        buf: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            loop {
                {
                    let mut r_guard = self.reader.lock().await;
                    if let Some(r) = r_guard.as_mut() {
                        let mut len_buf = [0u8; 2];
                        r.read_exact(&mut len_buf).await?;
                        let len = u16::from_be_bytes(len_buf) as usize;
                        if len > buf.len() {
                            let mut discard = vec![0u8; len];
                            r.read_exact(&mut discard).await?;
                            return Err(io::Error::new(
                                io::ErrorKind::InvalidInput,
                                "receive buffer too small for AnyTLS UoT packet",
                            ));
                        }
                        r.read_exact(&mut buf[..len]).await?;
                        let peer = self
                            .state
                            .lock()
                            .await
                            .clone()
                            .unwrap_or_else(|| TargetAddr::Ip(([0, 0, 0, 0], 0).into()));
                        return Ok((len, peer));
                    }
                }
                self.ready.notified().await;
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn anytls_uot_connected_roundtrip() {
        let (client_io, mut server_io) = tokio::io::duplex(4096);
        let sock = AnyTlsUdpSocket {
            stream: tokio::sync::Mutex::new(Some(Box::new(client_io) as BoxedStream)),
            state: tokio::sync::Mutex::new(None),
            reader: tokio::sync::Mutex::new(None),
            writer: tokio::sync::Mutex::new(None),
            ready: tokio::sync::Notify::new(),
        };
        let target = TargetAddr::Ip("1.2.3.4:8443".parse().unwrap());

        let srv = tokio::spawn(async move {
            let mut hdr = [0u8; 8];
            server_io.read_exact(&mut hdr).await.unwrap();
            assert_eq!(hdr[0], 0x01); // isConnect
            assert_eq!(hdr[1], 0x01); // SOCKS5 IPv4 ATYP
            assert_eq!(&hdr[2..6], &[1, 2, 3, 4]);
            assert_eq!(u16::from_be_bytes([hdr[6], hdr[7]]), 8443);

            for expected in [b"hello".as_slice(), b"world-packet".as_slice()] {
                let mut l = [0u8; 2];
                server_io.read_exact(&mut l).await.unwrap();
                let n = u16::from_be_bytes(l) as usize;
                let mut data = vec![0u8; n];
                server_io.read_exact(&mut data).await.unwrap();
                assert_eq!(data, expected);
                server_io.write_all(&l).await.unwrap();
                server_io.write_all(&data).await.unwrap();
            }
            server_io.flush().await.unwrap();
        });

        assert_eq!(sock.send_to(b"hello", &target).await.unwrap(), 5);
        assert_eq!(sock.send_to(b"world-packet", &target).await.unwrap(), 12);
        let mut buf = [0u8; 64];
        let (n1, p1) = sock.recv_from(&mut buf).await.unwrap();
        assert_eq!(&buf[..n1], b"hello");
        assert_eq!(p1, target);
        let (n2, p2) = sock.recv_from(&mut buf).await.unwrap();
        assert_eq!(&buf[..n2], b"world-packet");
        assert_eq!(p2, target);
        srv.await.unwrap();
    }
}
