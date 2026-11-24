//! Core async stream and outbound dialer abstractions for the standalone Rust data plane.

use std::{
    fmt,
    future::Future,
    io,
    net::{Ipv4Addr, Ipv6Addr, SocketAddr, SocketAddrV4, SocketAddrV6},
    pin::Pin,
};

use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWrite},
    net::TcpStream,
};

/// Object-safe async bidirectional stream.
pub trait AsyncStream: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send + ?Sized> AsyncStream for T {}

/// Heap-allocated async bidirectional stream passed across transport and protocol layers.
pub type BoxedStream = Box<dyn AsyncStream>;

/// Boxed future helper for object-safe async traits without proc-macro dependencies.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// Target destination address for outbound proxy connections.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TargetAddr {
    Ip(SocketAddr),
    Domain(String, u16),
}

impl TargetAddr {
    pub fn domain(host: impl Into<String>, port: u16) -> Self {
        Self::Domain(host.into(), port)
    }

    pub fn host(&self) -> String {
        match self {
            Self::Ip(addr) => addr.ip().to_string(),
            Self::Domain(host, _) => host.clone(),
        }
    }

    pub fn port(&self) -> u16 {
        match self {
            Self::Ip(addr) => addr.port(),
            Self::Domain(_, port) => *port,
        }
    }

    /// Encodes this address in SOCKS5 ATYP format (used by SOCKS5, Shadowsocks, Trojan, and AnyTLS).
    pub fn write_socks_addr(&self, out: &mut Vec<u8>) -> io::Result<()> {
        match self {
            Self::Ip(SocketAddr::V4(v4)) => {
                out.push(0x01);
                out.extend_from_slice(&v4.ip().octets());
                out.extend_from_slice(&v4.port().to_be_bytes());
            }
            Self::Ip(SocketAddr::V6(v6)) => {
                out.push(0x04);
                out.extend_from_slice(&v6.ip().octets());
                out.extend_from_slice(&v6.port().to_be_bytes());
            }
            Self::Domain(host, port) => {
                let bytes = host.as_bytes();
                let len = u8::try_from(bytes.len()).map_err(|_| {
                    io::Error::new(io::ErrorKind::InvalidInput, "domain exceeds 255 bytes")
                })?;
                if len == 0 {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidInput,
                        "empty domain name",
                    ));
                }
                out.push(0x03);
                out.push(len);
                out.extend_from_slice(bytes);
                out.extend_from_slice(&port.to_be_bytes());
            }
        }
        Ok(())
    }

    /// Reads a SOCKS5 ATYP-encoded address from an async reader.
    pub async fn read_socks_addr<R: AsyncRead + Unpin>(reader: &mut R) -> io::Result<Self> {
        let mut atyp = [0u8; 1];
        reader.read_exact(&mut atyp).await?;
        match atyp[0] {
            0x01 => {
                let mut buf = [0u8; 6];
                reader.read_exact(&mut buf).await?;
                let ip = Ipv4Addr::new(buf[0], buf[1], buf[2], buf[3]);
                let port = u16::from_be_bytes([buf[4], buf[5]]);
                Ok(Self::Ip(SocketAddr::V4(SocketAddrV4::new(ip, port))))
            }
            0x03 => {
                let mut len = [0u8; 1];
                reader.read_exact(&mut len).await?;
                let mut domain = vec![0u8; len[0] as usize];
                reader.read_exact(&mut domain).await?;
                let host = String::from_utf8(domain).map_err(|_| {
                    io::Error::new(io::ErrorKind::InvalidData, "non-UTF-8 domain in address")
                })?;
                let mut port_buf = [0u8; 2];
                reader.read_exact(&mut port_buf).await?;
                Ok(Self::Domain(host, u16::from_be_bytes(port_buf)))
            }
            0x04 => {
                let mut buf = [0u8; 18];
                reader.read_exact(&mut buf).await?;
                let mut octets = [0u8; 16];
                octets.copy_from_slice(&buf[..16]);
                let ip = Ipv6Addr::from(octets);
                let port = u16::from_be_bytes([buf[16], buf[17]]);
                Ok(Self::Ip(SocketAddr::V6(SocketAddrV6::new(ip, port, 0, 0))))
            }
            other => Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("unsupported SOCKS ATYP: {other:#x}"),
            )),
        }
    }
}

impl fmt::Display for TargetAddr {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Ip(addr) => write!(f, "{addr}"),
            Self::Domain(host, port) => write!(f, "{host}:{port}"),
        }
    }
}

/// Outbound TCP dialer implemented by direct sockets and all proxy protocols.
/// Wrapping an `underlay: Arc<dyn OutboundDialer>` inside any protocol dialer
/// enables arbitrary multi-hop chaining without extra local proxy ports.
pub trait OutboundDialer: Send + Sync {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        None
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>>;
}

/// Direct TCP dialer connecting straight to the target address.
#[derive(Clone, Copy, Debug, Default)]
pub struct DirectDialer;

impl OutboundDialer for DirectDialer {
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let stream = match target {
                TargetAddr::Ip(addr) => TcpStream::connect(addr).await?,
                TargetAddr::Domain(host, port) => {
                    TcpStream::connect((host.as_str(), *port)).await?
                }
            };
            let _ = stream.set_nodelay(true);
            Ok(Box::new(stream) as BoxedStream)
        })
    }
}

/// A packet-oriented outbound session. Each receive preserves one datagram boundary.
pub trait UdpPacketSocket: Send + Sync {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>>;
    fn recv_from<'a>(&'a self, buf: &'a mut [u8])
        -> BoxFuture<'a, io::Result<(usize, TargetAddr)>>;
}

pub trait OutboundUdpDialer: Send + Sync {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>>;
}
