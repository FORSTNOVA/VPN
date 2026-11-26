#[allow(dead_code)] // Shared wire servers also cover the other protocol suites.
mod support;
use smartvpn_engine::{
    dialer::TargetAddr,
    dns::FakeIpPool,
    protocol::{anytls::AnyTlsConfig, trojan::TrojanConfig},
    router::{build_dialer, NodeSpec, ProcessResolver, Router},
    rule::RuleSet,
    transport::tls::TlsConfig,
    tun::{IpDevice, MemoryTun, TunDevice, TunStack},
};
use smoltcp::{
    iface::{Config, Interface, SocketHandle, SocketSet},
    socket::{tcp, udp},
    time::Instant as SmolInstant,
    wire::{HardwareAddress, IpCidr, Ipv4Packet, TcpPacket},
};
use std::{
    collections::HashMap,
    net::{Ipv4Addr, SocketAddr},
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::time::timeout;

struct Client {
    dev: IpDevice,
    tun: MemoryTun,
    iface: Interface,
    sockets: SocketSet<'static>,
    start: Instant,
    reset: bool,
}
impl Client {
    fn new(router: Router) -> Self {
        let (server, tun) = MemoryTun::pair(64);
        tokio::spawn(TunStack::new(Arc::new(router), None).run(server));
        let mut dev = IpDevice::default();
        let mut iface = Interface::new(
            Config::new(HardwareAddress::Ip),
            &mut dev,
            SmolInstant::ZERO,
        );
        iface.update_ip_addrs(|a| {
            a.push("10.0.0.2/0".parse::<IpCidr>().unwrap()).unwrap();
            a.push("fd00::2/0".parse().unwrap()).unwrap()
        });
        Self {
            dev,
            tun,
            iface,
            sockets: SocketSet::new(vec![]),
            start: Instant::now(),
            reset: false,
        }
    }
    async fn step(&mut self) {
        self.iface.poll(
            SmolInstant::from_millis(self.start.elapsed().as_millis() as i64),
            &mut self.dev,
            &mut self.sockets,
        );
        while let Some(p) = self.dev.transmitted.pop_front() {
            self.tun.send_packet(&p).await.unwrap();
        }
        if let Ok(p) = timeout(Duration::from_millis(2), self.tun.recv_packet()).await {
            let p = p.unwrap();
            if let Ok(ip) = Ipv4Packet::new_checked(&p[..]) {
                if ip.version() == 4 && ip.next_header() == smoltcp::wire::IpProtocol::Tcp {
                    self.reset |= TcpPacket::new_checked(ip.payload()).unwrap().rst();
                }
            }
            self.dev.received = Some(p);
        }
    }
    async fn datagram(&mut self, dst: SocketAddr, bytes: &[u8]) -> Vec<u8> {
        let buf = || udp::PacketBuffer::new(vec![udp::PacketMetadata::EMPTY; 2], vec![0; 4096]);
        let mut s = udp::Socket::new(buf(), buf());
        s.bind(42000).unwrap();
        s.send_slice(bytes, dst).unwrap();
        let h = self.sockets.add(s);
        loop {
            self.step().await;
            if let Ok((p, m)) = self.sockets.get_mut::<udp::Socket>(h).recv() {
                assert_eq!(m.endpoint, smoltcp::wire::IpEndpoint::from(dst));
                let p = p.to_vec();
                self.sockets.remove(h);
                return p;
            }
        }
    }
    async fn connect(&mut self, dst: SocketAddr, src: u16) -> SocketHandle {
        let buf = || tcp::SocketBuffer::new(vec![0; 65536]);
        let mut s = tcp::Socket::new(buf(), buf());
        s.connect(self.iface.context(), dst, src).unwrap();
        let h = self.sockets.add(s);
        while self.sockets.get::<tcp::Socket>(h).state() != tcp::State::Established {
            self.step().await
        }
        h
    }
    async fn echo(&mut self, h: SocketHandle, data: &[u8]) {
        let mut sent = 0;
        let mut reply = Vec::new();
        while reply.len() < data.len() {
            let s = self.sockets.get_mut::<tcp::Socket>(h);
            if s.can_send() && sent < data.len() {
                sent += s.send_slice(&data[sent..]).unwrap()
            }
            if s.can_recv() {
                s.recv(|buf| {
                    reply.extend_from_slice(buf);
                    (buf.len(), ())
                })
                .unwrap();
            }
            self.step().await;
        }
        assert_eq!(reply, data);
    }
}
fn tls() -> TlsConfig {
    TlsConfig {
        sni: Some("localhost".into()),
        alpn: vec![],
        insecure: true,
    }
}
#[tokio::test]
async fn tun_dns_hijack_fake_ip_and_tcp_proxy() {
    timeout(Duration::from_secs(10), async {
        let echo = support::spawn_echo_target().await;
        let proxy = support::spawn_trojan_server_with_target("pass", move |dst| {
            assert_eq!(dst, TargetAddr::domain("echo.test", 80));
            TargetAddr::Ip(echo)
        })
        .await;
        let spec = NodeSpec::Trojan(TrojanConfig {
            server: TargetAddr::Ip(proxy),
            password: "pass".into(),
            tls: tls(),
        });
        let mut router = Router::new(
            RuleSet::from_lines(&[
                "IP-CIDR,198.18.0.0/15,REJECT",
                "DOMAIN,echo.test,Trojan",
                "MATCH,REJECT",
            ])
            .unwrap(),
            HashMap::from([("Trojan".into(), build_dialer(&spec).unwrap())]),
        );
        router.fake_ip_pool = Some(Arc::new(FakeIpPool::default()));
        let mut c = Client::new(router);
        let query =
            b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x04echo\x04test\x00\x00\x01\x00\x01";
        let reply = c.datagram("198.18.0.2:53".parse().unwrap(), query).await;
        assert_eq!(&reply[..2], &query[..2]);
        assert_eq!(&reply[6..8], &[0, 1]);
        let ip = Ipv4Addr::from(<[u8; 4]>::try_from(&reply[reply.len() - 4..]).unwrap());
        assert_eq!(ip.octets()[..2], [198, 18]);
        let h = c.connect(SocketAddr::new(ip.into(), 80), 41000).await;
        let data = (0..98304).map(|n| (n % 251) as u8).collect::<Vec<_>>();
        c.echo(h, &data).await;
    })
    .await
    .unwrap();
}
struct Owner;

#[tokio::test]
async fn tun_flushes_first_binary_request_before_waiting_for_more_data() {
    use smartvpn_engine::dialer::{BoxFuture, BoxedStream, OutboundDialer};
    use tokio::io::{AsyncWriteExt, BufStream};
    struct BufferedEcho;
    impl OutboundDialer for BufferedEcho {
        fn dial_tcp<'a>(
            &'a self,
            _: &'a TargetAddr,
        ) -> BoxFuture<'a, std::io::Result<BoxedStream>> {
            Box::pin(async {
                let (stream, peer) = tokio::io::duplex(4096);
                tokio::spawn(async move {
                    let (mut r, mut w) = tokio::io::split(peer);
                    tokio::io::copy(&mut r, &mut w).await.unwrap();
                    w.shutdown().await.unwrap();
                });
                Ok(Box::new(BufStream::new(stream)) as BoxedStream)
            })
        }
    }
    timeout(Duration::from_secs(5), async {
        let outbound: Arc<dyn OutboundDialer> = Arc::new(BufferedEcho);
        let mut client = Client::new(Router::new(
            RuleSet::from_lines(&["MATCH,buffered"]).unwrap(),
            HashMap::from([("buffered".into(), outbound)]),
        ));
        let handle = client
            .connect("192.0.2.12:443".parse().unwrap(), 41000)
            .await;
        client.echo(handle, &[0xea; 105]).await;
    })
    .await
    .unwrap();
}
impl ProcessResolver for Owner {
    fn resolve(&self, src: SocketAddr, _: SocketAddr, udp: bool) -> Option<String> {
        assert!(!udp);
        assert_eq!(src.ip().to_string(), "10.0.0.2");
        Some(
            match src.port() {
                41000 => "allowed.app",
                41001 => "blocked.app",
                _ => panic!("unexpected source port"),
            }
            .into(),
        )
    }
}
#[tokio::test]
async fn tun_process_rule_routes_to_app_group_and_rejects() {
    timeout(Duration::from_secs(10), async {
        let echo = support::spawn_echo_target().await;
        let proxy = support::spawn_anytls_server("pass").await;
        let spec = NodeSpec::AnyTls(AnyTlsConfig {
            server: TargetAddr::Ip(proxy),
            password: "pass".into(),
            tls: tls(),
        });
        let mut router = Router::new(
            RuleSet::from_lines(&[
                "PROCESS-NAME,allowed.app,SmartVPN-Group-1",
                "PROCESS-NAME,blocked.app,REJECT",
            ])
            .unwrap(),
            HashMap::from([("SmartVPN-Group-1".into(), build_dialer(&spec).unwrap())]),
        );
        router.process_resolver = Some(Arc::new(Owner));
        router.fake_ip_pool = Some(Arc::new(FakeIpPool::default()));
        let dst = SocketAddr::new(
            router
                .fake_ip_pool
                .as_ref()
                .unwrap()
                .allocate("localhost")
                .into(),
            echo.port(),
        );
        let mut c = Client::new(router);
        let h = c.connect(dst, 41000).await;
        c.echo(h, b"AnyTLS process route").await;
        let rejected = c.connect(dst, 41001).await;
        while c.sockets.get::<tcp::Socket>(rejected).state() != tcp::State::Closed {
            c.step().await;
        }
        assert!(c.reset);
    })
    .await
    .unwrap();
}
#[tokio::test]
async fn tun_ipv6_udp_keeps_datagram_session() {
    timeout(Duration::from_secs(5), async {
        let server = tokio::net::UdpSocket::bind("[::1]:0").await.unwrap();
        let addr = server.local_addr().unwrap();
        let task = tokio::spawn(async move {
            let mut buf = [0; 32];
            let mut peer = None;
            for _ in 0..2 {
                let (n, src) = server.recv_from(&mut buf).await.unwrap();
                if let Some(prev) = peer {
                    assert_eq!(prev, src)
                }
                peer = Some(src);
                server.send_to(&buf[..n], src).await.unwrap();
            }
        });
        let mut c = Client::new(Router::new(
            RuleSet::from_lines(&["MATCH,DIRECT"]).unwrap(),
            HashMap::new(),
        ));
        for data in [b"one", b"two"] {
            assert_eq!(c.datagram(addr, data).await, data);
        }
        task.await.unwrap();
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn tun_sniffs_segmented_http_host() {
    timeout(Duration::from_secs(5), async {
        let echo = support::spawn_echo_target().await;
        let proxy = support::spawn_trojan_server("pass").await;
        let spec = NodeSpec::Trojan(TrojanConfig {
            server: TargetAddr::Ip(proxy),
            password: "pass".into(),
            tls: tls(),
        });
        let router = Router::new(
            RuleSet::from_lines(&["DOMAIN,localhost,Trojan", "MATCH,REJECT"]).unwrap(),
            HashMap::from([("Trojan".into(), build_dialer(&spec).unwrap())]),
        );
        let mut c = Client::new(router);
        let dst = SocketAddr::new("203.0.113.1".parse().unwrap(), echo.port());
        let h = c.connect(dst, 41000).await;
        let header = b"GET / HTTP/1.1\r\nHost: localhost\r\n\r\n";
        for part in [&header[..16], &header[16..]] {
            assert_eq!(
                c.sockets
                    .get_mut::<tcp::Socket>(h)
                    .send_slice(part)
                    .unwrap(),
                part.len()
            );
            c.step().await;
        }
        let mut reply = Vec::new();
        while reply.len() < header.len() {
            c.step().await;
            let s = c.sockets.get_mut::<tcp::Socket>(h);
            if s.can_recv() {
                s.recv(|b| {
                    reply.extend_from_slice(b);
                    (b.len(), ())
                })
                .unwrap();
            }
        }
        assert_eq!(reply, header);
    })
    .await
    .unwrap();
}
