//! VLESS outbound dialer with TLS, Reality, WebSocket, gRPC, and XTLS-Vision flow control.

use std::{
    io,
    net::SocketAddr,
    pin::Pin,
    sync::{atomic::Ordering, Arc},
    task::{ready, Context, Poll},
};

use rand::Rng;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, ReadBuf, ReadHalf, WriteHalf};
use uuid::Uuid;

use crate::{
    dialer::{
        BoxFuture, BoxedStream, DirectDialer, OutboundDialer, OutboundUdpDialer, TargetAddr,
        UdpPacketSocket,
    },
    transport::{
        grpc::{connect_grpc, GrpcConfig},
        reality::{connect_reality_with_splice, RealityConfig},
        tls::{connect_tls, connect_tls_spliceable, SpliceFlags, TlsConfig},
        ws::{connect_ws, WsConfig},
    },
};

const VLESS_VERSION: u8 = 0x00;
const CMD_TCP: u8 = 0x01;
const CMD_UDP: u8 = 0x02;

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

impl VlessDialer {
    async fn open_cmd(
        cfg: &VlessConfig,
        underlay: &Arc<dyn OutboundDialer>,
        cmd: u8,
        target: &TargetAddr,
        allow_vision: bool,
    ) -> io::Result<BoxedStream> {
        let raw = underlay.dial_tcp(&cfg.server).await?;
        let host = cfg.server.host();
        let is_vision = allow_vision
            && cfg
                .flow
                .as_deref()
                .is_some_and(|f| f.eq_ignore_ascii_case("xtls-rprx-vision"));

        let mut splice = None;
        let sec_stream = match &cfg.security {
            VlessSecurity::None => raw,
            VlessSecurity::Tls(tls) if is_vision => {
                let flags = Arc::new(SpliceFlags::default());
                let stream = connect_tls_spliceable(raw, &host, tls, flags.clone()).await?;
                splice = Some(flags);
                stream
            }
            VlessSecurity::Tls(tls) => connect_tls(raw, &host, tls).await?,
            VlessSecurity::Reality(reality) => {
                splice = is_vision.then(|| Arc::new(SpliceFlags::default()));
                connect_reality_with_splice(raw, reality, splice.clone()).await?
            }
        };
        let mut transport = match &cfg.network {
            VlessNetwork::Tcp => sec_stream,
            VlessNetwork::Ws(ws) => connect_ws(sec_stream, &host, ws).await?,
            VlessNetwork::WsHeaders(ws, headers) => {
                crate::transport::ws::connect_ws_headers(sec_stream, &host, ws, headers).await?
            }
            VlessNetwork::Grpc(grpc) => connect_grpc(sec_stream, &host, grpc).await?,
        };

        let mut addons = Vec::new();
        if is_vision {
            let flow_bytes = b"xtls-rprx-vision";
            addons.push(0x0a); // protobuf field 1 (Flow)
            addons.push(flow_bytes.len() as u8);
            addons.extend_from_slice(flow_bytes);
        }

        let mut hdr = Vec::with_capacity(1 + 16 + 1 + addons.len() + 1 + 2 + 64);
        hdr.push(VLESS_VERSION);
        hdr.extend_from_slice(&cfg.uuid);
        hdr.push(addons.len() as u8);
        hdr.extend_from_slice(&addons);
        hdr.push(cmd);
        write_vless_target(target, &mut hdr)?;

        transport.write_all(&hdr).await?;
        transport.flush().await?;

        let vless_stream = VlessStream::new(transport);
        if is_vision {
            Ok(
                Box::new(VisionStream::new(Box::new(vless_stream), cfg.uuid, splice))
                    as BoxedStream,
            )
        } else {
            Ok(Box::new(vless_stream) as BoxedStream)
        }
    }
}

impl OutboundDialer for VlessDialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }

    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(
            async move { Self::open_cmd(&self.cfg, &self.underlay, CMD_TCP, target, true).await },
        )
    }
}

impl OutboundUdpDialer for VlessDialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async move {
            Ok(Box::new(VlessUdpSocket {
                cfg: self.cfg.clone(),
                underlay: self.underlay.clone(),
                state: tokio::sync::Mutex::new(None),
                reader: tokio::sync::Mutex::new(None),
                writer: tokio::sync::Mutex::new(None),
                ready: tokio::sync::Notify::new(),
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}

struct VlessUdpSocket {
    cfg: VlessConfig,
    underlay: Arc<dyn OutboundDialer>,
    state: tokio::sync::Mutex<Option<TargetAddr>>,
    reader: tokio::sync::Mutex<Option<ReadHalf<BoxedStream>>>,
    writer: tokio::sync::Mutex<Option<WriteHalf<BoxedStream>>>,
    ready: tokio::sync::Notify,
}

impl UdpPacketSocket for VlessUdpSocket {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            let mut w_guard = self.writer.lock().await;
            if w_guard.is_none() {
                let stream =
                    VlessDialer::open_cmd(&self.cfg, &self.underlay, CMD_UDP, target, false)
                        .await?;
                let (r, w) = tokio::io::split(stream);
                *self.state.lock().await = Some(target.clone());
                *self.reader.lock().await = Some(r);
                *w_guard = Some(w);
                self.ready.notify_waiters();
            }
            let len = u16::try_from(payload.len()).map_err(|_| {
                io::Error::new(io::ErrorKind::InvalidInput, "UDP payload too large")
            })?;
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
                                "receive buffer too small for VLESS UDP packet",
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
        cmd: u8,
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
    pending_cmd: u8,
    splice: Option<Arc<SpliceFlags>>,
    read_state: VisionReadState,
    pre_buf: Vec<u8>,
    pre_pos: usize,
    plain_buf: Vec<u8>,
    plain_pos: usize,
}

impl VisionStream {
    fn new(inner: BoxedStream, uuid: [u8; 16], splice: Option<Arc<SpliceFlags>>) -> Self {
        Self {
            inner,
            uuid,
            write_uuid_sent: false,
            write_direct: false,
            write_buf: Vec::new(),
            write_pos: 0,
            pending_cmd: VISION_CONTINUE,
            splice,
            read_state: VisionReadState::FirstUuid {
                buf: [0u8; 16],
                filled: 0,
            },
            pre_buf: Vec::new(),
            pre_pos: 0,
            plain_buf: Vec::new(),
            plain_pos: 0,
        }
    }

    fn poll_read_inner(
        &mut self,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        if self.pre_pos < self.pre_buf.len() {
            let avail = &self.pre_buf[self.pre_pos..];
            let n = avail.len().min(out.remaining());
            out.put_slice(&avail[..n]);
            self.pre_pos += n;
            if self.pre_pos == self.pre_buf.len() {
                self.pre_buf.clear();
                self.pre_pos = 0;
            }
            return Poll::Ready(Ok(()));
        }
        Pin::new(&mut self.inner).poll_read(cx, out)
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
        if self.pending_cmd == VISION_DIRECT {
            ready!(Pin::new(&mut self.inner).poll_flush(cx))?;
            if let Some(flags) = &self.splice {
                flags.write.store(true, Ordering::Release);
            }
        }
        if self.pending_cmd != VISION_CONTINUE {
            self.write_direct = true;
            self.pending_cmd = VISION_CONTINUE;
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
            let mut state = std::mem::replace(&mut this.read_state, VisionReadState::Direct);
            match &mut state {
                VisionReadState::Direct => {
                    this.read_state = state;
                    return this.poll_read_inner(cx, out);
                }
                VisionReadState::FirstUuid { buf, filled } => {
                    while *filled < 16 {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    if *filled > 0 {
                                        this.pre_buf = buf[..*filled].to_vec();
                                        this.pre_pos = 0;
                                        this.read_state = VisionReadState::Direct;
                                        return this.poll_read_inner(cx, out);
                                    }
                                    this.read_state = state;
                                    return Poll::Ready(Ok(()));
                                }
                                *filled += n;
                                if buf[..*filled] != this.uuid[..*filled] {
                                    break;
                                }
                            }
                            Poll::Ready(Err(e)) => {
                                this.read_state = state;
                                return Poll::Ready(Err(e));
                            }
                            Poll::Pending => {
                                this.read_state = state;
                                return Poll::Pending;
                            }
                        }
                    }
                    if *filled < 16 || *buf != this.uuid {
                        this.pre_buf = buf[..*filled].to_vec();
                        this.pre_pos = 0;
                    }
                    this.read_state = VisionReadState::FrameHeader {
                        buf: [0u8; 5],
                        filled: 0,
                    };
                }
                VisionReadState::FrameHeader { buf, filled } => {
                    while *filled < 5 {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match this.poll_read_inner(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    this.read_state = state;
                                    return Poll::Ready(Ok(()));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => {
                                this.read_state = state;
                                return Poll::Ready(Err(e));
                            }
                            Poll::Pending => {
                                this.read_state = state;
                                return Poll::Pending;
                            }
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
                        match this.poll_read_inner(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    this.read_state = state;
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated Vision content",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => {
                                this.read_state = state;
                                return Poll::Ready(Err(e));
                            }
                            Poll::Pending => {
                                this.read_state = state;
                                return Poll::Pending;
                            }
                        }
                    }
                    this.plain_buf = std::mem::take(buf);
                    this.plain_pos = 0;
                    this.read_state = VisionReadState::FramePadding {
                        buf: vec![0u8; *pad_len],
                        filled: 0,
                        cmd: *cmd,
                    };
                }
                VisionReadState::FramePadding { buf, filled, cmd } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match this.poll_read_inner(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    this.read_state = state;
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated Vision padding",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => {
                                this.read_state = state;
                                return Poll::Ready(Err(e));
                            }
                            Poll::Pending => {
                                this.read_state = state;
                                return Poll::Pending;
                            }
                        }
                    }
                    if *cmd == VISION_DIRECT {
                        if let Some(flags) = &this.splice {
                            flags.read.store(true, Ordering::Release);
                        }
                    }
                    this.read_state = if matches!(*cmd, VISION_DIRECT | VISION_END) {
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
        ready!(self.as_mut().flush_write_buf(cx))?;
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
        // Match XTLS-Vision framing: keep padding through Handshake (0x16 0x03) and
        // ChangeCipherSpec (0x14 0x03), switch to DIRECT (0x02) on TLS 1.3 ApplicationData
        // (0x17 0x03 0x03), and END (0x01) for non-TLS traffic.
        let cmd = if buf.starts_with(&[0x16, 0x03]) || buf.starts_with(&[0x14, 0x03]) {
            VISION_CONTINUE
        } else if buf.starts_with(&[0x17, 0x03, 0x03]) {
            VISION_DIRECT
        } else {
            VISION_END
        };
        this.write_buf.push(cmd);
        this.write_buf
            .extend_from_slice(&(take as u16).to_be_bytes());
        this.write_buf
            .extend_from_slice(&(pad_len as u16).to_be_bytes());
        this.write_buf.extend_from_slice(&buf[..take]);
        this.write_buf.resize(this.write_buf.len() + pad_len, 0);
        this.pending_cmd = cmd;
        // This frame owns the accepted input even if the underlay is blocked.
        // The next write, flush or shutdown must finish it before proceeding.
        if let Poll::Ready(result) = this.flush_write_buf(cx) {
            result?;
        }
        Poll::Ready(Ok(take))
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

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn vision_stream_reads_server_frames_without_expecting_uuid() {
        let (client_io, mut server_io) = tokio::io::duplex(4096);
        let uuid = [0x11u8; 16];
        let mut vision = VisionStream::new(Box::new(client_io) as BoxedStream, uuid, None);

        // Client sends TLS ClientHello (0x16 0x03 ...), server replies with Vision CONTINUE
        // (no 16-byte UUID!) followed by DIRECT and raw TLS records.
        let srv = tokio::spawn(async move {
            let mut client_uuid = [0u8; 16];
            server_io.read_exact(&mut client_uuid).await.unwrap();
            assert_eq!(client_uuid, uuid);

            let mut hdr = [0u8; 5];
            server_io.read_exact(&mut hdr).await.unwrap();
            assert_eq!(hdr[0], VISION_CONTINUE);
            let clen = u16::from_be_bytes([hdr[1], hdr[2]]) as usize;
            let plen = u16::from_be_bytes([hdr[3], hdr[4]]) as usize;
            let mut body = vec![0u8; clen + plen];
            server_io.read_exact(&mut body).await.unwrap();
            assert_eq!(&body[..clen], b"\x16\x03\x01hello");

            // Server sends VISION_DIRECT frame (no leading UUID) + raw trailing bytes.
            let msg1 = b"\x16\x03\x03srv";
            let mut resp = Vec::new();
            resp.push(VISION_DIRECT);
            resp.extend_from_slice(&(msg1.len() as u16).to_be_bytes());
            resp.extend_from_slice(&3u16.to_be_bytes());
            resp.extend_from_slice(msg1);
            resp.extend_from_slice(&[0u8; 3]);
            resp.extend_from_slice(b"raw-direct-data");
            server_io.write_all(&resp).await.unwrap();
            server_io.flush().await.unwrap();
        });

        vision.write_all(b"\x16\x03\x01hello").await.unwrap();
        vision.flush().await.unwrap();

        let mut got = vec![0u8; 64];
        let n1 = vision.read(&mut got).await.unwrap();
        assert_eq!(&got[..n1], b"\x16\x03\x03srv");
        let mut rest = [0u8; 15];
        vision.read_exact(&mut rest).await.unwrap();
        assert_eq!(&rest, b"raw-direct-data");
        srv.await.unwrap();
    }

    async fn check_tls_transition(up: u8, down: u8) {
        use rustls::pki_types::PrivatePkcs8KeyDer;
        use std::io::Write;

        crate::transport::tls::ensure_crypto_provider();
        let cert = rcgen::generate_simple_self_signed(vec!["localhost".into()]).unwrap();
        let mut server_config = rustls::ServerConfig::builder()
            .with_no_client_auth()
            .with_single_cert(
                vec![cert.cert.der().clone()],
                PrivatePkcs8KeyDer::from(cert.key_pair.serialize_der()).into(),
            )
            .unwrap();
        server_config.send_tls13_tickets = 0;
        let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(server_config));
        let flags = Arc::new(SpliceFlags::default());
        let cfg = TlsConfig {
            insecure: true,
            ..Default::default()
        };
        let (client, peer) = tokio::io::duplex(128);
        let (tls, peer) = tokio::join!(
            connect_tls_spliceable(Box::new(client), "localhost", &cfg, flags.clone()),
            acceptor.accept(peer),
        );
        let uuid = [0x42; 16];
        let mut vision = VisionStream::new(
            Box::new(VlessStream::new(tls.unwrap())),
            uuid,
            Some(flags.clone()),
        );
        let mut peer = peer.unwrap();

        // One raw write queues the outer record and its immediate successor.
        // Omit the UUID and use a short frame to exercise initial lookahead.
        let mut wire = Vec::new();
        let (raw, conn) = peer.get_mut();
        conn.writer()
            .write_all(&[0, 0, down, 0, 1, 0, 2, b'x'])
            .unwrap();
        conn.write_tls(&mut wire).unwrap();
        raw.write_all(&wire).await.unwrap();
        let mut first = [0; 1];
        vision.read_exact(&mut first).await.unwrap();
        assert_eq!(&first, b"x");
        assert!(!flags.read.load(Ordering::Acquire));

        wire.clear();
        conn.writer().write_all(b"ppA").unwrap(); // padding + leftover plaintext
        conn.write_tls(&mut wire).unwrap();
        let tail = b"\x17\x03\x03\x00\x03end";
        if down == VISION_DIRECT {
            wire.extend_from_slice(tail);
        } else {
            conn.writer().write_all(tail).unwrap();
            conn.write_tls(&mut wire).unwrap();
        }
        raw.write_all(&wire).await.unwrap();
        let mut rest = [0; 9];
        vision.read_exact(&mut rest).await.unwrap();
        assert_eq!(rest[0], b'A');
        assert_eq!(&rest[1..], tail);
        assert_eq!(flags.read.load(Ordering::Acquire), down == VISION_DIRECT);
        assert!(!flags.write.load(Ordering::Acquire));

        // The terminal frame cannot fit in the duplex buffer. For DIRECT
        // poll_flush must finish its outer record before publishing the flag.
        let mut payload = vec![0xab; 1024];
        if up == VISION_DIRECT {
            payload[..3].copy_from_slice(&[0x17, 3, 3]);
        }
        vision.write_all(&payload).await.unwrap();
        assert!(!flags.write.load(Ordering::Acquire));
        assert!(futures_util::poll!(std::pin::pin!(vision.flush())).is_pending());
        let (flushed, received) = tokio::join!(vision.flush(), async {
            let mut frame = vec![0; 21 + payload.len()];
            peer.read_exact(&mut frame).await.unwrap();
            frame
        });
        flushed.unwrap();
        assert_eq!(&received[..16], &uuid);
        assert_eq!(&received[16..21], &[up, 4, 0, 0, 0]);
        assert_eq!(&received[21..], &payload);
        assert_eq!(flags.write.load(Ordering::Acquire), up == VISION_DIRECT);
        vision.write_all(tail).await.unwrap();
        vision.shutdown().await.unwrap();
        let mut last = Vec::new();
        if up == VISION_DIRECT {
            peer.get_mut().0.read_to_end(&mut last).await.unwrap();
        } else {
            peer.read_to_end(&mut last).await.unwrap();
        }
        assert_eq!(last, tail);
    }

    #[tokio::test]
    async fn vision_direct_uplink_and_end_downlink() {
        tokio::time::timeout(
            std::time::Duration::from_secs(10),
            check_tls_transition(VISION_DIRECT, VISION_END),
        )
        .await
        .unwrap();
    }

    #[tokio::test]
    async fn vision_end_uplink_and_direct_downlink() {
        tokio::time::timeout(
            std::time::Duration::from_secs(10),
            check_tls_transition(VISION_END, VISION_DIRECT),
        )
        .await
        .unwrap();
    }
}
