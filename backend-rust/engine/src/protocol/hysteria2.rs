//! Hysteria 2 over an authenticated, multiplexed QUIC connection.
mod socket;
mod udp;

use bytes::Bytes;
use quinn::{Connection, Endpoint, Runtime};
use rand::{distributions::Alphanumeric, Rng};
use std::{
    io,
    net::SocketAddr,
    pin::Pin,
    sync::{atomic::Ordering, Arc},
    task::{Context, Poll},
};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWrite, ReadBuf},
    sync::Mutex,
};

use crate::{
    dialer::{
        BoxFuture, BoxedStream, OutboundDialer, OutboundUdpDialer, TargetAddr, UdpPacketSocket,
    },
    transport::tls::{client_config, TlsConfig},
};
use socket::PacketTransport;
use udp::Sessions;

#[derive(Clone)]
pub struct Hysteria2Config {
    pub server: TargetAddr,
    pub password: String,
    pub sni: Option<String>,
    pub insecure: bool,
    pub keepalive: bool,
    pub up_mbps: u64,
    pub down_mbps: u64,
    pub obfs_salamander: Option<String>,
    /// Destination ports in rotation order, with a 30-second hop interval.
    pub hop_ports: Vec<u16>,
}

pub struct Hysteria2Dialer {
    cfg: Hysteria2Config,
    underlay: Option<Arc<dyn OutboundDialer>>,
    active: Mutex<Option<Arc<Session>>>,
}

struct Session {
    conn: Connection,
    sessions: Option<Arc<Sessions>>,
    _auth: h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    _endpoint: Endpoint,
}

impl Drop for Session {
    fn drop(&mut self) {
        self.conn.close(0u32.into(), b"session closed");
    }
}

fn invalid(message: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message)
}

fn rate(mbps: u64) -> io::Result<u64> {
    mbps.checked_mul(125_000).ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            "bandwidth exceeds u64 bytes per second",
        )
    })
}

fn padding() -> String {
    let mut rng = rand::thread_rng();
    let len = rng.gen_range(32..=128);
    rng.sample_iter(Alphanumeric)
        .take(len)
        .map(char::from)
        .collect()
}

fn address(target: &TargetAddr) -> io::Result<String> {
    if let TargetAddr::Domain(host, _) = target {
        if host.is_empty()
            || host.len() > 253
            || host
                .bytes()
                .any(|b| b.is_ascii_control() || b.is_ascii_whitespace() || b == b':')
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid Hysteria domain name",
            ));
        }
    }
    Ok(target.to_string())
}

// All locally emitted lengths and frame types fit a two-byte QUIC integer.
fn put_short_varint(buf: &mut Vec<u8>, value: u16) {
    assert!(value < 16384);
    if value < 64 {
        buf.push(value as u8);
    } else {
        buf.extend_from_slice(&(value | 0x4000).to_be_bytes());
    }
}

async fn read_field<R: AsyncRead + Unpin>(reader: &mut R, limit: usize) -> io::Result<Vec<u8>> {
    let first = reader.read_u8().await?;
    let width = 1 << (first >> 6);
    let mut integer = [0; 8];
    integer[8 - width] = first & 63;
    reader.read_exact(&mut integer[9 - width..]).await?;
    let n = u64::from_be_bytes(integer);
    if n > limit as u64 {
        return Err(invalid(format!(
            "Hysteria field length {n} exceeds {limit}"
        )));
    }
    let mut bytes = vec![0; n as usize];
    reader.read_exact(&mut bytes).await?;
    Ok(bytes)
}

impl Hysteria2Dialer {
    pub fn new(cfg: Hysteria2Config) -> Self {
        Self {
            cfg,
            underlay: None,
            active: Mutex::new(None),
        }
    }

    pub fn with_underlay(cfg: Hysteria2Config, underlay: Arc<dyn OutboundDialer>) -> Self {
        Self {
            cfg,
            underlay: Some(underlay),
            active: Mutex::new(None),
        }
    }

    async fn session(&self) -> io::Result<Arc<Session>> {
        let mut active = self.active.lock().await;
        if let Some(session) = &*active {
            if session.conn.close_reason().is_none() {
                return Ok(session.clone());
            }
        }
        let session = Arc::new(self.connect().await?);
        *active = Some(session.clone());
        Ok(session)
    }

    async fn connect(&self) -> io::Result<Session> {
        let cfg = &self.cfg;
        let up = rate(cfg.up_mbps)?;
        let down = rate(cfg.down_mbps)?;
        if cfg.hop_ports.contains(&0) || cfg.obfs_salamander.as_deref() == Some("") {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "zero hop port or empty Salamander password",
            ));
        }
        let request = http::Request::post("https://hysteria/auth")
            .header("hysteria-auth", &cfg.password)
            .header("hysteria-cc-rx", down)
            .header("hysteria-padding", padding())
            .body(())
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?;
        let host = cfg.server.host();
        let runtime = Arc::new(quinn::TokioRuntime);
        let (peer, raw_udp): (SocketAddr, Arc<dyn quinn::AsyncUdpSocket>) =
            if let Some(underlay) = &self.underlay {
                let udp_dialer = underlay.udp().ok_or_else(|| {
                    io::Error::new(
                        io::ErrorKind::Unsupported,
                        "previous hop does not support UDP relay for Hysteria2",
                    )
                })?;
                let udp_sock = udp_dialer.dial_udp().await?;
                let synthetic_peer: SocketAddr = match &cfg.server {
                    TargetAddr::Ip(addr) => *addr,
                    TargetAddr::Domain(_, port) => ([127, 0, 0, 1], *port).into(),
                };
                let tunnel_sock =
                    socket::TunnelUdpSocket::new(udp_sock, cfg.server.clone(), synthetic_peer);
                (synthetic_peer, tunnel_sock)
            } else {
                let peer = match &cfg.server {
                    TargetAddr::Ip(addr) => *addr,
                    TargetAddr::Domain(host, port) => {
                        let addrs: Vec<SocketAddr> =
                            tokio::net::lookup_host((host.as_str(), *port))
                                .await?
                                .collect();
                        if addrs.is_empty() {
                            return Err(io::Error::new(
                                io::ErrorKind::NotFound,
                                "Hysteria server has no addresses",
                            ));
                        }
                        // Prefer route-reachable addresses (skipping IPv6 when the host has no IPv6 route).
                        addrs
                            .iter()
                            .copied()
                            .find(|addr| {
                                let bind_addr = if addr.is_ipv4() {
                                    "0.0.0.0:0"
                                } else {
                                    "[::]:0"
                                };
                                std::net::UdpSocket::bind(bind_addr)
                                    .and_then(|s| s.connect(addr))
                                    .is_ok()
                            })
                            .unwrap_or(addrs[0])
                    }
                };
                let bind: SocketAddr = if peer.is_ipv4() {
                    ([0; 4], 0).into()
                } else {
                    ([0; 16], 0).into()
                };
                let wrapped = runtime.wrap_udp_socket(socket::bind_protected(bind)?)?;
                (peer, wrapped)
            };
        let socket = Arc::new(PacketTransport::new(
            raw_udp,
            peer,
            if self.underlay.is_some() {
                Vec::new()
            } else {
                cfg.hop_ports.clone()
            },
            cfg.obfs_salamander.clone(),
        ));
        let endpoint =
            Endpoint::new_with_abstract_socket(Default::default(), None, socket.clone(), runtime)?;
        let tls = client_config(&TlsConfig {
            insecure: cfg.insecure,
            sni: cfg.sni.clone(),
            alpn: vec![b"h3".to_vec()],
        });
        let crypto =
            quinn::crypto::rustls::QuicClientConfig::try_from(tls).map_err(io::Error::other)?;
        let mut client = quinn::ClientConfig::new(Arc::new(crypto));
        if cfg.keepalive {
            let mut transport = quinn::TransportConfig::default();
            transport.keep_alive_interval(Some(std::time::Duration::from_secs(5)));
            client.transport_config(Arc::new(transport));
        }
        let conn = endpoint
            .connect_with(
                client,
                peer,
                cfg.sni.as_deref().unwrap_or(&host),
            )
            .map_err(io::Error::other)?
            .await
            .map_err(io::Error::other)?;
        let (mut driver, mut auth) = h3::client::builder()
            .send_grease(false)
            .enable_datagram(true)
            .build(h3_quinn::Connection::new(conn.clone()))
            .await
            .map_err(io::Error::other)?;
        tokio::spawn(async move { driver.wait_idle().await });
        let mut stream = auth.send_request(request).await.map_err(io::Error::other)?;
        stream.finish().await.map_err(io::Error::other)?;
        let response = stream.recv_response().await.map_err(io::Error::other)?;
        if response.status().as_u16() != 233 {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                format!("Hysteria authentication returned {}", response.status()),
            ));
        }
        let header = |name: &str| {
            response
                .headers()
                .get(name)
                .ok_or_else(|| invalid(format!("missing {name}")))?
                .to_str()
                .map_err(invalid)
        };
        let udp = header("hysteria-udp")?.parse::<bool>().map_err(invalid)?;
        let peer_rate = header("hysteria-cc-rx")?;
        let cap = match peer_rate {
            "auto" => up,
            n => match (up, n.parse::<u64>().map_err(invalid)?) {
                (0, peer) => peer,
                (local, 0) => local,
                (local, peer) => local.min(peer),
            },
        };
        socket.rate.store(cap, Ordering::Relaxed);
        let sessions = udp.then(|| Sessions::start(conn.clone()));
        Ok(Session {
            conn,
            sessions,
            _auth: auth,
            _endpoint: endpoint,
        })
    }
}

struct TcpStream {
    io: tokio::io::Join<quinn::RecvStream, quinn::SendStream>,
    _session: Arc<Session>,
}

impl AsyncRead for TcpStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        Pin::new(&mut self.io).poll_read(cx, buf)
    }
}
impl AsyncWrite for TcpStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.io).poll_write(cx, buf)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.io).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.io).poll_shutdown(cx)
    }
}

impl OutboundDialer for Hysteria2Dialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let addr = address(target)?;
            let pad = padding();
            let mut req = Vec::new();
            put_short_varint(&mut req, 0x401);
            for field in [addr, pad] {
                put_short_varint(&mut req, field.len() as u16);
                req.extend_from_slice(field.as_bytes());
            }
            let session = self.session().await?;
            let (mut tx, mut rx) = session.conn.open_bi().await?;
            tx.write_all(&req).await?;
            let status = rx.read_u8().await?;
            let message = read_field(&mut rx, 65535).await?;
            read_field(&mut rx, 65535).await?;
            if status != 0 {
                return Err(io::Error::new(
                    io::ErrorKind::ConnectionRefused,
                    format!(
                        "Hysteria TCP status {status}: {}",
                        String::from_utf8_lossy(&message)
                    ),
                ));
            }
            Ok(Box::new(TcpStream {
                io: tokio::io::join(rx, tx),
                _session: session,
            }) as BoxedStream)
        })
    }
}

impl OutboundUdpDialer for Hysteria2Dialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async move {
            let session = self.session().await?;
            let sessions = session.sessions.as_ref().ok_or_else(|| {
                io::Error::new(io::ErrorKind::Unsupported, "Hysteria server disabled UDP")
            })?;
            Ok(Box::new(sessions.open(session.clone())?) as Box<dyn UdpPacketSocket>)
        })
    }
}
