use crate::dialer::{
    BoxFuture, BoxedStream, OutboundDialer, OutboundUdpDialer, TargetAddr, UdpPacketSocket,
};
use crate::transport::tls::{client_config, TlsConfig};
use bytes::Bytes;
use std::{io, net::SocketAddr, sync::Arc, time::Duration};
use tokio::sync::{mpsc, Mutex, OnceCell};
use wind_core::{
    hooks::Protocol,
    rule::NetworkType,
    types::TargetAddr as WindAddr,
    udp::{UdpPacket, UdpStream},
    AppContext, FlowContext, Outbound,
};
use wind_tuic::quinn::{
    outbound::{ReconnectConfig, TuicOutbound, TuicOutboundOpts},
    CongestionControl, UdpRelayMode,
};

#[derive(Clone)]
pub struct Config {
    pub server: TargetAddr,
    pub uuid: uuid::Uuid,
    pub password: String,
    pub tls: TlsConfig,
    pub relay: UdpRelayMode,
    pub congestion: CongestionControl,
    pub disable_sni: bool,
    pub reduce_rtt: bool,
    pub heartbeat: Duration,
    pub timeout: Duration,
}
struct Session {
    client: Arc<TuicOutbound>,
    ctx: Arc<AppContext>,
}
impl Drop for Session {
    fn drop(&mut self) {
        self.ctx.token.cancel();
    }
}
pub struct Dialer {
    cfg: Config,
    session: OnceCell<Session>,
}
fn to_wind(addr: &TargetAddr) -> WindAddr {
    match addr {
        TargetAddr::Ip(a) => (*a).into(),
        TargetAddr::Domain(h, p) => WindAddr::Domain(h.clone(), *p),
    }
}
async fn resolve(server: &TargetAddr) -> io::Result<SocketAddr> {
    match server {
        TargetAddr::Ip(addr) => Ok(*addr),
        TargetAddr::Domain(host, port) => tokio::net::lookup_host((host.as_str(), *port))
            .await?
            .next()
            .ok_or_else(|| io::Error::other("TUIC server has no address")),
    }
}
impl Dialer {
    pub fn new(cfg: Config) -> Self {
        Self {
            cfg,
            session: OnceCell::new(),
        }
    }
    async fn session(&self) -> io::Result<&Session> {
        self.session
            .get_or_try_init(|| async {
                let cfg = &self.cfg;
                let mut tls = client_config(&cfg.tls);
                tls.enable_sni = !cfg.disable_sni;
                tls.enable_early_data = cfg.reduce_rtt;
                let server = cfg.server.clone();
                let opts = TuicOutboundOpts {
                    peer_addr: resolve(&server).await?,
                    peer_resolver: Some(Arc::new(move || {
                        let server = server.clone();
                        Box::pin(async move { resolve(&server).await.map_err(|e| e.to_string()) })
                    })),
                    sni: cfg.tls.sni.clone().unwrap_or_else(|| cfg.server.host()),
                    auth: (cfg.uuid, Arc::from(cfg.password.as_bytes())),
                    zero_rtt_handshake: cfg.reduce_rtt,
                    heartbeat: cfg.heartbeat,
                    gc_interval: Duration::from_secs(3),
                    gc_lifetime: Duration::from_secs(15),
                    skip_cert_verify: cfg.tls.insecure,
                    alpn: cfg
                        .tls
                        .alpn
                        .iter()
                        .map(|p| String::from_utf8(p.clone()))
                        .collect::<Result<_, _>>()
                        .map_err(io::Error::other)?,
                    reconnect: ReconnectConfig::default(),
                    client_config: Some(Arc::new(tls)),
                    congestion_control: cfg.congestion,
                    max_concurrent_bi_streams: None,
                    max_concurrent_uni_streams: None,
                    send_window: None,
                    stream_receive_window: None,
                    max_idle_time: None,
                    udp_relay_mode: cfg.relay,
                    socket_factory: Some(Arc::new(|peer| {
                        let bind: SocketAddr = if peer.is_ipv4() {
                            ([0; 4], 0).into()
                        } else {
                            ([0; 16], 0).into()
                        };
                        let sock = std::net::UdpSocket::bind(bind)?;
                        #[cfg(unix)]
                        crate::dialer::protect_socket_fd(std::os::fd::AsRawFd::as_raw_fd(&sock))?;
                        Ok(sock)
                    })),
                };
                let ctx = Arc::new(AppContext::default());
                let client = TuicOutbound::new(ctx.clone(), opts)
                    .await
                    .map_err(io::Error::other)?;
                let session = Session {
                    client: Arc::new(client),
                    ctx,
                };
                session
                    .client
                    .start_poll()
                    .await
                    .map_err(io::Error::other)?;
                Ok(session)
            })
            .await
    }
}
impl OutboundDialer for Dialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            tokio::time::timeout(self.cfg.timeout, async {
                let session = self.session().await?;
                let stream = session
                    .client
                    .connect_tcp(&to_wind(target))
                    .await
                    .map_err(io::Error::other)?;
                Ok(Box::new(stream) as BoxedStream)
            })
            .await?
        })
    }
}
struct Association {
    tx: mpsc::Sender<UdpPacket>,
    rx: Mutex<mpsc::Receiver<UdpPacket>>,
}
impl OutboundUdpDialer for Dialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async {
            let session = tokio::time::timeout(self.cfg.timeout, self.session()).await??;
            let client = session.client.clone();
            let (tx, outgoing) = mpsc::channel(32);
            let (incoming, rx) = mpsc::channel(32);
            let flow = FlowContext {
                target: SocketAddr::from(([0; 4], 0)).into(),
                network: NetworkType::Udp,
                source: None,
                inbound_tag: "SmartVPN".into(),
                protocol: Protocol::Tuic,
                user: None,
                inbound_port: None,
                inbound_type: None,
            };
            session.ctx.tasks.spawn(async move {
                if let Err(e) = client
                    .handle_udp(
                        flow,
                        UdpStream {
                            tx: incoming,
                            rx: outgoing,
                        },
                    )
                    .await
                {
                    eprintln!("TUIC UDP association: {e}");
                }
            });
            Ok(Box::new(Association {
                tx,
                rx: Mutex::new(rx),
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}
impl UdpPacketSocket for Association {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            self.tx
                .send(UdpPacket {
                    source: None,
                    target: to_wind(target),
                    payload: Bytes::copy_from_slice(payload),
                })
                .await
                .map_err(|_| io::Error::other("TUIC UDP association closed"))?;
            Ok(payload.len())
        })
    }
    fn recv_from<'a>(
        &'a self,
        buf: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            let packet = self
                .rx
                .lock()
                .await
                .recv()
                .await
                .ok_or_else(|| io::Error::other("TUIC UDP association closed"))?;
            let peer = match packet.target {
                WindAddr::Domain(h, p) => TargetAddr::Domain(h, p),
                WindAddr::IPv4(ip, p) => TargetAddr::Ip((ip, p).into()),
                WindAddr::IPv6(ip, p) => TargetAddr::Ip((ip, p).into()),
            };
            let n = packet.payload.len();
            if n > buf.len() {
                return Err(io::Error::other("TUIC receive buffer too small"));
            }
            buf[..n].copy_from_slice(&packet.payload);
            Ok((n, peer))
        })
    }
}
