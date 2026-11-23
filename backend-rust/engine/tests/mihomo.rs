//! Reference interoperability with a separately started, loopback-only Mihomo.
use smartvpn_engine::chain::{self, Transport};
use smartvpn_engine::socks5::connect;
use std::io::{Read, Write};
use std::net::{Ipv4Addr, TcpListener, TcpStream};
use std::time::Duration;

#[test]
#[ignore = "requires SMARTVPN_TEST_SOCKS_PORT on a local Mihomo with test-only authentication"]
fn round_trip_through_reference_kernel() {
    let port: u16 = std::env::var("SMARTVPN_TEST_SOCKS_PORT")
        .unwrap()
        .parse()
        .unwrap();
    check_round_trip(port, |mut tunnel, dest| {
        connect(
            &mut tunnel,
            "127.0.0.1",
            dest,
            Some((b"fixture", b"loopback")),
        )
        .unwrap();
        tunnel
    });
}

#[test]
#[ignore = "requires two independent local Mihomo processes with authenticated HTTP and SOCKS5 ports"]
fn http_and_socks_chain_in_both_orders() {
    let http: u16 = std::env::var("SMARTVPN_TEST_HTTP_PORT")
        .unwrap()
        .parse()
        .unwrap();
    let socks: u16 = std::env::var("SMARTVPN_TEST_SOCKS_PORT")
        .unwrap()
        .parse()
        .unwrap();
    let auth = Some((b"fixture".as_slice(), b"loopback".as_slice()));
    let h = Transport::Http(auth);
    let s = Transport::Socks5(auth);
    for (first, entry, second, exit) in [(http, h, socks, s), (socks, s, http, h)] {
        check_round_trip(first, |socket, dest| {
            chain::connect(
                socket,
                entry,
                &[("127.0.0.1", second, exit)],
                ("127.0.0.1", dest),
            )
            .unwrap()
        });
    }
}

fn check_round_trip(port: u16, establish: impl FnOnce(TcpStream, u16) -> TcpStream) {
    let target = TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
    let addr = target.local_addr().unwrap();
    let limit = Some(Duration::from_secs(5));
    let tunnel =
        TcpStream::connect_timeout(&(Ipv4Addr::LOCALHOST, port).into(), limit.unwrap()).unwrap();
    tunnel.set_read_timeout(limit).unwrap();
    tunnel.set_write_timeout(limit).unwrap();
    let mut tunnel = establish(tunnel, addr.port());
    tunnel.write_all(b"request").unwrap();
    target.set_nonblocking(true).unwrap();
    let deadline = std::time::Instant::now() + limit.unwrap();
    let mut destination = loop {
        match target.accept() {
            Ok((stream, _)) => break stream,
            Err(e)
                if e.kind() == std::io::ErrorKind::WouldBlock
                    && std::time::Instant::now() < deadline =>
            {
                std::thread::sleep(Duration::from_millis(10));
            }
            Err(e) => panic!("target accept failed: {e}"),
        }
    };
    destination.set_nonblocking(false).unwrap();
    destination.set_read_timeout(limit).unwrap();
    destination.set_write_timeout(limit).unwrap();
    let mut request = [0; 7];
    destination.read_exact(&mut request).unwrap();
    assert_eq!(&request, b"request");
    destination.write_all(b"response").unwrap();
    let mut response = [0; 8];
    tunnel.read_exact(&mut response).unwrap();
    assert_eq!(&response, b"response");
}
