//! VLESS outbound dialer with TLS, Reality, WebSocket, gRPC, and XTLS-Vision flow control.

use std::{
    io,
    net::SocketAddr,
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
};

use rand::Rng;
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt, ReadBuf};
use uuid::Uuid;

use crate::{
    dialer::{BoxFuture, BoxedStream, DirectDialer, OutboundDialer, TargetAddr},
    transport::{
        grpc::{connect_grpc, GrpcConfig},
        reality::{connect_reality, RealityConfig},
        tls::{connect_tls, TlsConfig},
        ws::{connect_ws, WsConfig},
    },
};

const VLESS_VERSION: u8 = 0x00;
const CMD_TCP: u8 = 0x01;

const VISION_CONTINUE: u8 = 0x00;
const VISION_END: u8 = 0x01;
const VISION_DIRECT: u8 = 0x02;

#[derive(Clone, Debug)]
pub enum VlessSecurity {
    None,
    Tls(TlsConfig),
    Reality(RealityConfig),
}

#[derive(Clone, Debug)]
pub enum VlessNetwork {
    Tcp,
    Ws(WsConfig),
    WsHeaders(WsConfig, http::HeaderMap),
    Grpc(GrpcConfig),
}

#[derive(Clone, Debug)]
pub struct VlessConfig {
    pub server: TargetAddr,
    pub uuid: [u8; 16],
    pub flow: Option<String>,
    pub security: VlessSecurity,
    pub network: VlessNetwork,
}

impl VlessConfig {
    pub fn parse_uuid(s: &str) -> io::Result<[u8; 16]> {
        let u = Uuid::parse_str(s.trim()).map_err(|e| {
            io::Error::new(io::ErrorKind::InvalidInput, format!("invalid UUID: {e}"))
        })?;
        Ok(*u.as_bytes())
    }
}

pub struct VlessDialer {
    cfg: VlessConfig,
    underlay: Arc<dyn OutboundDialer>,
}

impl VlessDialer {
    pub fn new(cfg: VlessConfig) -> Self {
        Self::with_underlay(cfg, Arc::new(DirectDialer))
    }

    pub fn with_underlay(cfg: VlessConfig, underlay: Arc<dyn OutboundDialer>) -> Self {
        Self { cfg, underlay }
    }
}

fn write_vless_target(target: &TargetAddr, out: &mut Vec<u8>) -> io::Result<()> {
    out.extend_from_slice(&target.port().to_be_bytes());
    match target {
        TargetAddr::Ip(SocketAddr::V4(v4)) => {
            out.push(0x01);
            out.extend_from_slice(&v4.ip().octets());
        }
        TargetAddr::Domain(host, _) => {
            let bytes = host.as_bytes();
            let len = u8::try_from(bytes.len()).map_err(|_| {
                io::Error::new(io::ErrorKind::InvalidInput, "domain exceeds 255 bytes")
            })?;
            out.push(0x02);
            out.push(len);
            out.extend_from_slice(bytes);
        }
        TargetAddr::Ip(SocketAddr::V6(v6)) => {
            out.push(0x03);
            out.extend_from_slice(&v6.ip().octets());
        }
    }
    Ok(())
}

impl OutboundDialer for VlessDialer {
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let raw = self.underlay.dial_tcp(&self.cfg.server).await?;
            let host = self.cfg.server.host();
            let sec_stream = match &self.cfg.security {
                VlessSecurity::None => raw,
                VlessSecurity::Tls(tls) => connect_tls(raw, &host, tls).await?,
                VlessSecurity::Reality(reality) => connect_reality(raw, reality).await?,
            };
            let mut transport = match &self.cfg.network {
                VlessNetwork::Tcp => sec_stream,
                VlessNetwork::Ws(ws) => connect_ws(sec_stream, &host, ws).await?,
                VlessNetwork::WsHeaders(ws, headers) => {
                    crate::transport::ws::connect_ws_headers(sec_stream, &host, ws, headers).await?
                }
                VlessNetwork::Grpc(grpc) => connect_grpc(sec_stream, &host, grpc).await?,
            };

            let is_vision = self
                .cfg
                .flow
                .as_deref()
                .is_some_and(|f| f.eq_ignore_ascii_case("xtls-rprx-vision"));

            let mut addons = Vec::new();
            if is_vision {
                let flow_bytes = b"xtls-rprx-vision";
                addons.push(0x0a); // protobuf field 1 (Flow)
                addons.push(flow_bytes.len() as u8);
                addons.extend_from_slice(flow_bytes);
            }

            let mut hdr = Vec::with_capacity(1 + 16 + 1 + addons.len() + 1 + 2 + 64);
            hdr.push(VLESS_VERSION);
            hdr.extend_from_slice(&self.cfg.uuid);
            hdr.push(addons.len() as u8);
            hdr.extend_from_slice(&addons);
            hdr.push(CMD_TCP);
            write_vless_target(target, &mut hdr)?;

            transport.write_all(&hdr).await?;
            transport.flush().await?;

            let vless_stream = VlessStream::new(transport);
            if is_vision {
                Ok(
                    Box::new(VisionStream::new(Box::new(vless_stream), self.cfg.uuid))
                        as BoxedStream,
                )
            } else {
                Ok(Box::new(vless_stream) as BoxedStream)
            }
        })
    }
}

enum RespState {
    Header { buf: [u8; 2], filled: usize },
    Addons { buf: Vec<u8>, filled: usize },
    Ready,
}

struct VlessStream {
    inner: BoxedStream,
    resp: RespState,
}

impl VlessStream {
    fn new(inner: BoxedStream) -> Self {
        Self {
            inner,
            resp: RespState::Header {
                buf: [0u8; 2],
                filled: 0,
            },
        }
    }
}

impl AsyncRead for VlessStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            let this = self.as_mut().get_mut();
            match &mut this.resp {
                RespState::Header { buf, filled } => {
                    while *filled < 2 {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "VLESS server closed before response header",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    if buf[0] != VLESS_VERSION {
                        return Poll::Ready(Err(io::Error::new(
                            io::ErrorKind::InvalidData,
                            format!("unexpected VLESS response version: {}", buf[0]),
                        )));
                    }
                    let addons_len = buf[1] as usize;
                    this.resp = if addons_len == 0 {
                        RespState::Ready
                    } else {
                        RespState::Addons {
                            buf: vec![0u8; addons_len],
                            filled: 0,
                        }
                    };
                }
                RespState::Addons { buf, filled } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated VLESS response addons",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    this.resp = RespState::Ready;
                }
                RespState::Ready => return Pin::new(&mut this.inner).poll_read(cx, out),
            }
        }
    }
}

impl AsyncWrite for VlessStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.inner).poll_write(cx, buf)
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.inner).poll_flush(cx)
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.inner).poll_shutdown(cx)
    }
}

enum VisionReadState {
    FirstUuid {
        buf: [u8; 16],
        filled: usize,
    },
    FrameHeader {
        buf: [u8; 5],
        filled: usize,
    },
    FrameContent {
        cmd: u8,
        pad_len: usize,
        buf: Vec<u8>,
        filled: usize,
    },
    FramePadding {
        buf: Vec<u8>,
        filled: usize,
        next_direct: bool,
    },
    Direct,
}

/// XTLS-Vision padding stream wrapper over `VlessStream`.
struct VisionStream {
    inner: BoxedStream,
    uuid: [u8; 16],
    write_uuid_sent: bool,
    write_direct: bool,
    write_buf: Vec<u8>,
    write_pos: usize,
    pending_plain_len: usize,
    read_state: VisionReadState,
    plain_buf: Vec<u8>,
    plain_pos: usize,
}

impl VisionStream {
    fn new(inner: BoxedStream, uuid: [u8; 16]) -> Self {
        Self {
            inner,
            uuid,
            write_uuid_sent: false,
            write_direct: false,
            write_buf: Vec::new(),
            write_pos: 0,
            pending_plain_len: 0,
            read_state: VisionReadState::FirstUuid {
                buf: [0u8; 16],
                filled: 0,
            },
            plain_buf: Vec::new(),
            plain_pos: 0,
        }
    }

    fn flush_write_buf(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        while self.write_pos < self.write_buf.len() {
            match Pin::new(&mut self.inner).poll_write(cx, &self.write_buf[self.write_pos..]) {
                Poll::Ready(Ok(0)) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "Vision stream closed",
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

impl AsyncRead for VisionStream {
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
            let this = self.as_mut().get_mut();
            match &mut this.read_state {
                VisionReadState::Direct => return Pin::new(&mut this.inner).poll_read(cx, out),
                VisionReadState::FirstUuid { buf, filled } => {
                    while *filled < 16 {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Ok(()));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    this.read_state = VisionReadState::FrameHeader {
                        buf: [0u8; 5],
                        filled: 0,
                    };
                }
                VisionReadState::FrameHeader { buf, filled } => {
                    while *filled < 5 {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Ok(()));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    let cmd = buf[0];
                    let content_len = u16::from_be_bytes([buf[1], buf[2]]) as usize;
                    let pad_len = u16::from_be_bytes([buf[3], buf[4]]) as usize;
                    this.read_state = VisionReadState::FrameContent {
                        cmd,
                        pad_len,
                        buf: vec![0u8; content_len],
                        filled: 0,
                    };
                }
                VisionReadState::FrameContent {
                    cmd,
                    pad_len,
                    buf,
                    filled,
                } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated Vision content",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    this.plain_buf = std::mem::take(buf);
                    this.plain_pos = 0;
                    let next_direct = *cmd == VISION_DIRECT || *cmd == VISION_END;
                    this.read_state = VisionReadState::FramePadding {
                        buf: vec![0u8; *pad_len],
                        filled: 0,
                        next_direct,
                    };
                }
                VisionReadState::FramePadding {
                    buf,
                    filled,
                    next_direct,
                } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated Vision padding",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    this.read_state = if *next_direct {
                        VisionReadState::Direct
                    } else {
                        VisionReadState::FrameHeader {
                            buf: [0u8; 5],
                            filled: 0,
                        }
                    };
                }
            }
        }
    }
}

impl AsyncWrite for VisionStream {
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
        if self.write_direct {
            return Pin::new(&mut self.inner).poll_write(cx, buf);
        }
        if buf.is_empty() {
            return Poll::Ready(Ok(0));
        }
        let take = buf.len().min(8192 - 21);
        let pad_len: usize = if take < 900 {
            rand::thread_rng().gen_range(0..256)
        } else {
            0
        };
        let this = self.as_mut().get_mut();
        this.write_buf.clear();
        this.write_pos = 0;
        if !this.write_uuid_sent {
            this.write_buf.extend_from_slice(&this.uuid);
            this.write_uuid_sent = true;
        }
        // Transition to Direct mode once we've sent a Vision Continue or End frame;
        // for simplicity and compatibility, emit Continue on first packet if TLS ClientHello, else End.
        let cmd = if buf.starts_with(&[0x16, 0x03]) {
            VISION_CONTINUE
        } else {
            this.write_direct = true;
            VISION_END
        };
        this.write_buf.push(cmd);
        this.write_buf
            .extend_from_slice(&(take as u16).to_be_bytes());
        this.write_buf
            .extend_from_slice(&(pad_len as u16).to_be_bytes());
        this.write_buf.extend_from_slice(&buf[..take]);
        this.write_buf.resize(this.write_buf.len() + pad_len, 0);
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
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => Pin::new(&mut self.inner).poll_shutdown(cx),
            other => other,
        }
    }
}
