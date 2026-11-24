mod support;
use smartvpn_engine::{
    dialer::TargetAddr,
    dns::FakeIpPool,
    inbound::MixedServer,
    protocol::{
        anytls::AnyTlsConfig,
        ss::{ShadowsocksCipher, ShadowsocksConfig},
        trojan::TrojanConfig,
    },
    router::{build_dialer, NodeSpec, ProcessResolver, Router},
    rule::RuleSet,
    transport::tls::TlsConfig,
};
use std::{
    collections::HashMap,
    net::{IpAddr, SocketAddr},
    sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    },
    time::Duration,
};
use support::*;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    time::timeout,
};

// Count real TCP connections at the entrance to each wire-protocol fixture.
async fn counted(server: SocketAddr) -> (SocketAddr, Arc<AtomicUsize>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let count = Arc::new(AtomicUsize::new(0));
    let entries = count.clone();
    tokio::spawn(async move {
        loop {
            let (mut client, _) = listener.accept().await.unwrap();
            entries.fetch_add(1, Ordering::SeqCst);
            tokio::spawn(async move {
                let mut peer = TcpStream::connect(server).await.unwrap();
                tokio::io::copy_bidirectional(&mut client, &mut peer).await
            });
        }
    });
    (addr, count)
}
async fn ss() -> (NodeSpec, Arc<AtomicUsize>) {
    let cipher = ShadowsocksCipher::Aes128Gcm;
    let (a, count) = counted(spawn_ss_server(cipher, "ss").await).await;
    (
        NodeSpec::Shadowsocks(ShadowsocksConfig {
            server: TargetAddr::Ip(a),
            password: "ss".into(),
            cipher,
        }),
        count,
    )
}
fn tls() -> TlsConfig {
    TlsConfig {
        sni: Some("localhost".into()),
        alpn: vec![],
        insecure: true,
    }
}
async fn trojan() -> (NodeSpec, Arc<AtomicUsize>) {
    let (a, count) = counted(spawn_trojan_server("trojan").await).await;
    (
        NodeSpec::Trojan(TrojanConfig {
            server: TargetAddr::Ip(a),
            password: "trojan".into(),
            tls: tls(),
        }),
        count,
    )
}
async fn mixed(router: Router) -> SocketAddr {
    let server = MixedServer::bind("127.0.0.1:0".parse().unwrap(), Arc::new(router))
        .await
        .unwrap();
    let addr = server.local_addr().unwrap();
    tokio::spawn(server.serve());
    addr
}
async fn expect(client: &mut TcpStream, data: &[u8]) {
    let mut response = vec![0; data.len()];
    timeout(Duration::from_secs(5), client.read_exact(&mut response))
        .await
        .unwrap()
        .unwrap();
    assert_eq!(response, data);
}
async fn request(client: &mut TcpStream, target: &TargetAddr, status: u8, early: &[u8]) {
    client.write_all(&[5, 1, 0]).await.unwrap();
    expect(client, &[5, 0]).await;
    let mut bytes = vec![5, 1, 0];
    target.write_socks_addr(&mut bytes).unwrap();
    bytes.extend_from_slice(early);
    client.write_all(&bytes).await.unwrap();
    expect(client, &[5, status, 0, 1, 0, 0, 0, 0, 0, 0]).await;
}
async fn round_trip(client: &mut TcpStream) {
    client.write_all(b"through mixed").await.unwrap();
    expect(client, b"through mixed").await;
}

#[tokio::test]
async fn mixed_socks5_routes_by_domain_and_cidr_rules() {
    let echo = spawn_echo_target().await;
    let (ss, c) = ss().await;
    let rules = RuleSet::from_lines(&[
        "DOMAIN,blocked.localhost,REJECT",
        "DOMAIN-SUFFIX,localhost,SS",
        "IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
        "MATCH,REJECT",
    ])
    .unwrap();
    let proxy = mixed(Router::new(
        rules,
        HashMap::from([("SS".into(), build_dialer(&ss).unwrap())]),
    ))
    .await;
    for (target, status, count) in [
        (TargetAddr::domain("LOCALHOST", echo.port()), 0, 1),
        (TargetAddr::Ip(echo), 0, 1),
        (TargetAddr::domain("blocked.localhost", echo.port()), 2, 1),
    ] {
        let mut client = TcpStream::connect(proxy).await.unwrap();
        request(&mut client, &target, status, &[]).await;
        if status == 0 {
            round_trip(&mut client).await;
        }
        assert_eq!(c.load(Ordering::SeqCst), count);
    }
    let mut client = TcpStream::connect(proxy).await.unwrap();
    let early = b"GET / HTTP/1.1\r\nHost: localhost\r\n\r\n";
    request(&mut client, &TargetAddr::Ip(echo), 0, early).await;
    expect(&mut client, early).await;
    assert_eq!(c.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn mixed_http_connect_and_plain_http_through_chain() {
    let echo = spawn_echo_target().await;
    let (ss, first) = ss().await;
    let (trojan, second) = trojan().await;
    let chain = build_dialer(&NodeSpec::Chain(vec![ss, trojan])).unwrap();
    let proxy = mixed(Router::new(
        RuleSet::from_lines(&["MATCH,chain"]).unwrap(),
        HashMap::from([("chain".into(), chain)]),
    ))
    .await;
    let mut tunnel = TcpStream::connect(proxy).await.unwrap();
    tunnel
        .write_all(format!("CONNECT localhost:{} HTTP/1.1\r\n\r\nearly", echo.port()).as_bytes())
        .await
        .unwrap();
    expect(
        &mut tunnel,
        b"HTTP/1.1 200 Connection Established\r\n\r\nearly",
    )
    .await;
    round_trip(&mut tunnel).await;
    for method in ["GET", "POST"] {
        let mut client = TcpStream::connect(proxy).await.unwrap();
        let message=format!("{method} http://{echo}/path?q=1 HTTP/1.1\r\nHost: {echo}\r\nContent-Length: 4\r\n\r\nbody");
        client.write_all(message.as_bytes()).await.unwrap();
        expect(
            &mut client,
            format!("{method} /path?q=1 HTTP/1.1\r\nHost: {echo}\r\nContent-Length: 4\r\n\r\nbody")
                .as_bytes(),
        )
        .await;
    }
    assert_eq!(first.load(Ordering::SeqCst), 3);
    assert_eq!(second.load(Ordering::SeqCst), 3);
}

struct Resolver {
    calls: AtomicUsize,
    dst: SocketAddr,
}
impl ProcessResolver for Resolver {
    fn resolve(&self, src: SocketAddr, dst: SocketAddr, udp: bool) -> Option<String> {
        assert!(src.ip().is_loopback());
        assert_eq!(dst, self.dst);
        assert!(!udp);
        (self.calls.fetch_add(1, Ordering::SeqCst) == 0).then(|| "com.android.chrome".into())
    }
}
#[tokio::test]
async fn fake_ip_and_process_rule_routes_to_app_group() {
    let echo = spawn_echo_target().await;
    let pool = Arc::new(FakeIpPool::new(2));
    let ip = pool.allocate("localhost");
    assert_eq!(ip.octets(), [198, 18, 0, 2]);
    let dst = SocketAddr::new(IpAddr::V4(ip), echo.port());
    let (any, group) = counted(spawn_anytls_server("any").await).await;
    let (trojan, default) = trojan().await;
    let rules = RuleSet::from_lines(&[
        "PROCESS-NAME,com.android.chrome,SmartVPN-Group-1",
        "MATCH,SmartVPN",
    ])
    .unwrap();
    let mut router = Router::new(
        rules,
        HashMap::from([
            (
                "SmartVPN-Group-1".into(),
                build_dialer(&NodeSpec::AnyTls(AnyTlsConfig {
                    server: TargetAddr::Ip(any),
                    password: "any".into(),
                    tls: tls(),
                }))
                .unwrap(),
            ),
            ("SmartVPN".into(), build_dialer(&trojan).unwrap()),
        ]),
    );
    router.fake_ip_pool = Some(pool);
    router.process_resolver = Some(Arc::new(Resolver {
        calls: AtomicUsize::new(0),
        dst,
    }));
    let proxy = mixed(router).await;
    for i in 0..2 {
        let mut client = TcpStream::connect(proxy).await.unwrap();
        request(&mut client, &TargetAddr::Ip(dst), 0, &[]).await;
        round_trip(&mut client).await;
        assert_eq!(group.load(Ordering::SeqCst), 1);
        assert_eq!(default.load(Ordering::SeqCst), i);
    }
}
#[tokio::test]
async fn mixed_tls_sniff_preserves_hello_and_allows_server_first() {
    smartvpn_engine::transport::tls::ensure_crypto_provider();
    let config = rustls::ClientConfig::builder()
        .with_root_certificates(rustls::RootCertStore::empty())
        .with_no_client_auth();
    let mut tls =
        rustls::ClientConnection::new(Arc::new(config), "localhost".try_into().unwrap()).unwrap();
    let mut hello = Vec::new();
    tls.write_tls(&mut hello).unwrap();

    let echo = spawn_echo_target().await;
    let proxy = mixed(Router::new(
        RuleSet::from_lines(&["DOMAIN,localhost,DIRECT", "MATCH,REJECT"]).unwrap(),
        HashMap::new(),
    ))
    .await;
    let mut client = TcpStream::connect(proxy).await.unwrap();
    // The documentation IP cannot reach the echo server. Only SNI substitution can.
    let ip = SocketAddr::new("192.0.2.1".parse().unwrap(), echo.port());
    request(&mut client, &TargetAddr::Ip(ip), 0, &hello).await;
    expect(&mut client, &hello).await;

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        stream.write_all(b"banner").await.unwrap();
    });
    let proxy = mixed(Router::new(
        RuleSet::from_lines(&["MATCH,DIRECT"]).unwrap(),
        HashMap::new(),
    ))
    .await;
    let mut client = TcpStream::connect(proxy).await.unwrap();
    client
        .write_all(format!("CONNECT {addr} HTTP/1.1\r\n\r\n").as_bytes())
        .await
        .unwrap();
    expect(
        &mut client,
        b"HTTP/1.1 200 Connection Established\r\n\r\nbanner",
    )
    .await;
}
