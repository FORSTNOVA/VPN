use super::stack::Flow;
use crate::{
    dialer::{TargetAddr, UdpPacketSocket},
    dns_server,
    router::Router,
    rule::RuleAction,
};
use smoltcp::{
    phy::ChecksumCapabilities,
    wire::{IpProtocol, IpRepr, UdpPacket, UdpRepr},
};
use std::{io, net::SocketAddr, time::Duration};
use tokio::{net::UdpSocket, sync::mpsc, time::sleep};

enum Session {
    Direct(UdpSocket),
    Proxy(Box<dyn UdpPacketSocket>),
}
impl Session {
    async fn send(&self, data: &[u8], target: &TargetAddr) -> io::Result<()> {
        let n = match self {
            Self::Direct(s) => s.send(data).await?,
            Self::Proxy(s) => s.send_to(data, target).await?,
        };
        if n != data.len() {
            return Err(io::Error::new(
                io::ErrorKind::WriteZero,
                "partial UDP datagram",
            ));
        }
        Ok(())
    }
    async fn recv(&self, buf: &mut [u8], target: &TargetAddr) -> io::Result<usize> {
        match self {
            Self::Direct(s) => s.recv(buf).await,
            Self::Proxy(s) => {
                let (n, peer) = s.recv_from(buf).await?;
                let valid = match (&peer, target) {
                    (TargetAddr::Ip(_), TargetAddr::Domain(_, port)) => peer.port() == *port,
                    _ => peer == *target,
                };
                if !valid {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        format!("UDP peer {peer} for {target}"),
                    ));
                }
                Ok(n)
            }
        }
    }
}
async fn reply(flow: Flow, data: &[u8], tx: &mpsc::Sender<Vec<u8>>) -> io::Result<()> {
    let ip = IpRepr::new(
        flow.dst.ip().into(),
        flow.src.ip().into(),
        IpProtocol::Udp,
        data.len() + 8,
        64,
    );
    let len = ip.header_len() + data.len() + 8;
    if len > 65535 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "UDP reply exceeds TUN packet size",
        ));
    }
    let mut packet = vec![0; len];
    let caps = ChecksumCapabilities::default();
    ip.emit(&mut packet, &caps);
    let udp = UdpRepr {
        src_port: flow.dst.port(),
        dst_port: flow.src.port(),
    };
    udp.emit(
        &mut UdpPacket::new_unchecked(&mut packet[ip.header_len()..]),
        &ip.src_addr(),
        &ip.dst_addr(),
        data.len(),
        |b| b.copy_from_slice(data),
        &caps,
    );
    tx.send(packet).await.map_err(io::Error::other)
}
pub(super) async fn run(
    flow: Flow,
    router: &Router,
    upstream: Option<SocketAddr>,
    mut incoming: mpsc::Receiver<Vec<u8>>,
    tx: mpsc::Sender<Vec<u8>>,
) -> io::Result<()> {
    if flow.dst.port() == 53 {
        let pool = router.fake_ip_pool.as_ref().ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidInput, "TUN DNS requires FakeIpPool")
        })?;
        while let Ok(Some(query)) =
            tokio::time::timeout(Duration::from_secs(30), incoming.recv()).await
        {
            if query.len() < 12 || query[2] & 0x80 != 0 {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "invalid DNS query header",
                ));
            }
            let answer = dns_server::answer(&query, pool, &router.rules, upstream).await;
            reply(flow, &answer, &tx).await?;
        }
        return Ok(());
    }
    let (action, target) = router
        .route(flow.src, &TargetAddr::Ip(flow.dst), None, None, true)
        .await?;
    let session = match action {
        RuleAction::Reject => {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "UDP rejected by rule",
            ))
        }
        RuleAction::Direct => {
            let addr = match &target {
                TargetAddr::Ip(a) => *a,
                TargetAddr::Domain(h, p) => tokio::net::lookup_host((h.as_str(), *p))
                    .await?
                    .next()
                    .ok_or_else(|| {
                        io::Error::new(io::ErrorKind::NotFound, "UDP target has no addresses")
                    })?,
            };
            let socket = UdpSocket::bind(if addr.is_ipv4() {
                "0.0.0.0:0"
            } else {
                "[::]:0"
            })
            .await?;
            #[cfg(unix)]
            crate::dialer::protect_socket_fd(std::os::fd::AsRawFd::as_raw_fd(&socket))?;
            socket.connect(addr).await?;
            Session::Direct(socket)
        }
        RuleAction::Proxy(tag) => {
            let outbound = router.outbounds.get(tag).ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::NotFound,
                    format!("unknown UDP outbound {tag}"),
                )
            })?;
            let dialer = outbound.udp().ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::Unsupported,
                    format!("outbound {tag} does not support UDP"),
                )
            })?;
            Session::Proxy(dialer.dial_udp().await?)
        }
    };
    let meter =
        crate::traffic::Meter::open(router, flow.src, flow.dst, true, target.to_string(), action)?;
    let mut buf = vec![0; 65535];
    loop {
        tokio::select! {
            msg = incoming.recv() => match msg {
                Some(data) => {session.send(&data, &target).await?; meter.up(data.len());},
                None => return Ok(()),
            },
            n = session.recv(&mut buf, &target) => {let n=n?; reply(flow, &buf[..n], &tx).await?; meter.down(n);},
            _ = sleep(Duration::from_secs(60)) => return Ok(()),
        }
    }
}
