use super::{udp, IpDevice, TunDevice};
use crate::{
    dialer::TargetAddr,
    inbound::mixed::sniff,
    router::{RouteResult, Router},
};
use smoltcp::{
    iface::{Config, Interface, SocketSet},
    phy::ChecksumCapabilities,
    socket::tcp,
    time::{Duration as SmolDuration, Instant as SmolInstant},
    wire::*,
};
use std::{
    collections::HashMap,
    io,
    net::SocketAddr,
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt, DuplexStream},
    sync::mpsc,
    task::JoinSet,
    time::sleep,
};

#[derive(Clone, Copy, Debug, Eq, PartialEq, Hash)]
pub(super) struct Flow {
    pub src: SocketAddr,
    pub dst: SocketAddr,
    tcp: bool,
}
struct Incoming {
    flow: Flow,
    opens: bool,
    data: Vec<u8>,
}
fn decode(packet: Vec<u8>) -> smoltcp::wire::Result<Option<Incoming>> {
    if packet.is_empty() {
        return Err(smoltcp::wire::Error);
    }
    let caps = ChecksumCapabilities::default();
    let (src, dst, proto, payload) = match IpVersion::of_packet(&packet)? {
        IpVersion::Ipv4 => {
            let p = Ipv4Packet::new_checked(packet.as_slice())?;
            let repr = Ipv4Repr::parse(&p, &caps)?;
            (
                IpAddress::from(repr.src_addr),
                IpAddress::from(repr.dst_addr),
                repr.next_header,
                p.payload(),
            )
        }
        IpVersion::Ipv6 => {
            let p = Ipv6Packet::new_checked(packet.as_slice())?;
            let repr = Ipv6Repr::parse(&p)?;
            (
                IpAddress::from(repr.src_addr),
                IpAddress::from(repr.dst_addr),
                repr.next_header,
                p.payload(),
            )
        }
    };
    if !src.is_unicast() || !dst.is_unicast() {
        return Err(smoltcp::wire::Error);
    }
    let (sport, dport, opens, data) = match proto {
        IpProtocol::Tcp => {
            let p = TcpPacket::new_checked(payload)?;
            let r = TcpRepr::parse(&p, &src, &dst, &caps)?;
            (
                r.src_port,
                r.dst_port,
                r.control == TcpControl::Syn && r.ack_number.is_none(),
                packet,
            )
        }
        IpProtocol::Udp => {
            let p = UdpPacket::new_checked(payload)?;
            let r = UdpRepr::parse(&p, &src, &dst, &caps)?;
            (r.src_port, r.dst_port, true, p.payload().to_vec())
        }
        _ => return Ok(None),
    };
    Ok(Some(Incoming {
        flow: Flow {
            src: SocketAddr::new(src.into(), sport),
            dst: SocketAddr::new(dst.into(), dport),
            tcp: proto == IpProtocol::Tcp,
        },
        opens,
        data,
    }))
}
pub struct TunStack {
    router: Arc<Router>,
    dns_upstream: Option<SocketAddr>,
}
impl TunStack {
    pub fn new(router: Arc<Router>, dns_upstream: Option<SocketAddr>) -> Self {
        Self {
            router,
            dns_upstream,
        }
    }
    /// Dropping the run future drops the device and cancels all flow workers.
    pub async fn run(self, mut device: impl TunDevice) -> io::Result<()> {
        let (tx, mut rx) = mpsc::channel::<Vec<u8>>(128);
        let mut workers = JoinSet::new();
        let mut flows = HashMap::<Flow, (mpsc::Sender<Vec<u8>>, tokio::task::Id)>::new();
        loop {
            tokio::select! {
                done = workers.join_next_with_id(), if !workers.is_empty() => {
                    let (id, (flow, result)) = done.expect("active flow worker").map_err(io::Error::other)?;
                    if flows.get(&flow).is_some_and(|(_, current)| *current == id) {
                        flows.remove(&flow);
                    }
                    if let Err(err) = result {
                        eprintln!("TUN {flow:?}: {err}");
                    }
                }
                Some(packet) = rx.recv() => device.send_packet(&packet).await?,
                packet = device.recv_packet() => {
                    let msg = match decode(packet?) {
                        Ok(Some(msg)) => msg,
                        Ok(None) => continue, // Not a TCP or UDP packet.
                        Err(err) => {
                            eprintln!("invalid TUN packet: {err}");
                            continue;
                        }
                    };
                    let flow = msg.flow;
                    if let Some((sender, _)) = flows.get(&flow).filter(|(s, _)| !s.is_closed()) {
                        // Congestion on one flow must not stall unrelated flows.
                        if let Err(err) = sender.try_send(msg.data) {
                            eprintln!("TUN {flow:?}: {err}");
                        }
                        continue;
                    }
                    if !msg.opens {
                        continue; // Only a SYN can create TCP state.
                    }
                    if workers.len() >= 1024 {
                        eprintln!("TUN flow limit reached for {flow:?}");
                        continue;
                    }
                    let (sender, input) = mpsc::channel(32);
                    sender.try_send(msg.data).expect("empty flow queue");
                    let output = tx.clone();
                    let router = self.router.clone();
                    let dns = self.dns_upstream;
                    let worker = workers.spawn(async move {
                        let result = if flow.tcp {
                            tcp_flow(flow, &router, input, output).await
                        } else {
                            udp::run(flow, &router, dns, input, output).await
                        };
                        (flow, result)
                    });
                    flows.insert(flow, (sender, worker.id()));
                }
            }
        }
    }
}

async fn relay(flow: Flow, router: &Router, mut stream: DuplexStream) -> io::Result<()> {
    let mut prefix = Vec::new();
    let host = if router
        .fake_ip_pool
        .as_ref()
        .is_some_and(|p| p.is_fake_ip(flow.dst.ip()))
    {
        None
    } else {
        sniff(&mut stream, &mut prefix).await?
    };
    match router
        .dial_tcp(flow.src, &TargetAddr::Ip(flow.dst), host.as_deref())
        .await?
    {
        RouteResult::Reject => Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "TCP rejected by rule",
        )),
        RouteResult::Connected {
            stream: mut remote, ..
        } => {
            remote.write_all(&prefix).await?;
            tokio::io::copy_bidirectional(&mut stream, &mut remote).await?;
            Ok(())
        }
    }
}

async fn tcp_flow(
    flow: Flow,
    router: &Router,
    mut input: mpsc::Receiver<Vec<u8>>,
    output: mpsc::Sender<Vec<u8>>,
) -> io::Result<()> {
    let mut dev = IpDevice::default();
    let start = Instant::now();
    let now = || SmolInstant::from_millis(start.elapsed().as_millis() as i64);
    let mut config = Config::new(HardwareAddress::Ip);
    config.random_seed = rand::random();
    let mut iface = Interface::new(config, &mut dev, now());
    iface.update_ip_addrs(|a| a.push(IpCidr::new(flow.dst.ip().into(), 0)).unwrap());
    let buffer = || tcp::SocketBuffer::new(vec![0; 32768]);
    let mut sock = tcp::Socket::new(buffer(), buffer());
    sock.listen(flow.dst).map_err(io::Error::other)?;
    sock.set_timeout(Some(SmolDuration::from_secs(120)));
    let mut sockets = SocketSet::new(vec![]);
    let handle = sockets.add(sock);
    let (bridge, client) = tokio::io::duplex(32768);
    let (mut reader, mut writer) = tokio::io::split(bridge);
    let relay = relay(flow, router, client);
    tokio::pin!(relay);
    let (mut started, mut finished, mut sent_fin, mut received_fin) = (false, false, false, false);
    let mut downstream = [0; 8192];
    let mut upstream = [0; 8192];
    dev.received = input.recv().await; // The dispatcher enqueues the initial SYN before spawning us.
    loop {
        let time = now();
        iface.poll(time, &mut dev, &mut sockets);
        while let Some(p) = dev.transmitted.pop_front() {
            output.send(p).await.map_err(io::Error::other)?;
        }
        let delay = iface.poll_delay(time, &sockets);
        let sock = sockets.get_mut::<tcp::Socket>(handle);
        if matches!(sock.state(), tcp::State::Closed | tcp::State::Listen) {
            return Ok(());
        }
        started |= matches!(
            sock.state(),
            tcp::State::Established | tcp::State::CloseWait
        );
        let n = if sock.can_recv() {
            sock.peek_slice(&mut upstream).map_err(io::Error::other)?
        } else {
            0
        };
        if started && !sock.may_recv() && n == 0 && !received_fin {
            writer.shutdown().await?;
            received_fin = true;
        }
        let space = downstream
            .len()
            .min(sock.send_capacity() - sock.send_queue());
        tokio::select! {
            biased;
            result = &mut relay, if started && !finished => {
                finished = true;
                if let Err(err) = result {
                    sock.abort();
                    iface.poll(now(), &mut dev, &mut sockets);
                    while let Some(packet) = dev.transmitted.pop_front() {
                        output.send(packet).await.map_err(io::Error::other)?;
                    }
                    return Err(err);
                }
            }
            count = writer.write(&upstream[..n]), if n > 0 => {
                let count = count?;
                if count == 0 {
                    return Err(io::ErrorKind::WriteZero.into());
                }
                sock.recv_slice(&mut upstream[..count]).map_err(io::Error::other)?;
            }
            count = reader.read(&mut downstream[..space]), if sock.can_send() && !sent_fin => {
                let count = count?;
                if count == 0 {
                    sock.close();
                    sent_fin = true;
                } else {
                    sock.send_slice(&downstream[..count]).map_err(io::Error::other)?;
                }
            }
            packet = input.recv() => {
                dev.received = packet;
                if dev.received.is_none() {
                    return Ok(());
                }
            }
            _ = async {
                match delay {
                    Some(delay) => sleep(Duration::from_millis(delay.total_millis())).await,
                    None => std::future::pending().await,
                }
            } => {}

        }
    }
}
