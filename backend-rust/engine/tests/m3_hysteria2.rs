use blake2::{
    digest::{Update, VariableOutput},
    Blake2bVar,
};
use bytes::Bytes;
use quinn::{Connection, Endpoint};
use smartvpn_engine::{
    dialer::{OutboundDialer, OutboundUdpDialer, TargetAddr},
    protocol::hysteria2::{Hysteria2Config, Hysteria2Dialer},
    transport::tls::ensure_crypto_provider,
};
use std::{io, net::SocketAddr, sync::Arc, time::Duration};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream, UdpSocket},
    time::timeout,
};

fn server() -> Endpoint {
    ensure_crypto_provider();
    let cert = rcgen::generate_simple_self_signed(vec!["localhost".into()]).unwrap();
    let mut tls = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(
            vec![cert.cert.der().clone()],
            rustls::pki_types::PrivatePkcs8KeyDer::from(cert.key_pair.serialize_der()).into(),
        )
        .unwrap();
    tls.alpn_protocols = vec![b"h3".to_vec()];
    let crypto = quinn::crypto::rustls::QuicServerConfig::try_from(tls).unwrap();
    Endpoint::server(
        quinn::ServerConfig::with_crypto(Arc::new(crypto)),
        "127.0.0.1:0".parse().unwrap(),
    )
    .unwrap()
}

fn config(peer: SocketAddr) -> Hysteria2Config {
    Hysteria2Config {
        server: TargetAddr::Ip(peer),
        password: "auth pass".into(),
        sni: Some("localhost".into()),
        insecure: true,
        keepalive: false,
        up_mbps: 10,
        down_mbps: 20,
        obfs_salamander: None,
        hop_ports: vec![],
    }
}

async fn authenticate(
    conn: &Connection,
    status: u16,
    udp: bool,
) -> h3::server::Connection<h3_quinn::Connection, Bytes> {
    let mut h3 = h3::server::builder()
        .send_grease(false)
        .build(h3_quinn::Connection::new(conn.clone()))
        .await
        .unwrap();
    let (req, mut stream) = h3
        .accept()
        .await
        .unwrap()
        .unwrap()
        .resolve_request()
        .await
        .unwrap();
    assert_eq!(req.method(), "POST");
    assert_eq!(req.uri().to_string(), "https://hysteria/auth");
    assert_eq!(req.headers()["hysteria-auth"], "auth pass");
    assert_eq!(req.headers()["hysteria-cc-rx"], "2500000");
    assert!(!req.headers()["hysteria-padding"].is_empty());
    stream
        .send_response(
            http::Response::builder()
                .status(status)
                .header("hysteria-udp", udp.to_string())
                .header("hysteria-cc-rx", "1000000")
                .body(())
                .unwrap(),
        )
        .await
        .unwrap();
    stream.finish().await.unwrap();
    h3
}

async fn number<R: AsyncRead + Unpin>(r: &mut R) -> usize {
    let first = r.read_u8().await.unwrap();
    let mut n = usize::from(first & 63);
    for _ in 1..(1 << (first >> 6)) {
        n = (n << 8) + usize::from(r.read_u8().await.unwrap());
    }
    n
}

async fn field<R: AsyncRead + Unpin>(r: &mut R) -> Vec<u8> {
    let mut field = vec![0; number(r).await];
    r.read_exact(&mut field).await.unwrap();
    field
}

async fn tcp_round_trip(obfs: bool) {
    let endpoint = server();
    let peer = endpoint.local_addr().unwrap();
    let mut cfg = config(peer);
    let mut bridge = None;
    if obfs {
        let socket = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        // Only the hop port has a Salamander decoder. Direct traffic to peer fails.
        cfg.hop_ports = vec![socket.local_addr().unwrap().port()];
        cfg.obfs_salamander = Some("wire secret".into());
        bridge = Some(tokio::spawn(salamander_bridge(socket, peer)));
    }
    let echo = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let target = echo.local_addr().unwrap();
    let origin = tokio::spawn(async move {
        let (mut s, _) = echo.accept().await.unwrap();
        let (mut r, mut w) = s.split();
        tokio::io::copy(&mut r, &mut w).await.unwrap()
    });
    let proxy = tokio::spawn(async move {
        let conn = endpoint.accept().await.unwrap().await.unwrap();
        let _h3 = authenticate(&conn, 233, true).await;
        let (mut tx, mut rx) = conn.accept_bi().await.unwrap();
        assert_eq!(number(&mut rx).await, 0x401);
        let requested = String::from_utf8(field(&mut rx).await).unwrap();
        assert_eq!(requested, target.to_string());
        let _pad = field(&mut rx).await;
        let mut origin = TcpStream::connect(requested).await.unwrap();
        tx.write_all(b"\0\x02ok\x03pad").await.unwrap();
        let mut stream = tokio::io::join(rx, tx);
        tokio::io::copy_bidirectional(&mut stream, &mut origin)
            .await
            .unwrap();
        conn.closed().await;
    });
    let dialer = Hysteria2Dialer::new(cfg);
    let mut s = dialer.dial_tcp(&TargetAddr::Ip(target)).await.unwrap();
    let msg = b"QUIC TCP half close";
    s.write_all(msg).await.unwrap();
    s.shutdown().await.unwrap();
    let mut reply = Vec::new();
    s.read_to_end(&mut reply).await.unwrap();
    assert_eq!(reply, msg);
    assert_eq!(origin.await.unwrap(), msg.len() as u64);
    drop(s);
    drop(dialer);
    proxy.await.unwrap();
    if let Some(task) = bridge {
        task.abort();
        assert!(task.await.unwrap_err().is_cancelled());
    }
}

// An independent wire peer between a plain Quinn server and the obfuscated client.
async fn salamander_bridge(socket: UdpSocket, server: SocketAddr) {
    let mut client = None;
    let mut buf = vec![0; 65535];
    loop {
        let (n, from) = socket.recv_from(&mut buf).await.unwrap();
        let (salt, mut body, to) = if from == server {
            (
                rand::random::<[u8; 8]>(),
                buf[..n].to_vec(),
                client.unwrap(),
            )
        } else {
            client = Some(from);
            (buf[..8].try_into().unwrap(), buf[8..n].to_vec(), server)
        };
        let mut hash = Blake2bVar::new(32).unwrap();
        hash.update(b"wire secret");
        hash.update(&salt);
        let mut mask = [0; 32];
        hash.finalize_variable(&mut mask).unwrap();
        for (b, k) in body.iter_mut().zip(mask.iter().cycle()) {
            *b ^= k;
        }
        if from == server {
            let mut wire = salt.to_vec();
            wire.extend(body);
            socket.send_to(&wire, to).await.unwrap();
        } else {
            socket.send_to(&body, to).await.unwrap();
        }
    }
}

#[tokio::test]
async fn hysteria2_tcp_round_trip() {
    timeout(Duration::from_secs(10), tcp_round_trip(false))
        .await
        .unwrap();
}

#[tokio::test]
async fn hysteria2_salamander_obfs_round_trip() {
    timeout(Duration::from_secs(10), tcp_round_trip(true))
        .await
        .unwrap();
}

async fn relay_udp(conn: &Connection, target: SocketAddr) -> u32 {
    let first = conn.read_datagram().await.unwrap();
    let count = first[7] as usize;
    assert!(count > 1);
    let session = u32::from_be_bytes(first[..4].try_into().unwrap());
    let id = first[..6].to_vec();
    let mut pieces = vec![None; count];
    let mut frame = first;
    for i in 0..count {
        if i > 0 {
            frame = conn.read_datagram().await.unwrap();
        }
        assert_eq!(&frame[..6], id);
        assert_eq!(frame[7] as usize, count);
        let mut r = &frame[8..];
        assert_eq!(field(&mut r).await, target.to_string().as_bytes());
        assert!(pieces[frame[6] as usize].replace(r.to_vec()).is_none());
    }
    let payload: Vec<u8> = pieces.into_iter().flat_map(Option::unwrap).collect();
    let outbound = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    outbound.send_to(&payload, target).await.unwrap();
    let mut reply = vec![0; 65535];
    let (n, src) = outbound.recv_from(&mut reply).await.unwrap();
    let addr = src.to_string();
    let chunks: Vec<_> = reply[..n].chunks(600).collect();
    for (index, chunk) in chunks.iter().enumerate().rev() {
        let mut frame = id.clone();
        frame.extend_from_slice(&[index as u8, chunks.len() as u8, addr.len() as u8]);
        frame.extend_from_slice(addr.as_bytes());
        frame.extend_from_slice(chunk);
        conn.send_datagram_wait(frame.clone().into()).await.unwrap();
        if index == chunks.len() - 1 {
            conn.send_datagram_wait(frame.into()).await.unwrap();
        }
    }
    session
}

#[tokio::test]
async fn hysteria2_udp_round_trip() {
    timeout(Duration::from_secs(10), async {
        let endpoint = server();
        let dialer = Hysteria2Dialer::new(config(endpoint.local_addr().unwrap()));
        let echo = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let target = echo.local_addr().unwrap();
        let origin = tokio::spawn(async move {
            let mut buf = vec![0; 8192];
            for _ in 0..2 {
                let (n, from) = echo.recv_from(&mut buf).await.unwrap();
                echo.send_to(&buf[..n], from).await.unwrap();
            }
        });
        let proxy = tokio::spawn(async move {
            let conn = endpoint.accept().await.unwrap().await.unwrap();
            let _h3 = authenticate(&conn, 233, true).await;
            let one = relay_udp(&conn, target).await;
            let two = relay_udp(&conn, target).await;
            assert_ne!(one, two);
            conn.closed().await;
        });
        let one = dialer.dial_udp().await.unwrap();
        let two = dialer.dial_udp().await.unwrap();
        let target = TargetAddr::Ip(target);
        let a: Vec<_> = (0..5000).map(|i| (i % 251) as u8).collect();
        let b = vec![99; 3500];
        assert_eq!(one.send_to(&a, &target).await.unwrap(), a.len());
        assert_eq!(two.send_to(&b, &target).await.unwrap(), b.len());
        for (socket, msg) in [(two, b), (one, a)] {
            let mut buf = vec![0; msg.len()];
            assert_eq!(
                socket.recv_from(&mut buf).await.unwrap(),
                (msg.len(), target.clone())
            );
            assert_eq!(buf, msg);
        }
        origin.await.unwrap();
        drop(dialer);
        proxy.await.unwrap();
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn hysteria2_auth_and_udp_rejections() {
    timeout(Duration::from_secs(10), async {
        for (status, kind) in [
            (403, io::ErrorKind::PermissionDenied),
            (233, io::ErrorKind::Unsupported),
        ] {
            let endpoint = server();
            let dialer = Hysteria2Dialer::new(config(endpoint.local_addr().unwrap()));
            let proxy = tokio::spawn(async move {
                let conn = endpoint.accept().await.unwrap().await.unwrap();
                let _h3 = authenticate(&conn, status, false).await;
                conn.closed().await;
            });
            assert_eq!(dialer.dial_udp().await.err().unwrap().kind(), kind);
            drop(dialer);
            proxy.await.unwrap();
        }
    })
    .await
    .unwrap();
}
