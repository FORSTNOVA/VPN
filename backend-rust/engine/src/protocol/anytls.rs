//! AnyTLS outbound dialer and v2 frame multiplexer over TLS.

use std::{
    io,
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
};

use rand::Rng;
use sha2::{Digest, Sha256};
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt, ReadBuf};

use crate::{
    dialer::{BoxFuture, BoxedStream, DirectDialer, OutboundDialer, TargetAddr},
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

impl OutboundDialer for AnyTlsDialer {
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
