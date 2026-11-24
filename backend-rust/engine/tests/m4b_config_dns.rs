mod support;
use smartvpn_engine::{
    config_bridge::build_router_from_clash_yaml, dns::FakeIpPool, dns_server::DnsServer,
    inbound::MixedServer, protocol::ss::ShadowsocksCipher, rule::RuleSet,
};
use std::{
    net::{Ipv4Addr, SocketAddr},
    sync::Arc,
    time::Duration,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream, UdpSocket},
    time::timeout,
};

const YAML: &str = r#"
mixed-port: 7890
proxies:
 - {name: entry, type: ss, server: 127.0.0.1, port: ENTRY, password: first, cipher: aes-128-gcm}
 - {name: middle, type: trojan, server: 127.0.0.1, port: MIDDLE, password: middle, dialer-proxy: entry, sni: localhost, skip-cert-verify: true}
 - {name: exit, type: anytls, server: 127.0.0.1, port: EXIT, password: exit, dialer-proxy: middle, sni: localhost, skip-cert-verify: true}
 - {name: vision, type: vless, server: localhost, port: 443, uuid: 550e8400-e29b-41d4-a716-446655440000, flow: xtls-rprx-vision, tls: true, servername: test.example, reality-opts: {public-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA, short-id: "0102"}}
 - {name: quic, type: hysteria2, server: localhost, port: 8443, password: hy2, sni: localhost, skip-cert-verify: true, up: 10, down: 30 Mbps, obfs: salamander, obfs-password: secret, ports: "8443,9000-9002"}
proxy-groups:
 - {name: SmartVPN, type: select, proxies: [SmartVPN-Group-1, DIRECT, REJECT]}
 - {name: SmartVPN-Group-1, type: select, proxies: [exit, auto, backup, relay]}
 - {name: auto, type: url-test, proxies: [quic, vision]}
 - {name: backup, type: fallback, proxies: [quic, vision]}
 - {name: relay, type: relay, proxies: [vision, entry]}
rules:
 - DOMAIN,localhost,SmartVPN
 - MATCH,REJECT
"#;
fn query(name: &str, kind: u16) -> Vec<u8> {
    let mut q = vec![0xab, 0x12, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0];
    for label in name.split('.') {
        q.push(label.len() as u8);
        q.extend_from_slice(label.as_bytes());
    }
    q.push(0);
    q.extend_from_slice(&kind.to_be_bytes());
    q.extend_from_slice(&[0, 1]);
    q
}
async fn exchange(addr: SocketAddr, q: &[u8]) -> Vec<u8> {
    let socket = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    socket.connect(addr).await.unwrap();
    socket.send(q).await.unwrap();
    let mut response = vec![0; 512];
    let n = timeout(Duration::from_secs(3), socket.recv(&mut response))
        .await
        .unwrap()
        .unwrap();
    response.truncate(n);
    assert_eq!(&response[..2], &q[..2]);
    response
}
#[tokio::test]
async fn yaml_dns_socks_chain_roundtrip() {
    timeout(Duration::from_secs(10), async {
        let echo = support::spawn_echo_target().await;
        let ss = support::spawn_ss_server(ShadowsocksCipher::Aes128Gcm, "first").await;
        let entry = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let yaml = YAML
            .replace("ENTRY", &entry.local_addr().unwrap().port().to_string())
            .replace(
                "MIDDLE",
                &support::spawn_trojan_server("middle")
                    .await
                    .port()
                    .to_string(),
            )
            .replace(
                "EXIT",
                &support::spawn_anytls_server("exit")
                    .await
                    .port()
                    .to_string(),
            );
        let bundle = build_router_from_clash_yaml(&yaml, None).unwrap();
        assert_eq!(bundle.mixed_port, 7890);
        let (entered, observed) = tokio::sync::oneshot::channel();
        let tapped = tokio::spawn(async move {
            let (mut a, _) = entry.accept().await.unwrap();
            let mut b = TcpStream::connect(ss).await.unwrap();
            entered.send(()).unwrap();
            tokio::io::copy_bidirectional(&mut a, &mut b).await
        });
        let local = "127.0.0.1:0".parse().unwrap();
        let dns = DnsServer::bind(
            local,
            bundle.fake_ip_pool,
            Arc::new(bundle.router.rules.clone()),
            None,
        )
        .await
        .unwrap();
        let addr = dns.local_addr().unwrap();
        tokio::spawn(dns.serve());
        let mixed = MixedServer::bind(local, bundle.router).await.unwrap();
        let mixed_addr = mixed.local_addr().unwrap();
        tokio::spawn(mixed.serve());
        let a = exchange(addr, &query("localhost", 1)).await;
        assert_eq!(&a[2..8], &[0x81, 0x80, 0, 1, 0, 1]);
        let ip = Ipv4Addr::from(<[u8; 4]>::try_from(&a[a.len() - 4..]).unwrap());
        assert_eq!(&ip.octets()[..2], &[198, 18]);
        let q = query("localhost", 28);
        let aaaa = exchange(addr, &q).await;
        assert_eq!(&aaaa[2..8], &[0x81, 0x80, 0, 1, 0, 0]);
        assert_eq!(aaaa.len(), q.len());
        let mut client = TcpStream::connect(mixed_addr).await.unwrap();
        client.write_all(&[5, 1, 0]).await.unwrap();
        let mut method = [0; 2];
        client.read_exact(&mut method).await.unwrap();
        assert_eq!(method, [5, 0]);
        let mut request = vec![5, 1, 0, 1];
        request.extend_from_slice(&ip.octets());
        request.extend_from_slice(&echo.port().to_be_bytes());
        client.write_all(&request).await.unwrap();
        let mut reply = [0; 10];
        client.read_exact(&mut reply).await.unwrap();
        assert_eq!(reply, [5, 0, 0, 1, 0, 0, 0, 0, 0, 0]);
        client.write_all(b"m4-b").await.unwrap();
        let mut data = [0; 4];
        client.read_exact(&mut data).await.unwrap();
        assert_eq!(&data, b"m4-b");
        observed.await.unwrap();
        drop(client);
        tapped.abort();
    })
    .await
    .unwrap()
}
#[test]
fn rejects_cycles_and_unknown_rule_targets() {
    let yaml = YAML
        .replace("ENTRY", "1")
        .replace("MIDDLE", "2")
        .replace("EXIT", "3");
    for (from, to, error) in [
        ("dialer-proxy: entry", "dialer-proxy: exit", "cyclic"),
        ("exit, auto", "exit, SmartVPN", "cyclic"),
        (
            "DOMAIN,localhost,SmartVPN",
            "DOMAIN,localhost,missing",
            "unknown rule outbound",
        ),
    ] {
        assert!(build_router_from_clash_yaml(&yaml.replace(from, to), None)
            .err()
            .unwrap()
            .to_string()
            .contains(error));
    }
}
#[tokio::test]
async fn directs_only_matching_queries_to_upstream() {
    let upstream = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let server = DnsServer::bind(
        "127.0.0.1:0".parse().unwrap(),
        Arc::new(FakeIpPool::default()),
        Arc::new(RuleSet::from_lines(&["DOMAIN,local,DIRECT", "MATCH,proxy"]).unwrap()),
        Some(upstream.local_addr().unwrap()),
    )
    .await
    .unwrap();
    let addr = server.local_addr().unwrap();
    tokio::spawn(server.serve());
    let q = query("local", 28);
    let mut expected = q.clone();
    expected[2] = 0x81;
    expected[3] = 0x83;
    let reply = expected.clone();
    let responder = tokio::spawn(async move {
        let mut buf = [0; 512];
        let (n, peer) = upstream.recv_from(&mut buf).await.unwrap();
        assert_eq!(&buf[..n], query("local", 28));
        upstream.send_to(&reply, peer).await.unwrap();
    });
    assert_eq!(exchange(addr, &query("proxy", 28)).await[3], 0x80);
    assert_eq!(exchange(addr, &q).await, expected);
    responder.await.unwrap();
}

#[tokio::test]
async fn tested_groups_route_through_a_healthy_member_and_cache_the_probe() {
    use smartvpn_engine::dialer::TargetAddr;
    for kind in ["url-test", "fallback"] {
        let probe = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/health", probe.local_addr().unwrap());
        let yaml = format!("mixed-port: 7890\nproxy-groups:\n - {{name: group, type: {kind}, proxies: [REJECT, DIRECT], url: {url}, interval: 60}}\nrules: ['MATCH,group']");
        let bundle = build_router_from_clash_yaml(&yaml, None).unwrap();
        let probe_server = tokio::spawn(async move {
            let (mut stream, _) = probe.accept().await.unwrap();
            use tokio::io::{AsyncBufReadExt, BufReader};
            let mut reader = BufReader::new(&mut stream);
            let mut line = String::new();
            reader.read_line(&mut line).await.unwrap();
            assert_eq!(line, "GET /health HTTP/1.1\r\n");
            loop {
                line.clear();
                assert!(reader.read_line(&mut line).await.unwrap() > 0);
                if line == "\r\n" {
                    break;
                }
            }
            stream
                .write_all(b"HTTP/1.1 204 No Content\r\n\r\n")
                .await
                .unwrap();
            // Closing this listener makes a second probe fail.
        });
        let target = TargetAddr::Ip(support::spawn_echo_target().await);
        for _ in 0..2 {
            let mut stream = bundle.router.outbounds["group"]
                .dial_tcp(&target)
                .await
                .unwrap();
            stream.write_all(b"up").await.unwrap();
            let mut response = [0; 2];
            stream.read_exact(&mut response).await.unwrap();
            assert_eq!(&response, b"up");
        }
        probe_server.await.unwrap();
    }
}

#[test]
fn tolerates_subscription_rows_without_weakening_supported_rules() {
    use smartvpn_engine::rule::{RuleAction, RuleContext};
    let yaml = r#"
mixed-port: 7890
proxies:
 - {name: legacy, type: vmess}
 - {name: incomplete, type: trojan, server: host, port: 443}
 - {name: working, type: trojan, server: host, port: 443, password: test}
proxy-groups:
 - {name: empty, type: select, proxies: [legacy, incomplete]}
 - {name: chain, type: relay, proxies: [working, legacy]}
 - {name: usable, type: select, proxies: [chain, empty, missing, working]}
 - {name: block, type: select, proxies: [legacy, REJECT, DIRECT]}
rules:
 - GEOSITE,cn,missing
 - RULE-SET,regions,missing
 - GEOIP,CN,missing
 - PROCESS-NAME,test,block
 - GEOIP,LAN,DIRECT,no-resolve
 - MATCH,usable
"#;
    let set = build_router_from_clash_yaml(yaml, None).unwrap().router;
    for addr in [
        "10.2.3.4",
        "172.31.255.255",
        "192.168.7.8",
        "127.7.0.1",
        "169.254.3.2",
        "::1",
        "fdab::1",
        "febf::1",
    ] {
        assert_eq!(
            set.rules.evaluate(&RuleContext {
                ip: Some(addr.parse().unwrap()),
                ..Default::default()
            }),
            Some(&RuleAction::Direct)
        );
    }
    assert_eq!(
        set.rules.evaluate(&RuleContext {
            process_name: Some("test"),
            ..Default::default()
        }),
        Some(&RuleAction::Reject)
    );
    assert_eq!(
        set.rules.evaluate(&RuleContext::default()),
        Some(&RuleAction::Proxy("usable".into()))
    );
    assert!(
        build_router_from_clash_yaml(&yaml.replace("MATCH,usable", "MATCH,empty"), None).is_err()
    );
    assert!(build_router_from_clash_yaml(
        &yaml.replace("MATCH,usable", "IP-CIDR6,::1/129,usable"),
        None
    )
    .is_err());
    assert!(RuleSet::from_lines(&["GEOIP,LAN,DIRECT"]).is_err());
}
