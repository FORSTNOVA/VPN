//! Shadowsocks AEAD outbound dialer (`aes-128-gcm`, `aes-256-gcm`, `chacha20-poly1305`).

use std::{
    io,
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
};

use aes_gcm::{
    aead::{AeadInPlace, KeyInit},
    Aes128Gcm, Aes256Gcm, Nonce,
};
use chacha20poly1305::ChaCha20Poly1305;
use hkdf::Hkdf;
use md5::{Digest as _, Md5};
use rand::RngCore;
use sha1::Sha1;
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt, ReadBuf};

use crate::dialer::{
    protect_socket_fd, BoxFuture, BoxedStream, DirectDialer, OutboundDialer, OutboundUdpDialer,
    TargetAddr, UdpPacketSocket,
};

const MAX_CHUNK_SIZE: usize = 0x3FFF;
const TAG_LEN: usize = 16;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ShadowsocksCipher {
    Aes128Gcm,
    Aes256Gcm,
    ChaCha20Poly1305,
}

impl ShadowsocksCipher {
    pub fn from_str(name: &str) -> io::Result<Self> {
        match name.trim().to_ascii_lowercase().as_str() {
            "aes-128-gcm" | "aead_aes_128_gcm" => Ok(Self::Aes128Gcm),
            "aes-256-gcm" | "aead_aes_256_gcm" => Ok(Self::Aes256Gcm),
            "chacha20-ietf-poly1305" | "chacha20-poly1305" | "aead_chacha20_poly1305" => {
                Ok(Self::ChaCha20Poly1305)
            }
            other => Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("unsupported Shadowsocks cipher: {other}"),
            )),
        }
    }

    pub fn key_len(self) -> usize {
        match self {
            Self::Aes128Gcm => 16,
            Self::Aes256Gcm | Self::ChaCha20Poly1305 => 32,
        }
    }
}

#[derive(Clone, Debug)]
pub struct ShadowsocksConfig {
    pub server: TargetAddr,
    pub cipher: ShadowsocksCipher,
    pub password: String,
}

pub struct ShadowsocksDialer {
    cfg: ShadowsocksConfig,
    master_key: Vec<u8>,
    underlay: Arc<dyn OutboundDialer>,
}

impl ShadowsocksDialer {
    pub fn new(cfg: ShadowsocksConfig) -> Self {
        Self::with_underlay(cfg, Arc::new(DirectDialer))
    }

    pub fn with_underlay(cfg: ShadowsocksConfig, underlay: Arc<dyn OutboundDialer>) -> Self {
        let master_key = evp_bytes_to_key(cfg.password.as_bytes(), cfg.cipher.key_len());
        Self {
            cfg,
            master_key,
            underlay,
        }
    }
}

impl OutboundDialer for ShadowsocksDialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }

    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let mut raw = self.underlay.dial_tcp(&self.cfg.server).await?;

            let key_len = self.cfg.cipher.key_len();
            let mut salt = vec![0u8; key_len];
            rand::thread_rng().fill_bytes(&mut salt);

            let write_subkey = derive_subkey(&self.master_key, &salt, key_len)?;
            let mut write_cipher = AeadCipher::new(self.cfg.cipher, &write_subkey);

            let mut addr_buf = Vec::with_capacity(64);
            target.write_socks_addr(&mut addr_buf)?;

            let mut init_packet = Vec::with_capacity(salt.len() + 2 + TAG_LEN + addr_buf.len() + TAG_LEN);
            init_packet.extend_from_slice(&salt);
            write_cipher.seal_chunk(&addr_buf, &mut init_packet)?;

            raw.write_all(&init_packet).await?;
            raw.flush().await?;

            Ok(Box::new(ShadowsocksStream::new(
                raw,
                self.cfg.cipher,
                self.master_key.clone(),
                write_cipher,
            )) as BoxedStream)
        })
    }
}

pub fn evp_bytes_to_key(password: &[u8], key_len: usize) -> Vec<u8> {
    let mut key = Vec::with_capacity(key_len);
    let mut prev: Option<[u8; 16]> = None;
    while key.len() < key_len {
        let mut hasher = Md5::new();
        if let Some(p) = prev {
            hasher.update(p);
        }
        hasher.update(password);
        let digest: [u8; 16] = hasher.finalize().into();
        let take = (key_len - key.len()).min(16);
        key.extend_from_slice(&digest[..take]);
        prev = Some(digest);
    }
    key
}

pub fn derive_subkey(master_key: &[u8], salt: &[u8], key_len: usize) -> io::Result<Vec<u8>> {
    let hk = Hkdf::<Sha1>::new(Some(salt), master_key);
    let mut out = vec![0u8; key_len];
    hk.expand(b"ss-subkey", &mut out)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "hkdf expand failed"))?;
    Ok(out)
}

pub struct AeadCipher {
    kind: ShadowsocksCipher,
    key: Vec<u8>,
    nonce: [u8; 12],
}

impl AeadCipher {
    pub fn new(kind: ShadowsocksCipher, subkey: &[u8]) -> Self {
        Self {
            kind,
            key: subkey.to_vec(),
            nonce: [0u8; 12],
        }
    }

    fn bump_nonce(&mut self) {
        for b in &mut self.nonce {
            *b = b.wrapping_add(1);
            if *b != 0 {
                break;
            }
        }
    }

    fn encrypt_in_place(&mut self, buf: &mut Vec<u8>) -> io::Result<()> {
        let nonce = Nonce::from_slice(&self.nonce);
        let res = match self.kind {
            ShadowsocksCipher::Aes128Gcm => {
                let cipher = Aes128Gcm::new_from_slice(&self.key)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid key"))?;
                cipher.encrypt_in_place(nonce, b"", buf)
            }
            ShadowsocksCipher::Aes256Gcm => {
                let cipher = Aes256Gcm::new_from_slice(&self.key)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid key"))?;
                cipher.encrypt_in_place(nonce, b"", buf)
            }
            ShadowsocksCipher::ChaCha20Poly1305 => {
                let cipher = ChaCha20Poly1305::new_from_slice(&self.key)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid key"))?;
                cipher.encrypt_in_place(nonce, b"", buf)
            }
        };
        res.map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "AEAD encrypt failed"))?;
        self.bump_nonce();
        Ok(())
    }

    fn decrypt_in_place(&mut self, buf: &mut Vec<u8>) -> io::Result<()> {
        let nonce = Nonce::from_slice(&self.nonce);
        let res = match self.kind {
            ShadowsocksCipher::Aes128Gcm => {
                let cipher = Aes128Gcm::new_from_slice(&self.key)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid key"))?;
                cipher.decrypt_in_place(nonce, b"", buf)
            }
            ShadowsocksCipher::Aes256Gcm => {
                let cipher = Aes256Gcm::new_from_slice(&self.key)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid key"))?;
                cipher.decrypt_in_place(nonce, b"", buf)
            }
            ShadowsocksCipher::ChaCha20Poly1305 => {
                let cipher = ChaCha20Poly1305::new_from_slice(&self.key)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid key"))?;
                cipher.decrypt_in_place(nonce, b"", buf)
            }
        };
        res.map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "AEAD decrypt failed"))?;
        self.bump_nonce();
        Ok(())
    }

    pub fn seal_chunk(&mut self, payload: &[u8], out: &mut Vec<u8>) -> io::Result<()> {
        for chunk in payload.chunks(MAX_CHUNK_SIZE) {
            let mut len_buf = (chunk.len() as u16).to_be_bytes().to_vec();
            self.encrypt_in_place(&mut len_buf)?;
            out.extend_from_slice(&len_buf);

            let mut data_buf = chunk.to_vec();
            self.encrypt_in_place(&mut data_buf)?;
            out.extend_from_slice(&data_buf);
        }
        Ok(())
    }
}

enum ReadState {
    Salt { buf: Vec<u8>, filled: usize },
    Len { buf: [u8; 2 + TAG_LEN], filled: usize },
    Payload { buf: Vec<u8>, filled: usize },
}

pub struct ShadowsocksStream {
    inner: BoxedStream,
    kind: ShadowsocksCipher,
    master_key: Vec<u8>,
    write_cipher: AeadCipher,
    read_cipher: Option<AeadCipher>,
    read_state: ReadState,
    plain_buf: Vec<u8>,
    plain_pos: usize,
    write_buf: Vec<u8>,
    write_pos: usize,
    pending_plain_len: usize,
}

impl ShadowsocksStream {
    fn new(
        inner: BoxedStream,
        kind: ShadowsocksCipher,
        master_key: Vec<u8>,
        write_cipher: AeadCipher,
    ) -> Self {
        let salt_len = kind.key_len();
        Self {
            inner,
            kind,
            master_key,
            write_cipher,
            read_cipher: None,
            read_state: ReadState::Salt {
                buf: vec![0u8; salt_len],
                filled: 0,
            },
            plain_buf: Vec::new(),
            plain_pos: 0,
            write_buf: Vec::new(),
            write_pos: 0,
            pending_plain_len: 0,
        }
    }

    fn flush_write_buf(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        while self.write_pos < self.write_buf.len() {
            match Pin::new(&mut self.inner).poll_write(cx, &self.write_buf[self.write_pos..]) {
                Poll::Ready(Ok(0)) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "underlying stream closed",
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

impl AsyncRead for ShadowsocksStream {
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
                ReadState::Salt { buf, filled } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return if *filled == 0 {
                                        Poll::Ready(Ok(()))
                                    } else {
                                        Poll::Ready(Err(io::Error::new(
                                            io::ErrorKind::UnexpectedEof,
                                            "truncated Shadowsocks salt",
                                        )))
                                    };
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    let subkey = derive_subkey(&this.master_key, buf, this.kind.key_len())?;
                    this.read_cipher = Some(AeadCipher::new(this.kind, &subkey));
                    this.read_state = ReadState::Len {
                        buf: [0u8; 2 + TAG_LEN],
                        filled: 0,
                    };
                }
                ReadState::Len { buf, filled } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return if *filled == 0 {
                                        Poll::Ready(Ok(()))
                                    } else {
                                        Poll::Ready(Err(io::Error::new(
                                            io::ErrorKind::UnexpectedEof,
                                            "truncated Shadowsocks chunk length",
                                        )))
                                    };
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    let mut len_vec = buf.to_vec();
                    this.read_cipher
                        .as_mut()
                        .expect("read_cipher initialized")
                        .decrypt_in_place(&mut len_vec)?;
                    let payload_len = u16::from_be_bytes([len_vec[0], len_vec[1]]) as usize & 0x3FFF;
                    this.read_state = ReadState::Payload {
                        buf: vec![0u8; payload_len + TAG_LEN],
                        filled: 0,
                    };
                }
                ReadState::Payload { buf, filled } => {
                    while *filled < buf.len() {
                        let mut rb = ReadBuf::new(&mut buf[*filled..]);
                        match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                            Poll::Ready(Ok(())) => {
                                let n = rb.filled().len();
                                if n == 0 {
                                    return Poll::Ready(Err(io::Error::new(
                                        io::ErrorKind::UnexpectedEof,
                                        "truncated Shadowsocks chunk payload",
                                    )));
                                }
                                *filled += n;
                            }
                            Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                            Poll::Pending => return Poll::Pending,
                        }
                    }
                    let mut plain = std::mem::take(buf);
                    this.read_cipher
                        .as_mut()
                        .expect("read_cipher initialized")
                        .decrypt_in_place(&mut plain)?;
                    this.plain_buf = plain;
                    this.plain_pos = 0;
                    this.read_state = ReadState::Len {
                        buf: [0u8; 2 + TAG_LEN],
                        filled: 0,
                    };
                }
            }
        }
    }
}

impl AsyncWrite for ShadowsocksStream {
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
        let take = buf.len().min(MAX_CHUNK_SIZE);
        let this = self.as_mut().get_mut();
        this.write_buf.clear();
        this.write_pos = 0;
        this.write_cipher.seal_chunk(&buf[..take], &mut this.write_buf)?;
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

enum SsUdpCarrier {
    Direct(tokio::net::UdpSocket),
    Relay(Box<dyn UdpPacketSocket>),
}

struct ShadowsocksUdpSocket {
    carrier: SsUdpCarrier,
    server: TargetAddr,
    cipher: ShadowsocksCipher,
    master_key: Vec<u8>,
}

impl OutboundUdpDialer for ShadowsocksDialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async move {
            let carrier = if let Some(u) = self.underlay.udp() {
                SsUdpCarrier::Relay(u.dial_udp().await?)
            } else {
                let server_ip = match &self.cfg.server {
                    TargetAddr::Ip(addr) => *addr,
                    TargetAddr::Domain(host, port) => {
                        tokio::net::lookup_host((host.as_str(), *port))
                            .await?
                            .next()
                            .ok_or_else(|| {
                                io::Error::new(
                                    io::ErrorKind::NotFound,
                                    "Shadowsocks server has no addresses",
                                )
                            })?
                    }
                };
                let bind_addr: std::net::SocketAddr = if server_ip.is_ipv4() {
                    ([0, 0, 0, 0], 0).into()
                } else {
                    ([0; 16], 0).into()
                };
                let sock = socket2::Socket::new(
                    socket2::Domain::for_address(bind_addr),
                    socket2::Type::DGRAM,
                    Some(socket2::Protocol::UDP),
                )?;
                #[cfg(unix)]
                protect_socket_fd(std::os::fd::AsRawFd::as_raw_fd(&sock))?;
                sock.set_nonblocking(true)?;
                sock.bind(&bind_addr.into())?;
                let std_sock: std::net::UdpSocket = sock.into();
                let tokio_sock = tokio::net::UdpSocket::from_std(std_sock)?;
                tokio_sock.connect(server_ip).await?;
                SsUdpCarrier::Direct(tokio_sock)
            };
            Ok(Box::new(ShadowsocksUdpSocket {
                carrier,
                server: self.cfg.server.clone(),
                cipher: self.cfg.cipher,
                master_key: self.master_key.clone(),
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}

impl UdpPacketSocket for ShadowsocksUdpSocket {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            let key_len = self.cipher.key_len();
            let mut salt = vec![0u8; key_len];
            rand::thread_rng().fill_bytes(&mut salt);
            let subkey = derive_subkey(&self.master_key, &salt, key_len)?;
            let mut cipher = AeadCipher::new(self.cipher, &subkey);

            let mut plain = Vec::with_capacity(64 + payload.len() + TAG_LEN);
            target.write_socks_addr(&mut plain)?;
            plain.extend_from_slice(payload);
            cipher.encrypt_in_place(&mut plain)?;

            let mut packet = Vec::with_capacity(salt.len() + plain.len());
            packet.extend_from_slice(&salt);
            packet.extend_from_slice(&plain);

            match &self.carrier {
                SsUdpCarrier::Direct(sock) => {
                    sock.send(&packet).await?;
                }
                SsUdpCarrier::Relay(relay) => {
                    relay.send_to(&packet, &self.server).await?;
                }
            }
            Ok(payload.len())
        })
    }

    fn recv_from<'a>(
        &'a self,
        buf: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            let mut pkt = vec![0u8; 65535];
            let n = match &self.carrier {
                SsUdpCarrier::Direct(sock) => sock.recv(&mut pkt).await?,
                SsUdpCarrier::Relay(relay) => {
                    let (n, _) = relay.recv_from(&mut pkt).await?;
                    n
                }
            };
            let key_len = self.cipher.key_len();
            if n < key_len + TAG_LEN {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "truncated Shadowsocks UDP packet",
                ));
            }
            let (salt, rest) = pkt[..n].split_at(key_len);
            let subkey = derive_subkey(&self.master_key, salt, key_len)?;
            let mut cipher = AeadCipher::new(self.cipher, &subkey);
            let mut plain = rest.to_vec();
            cipher.decrypt_in_place(&mut plain)?;

            let mut cur = io::Cursor::new(plain.as_slice());
            let addr = TargetAddr::read_socks_addr(&mut cur).await?;
            let pos = cur.position() as usize;
            let data = &plain[pos..];
            if data.len() > buf.len() {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "receive buffer too small for Shadowsocks UDP packet",
                ));
            }
            buf[..data.len()].copy_from_slice(data);
            Ok((data.len(), addr))
        })
    }
}
