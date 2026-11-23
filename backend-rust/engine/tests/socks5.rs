use smartvpn_engine::socks5::connect;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::thread;
use std::time::Duration;

fn timeout(s: &TcpStream) {
    let limit = Some(Duration::from_secs(3));
    s.set_read_timeout(limit).unwrap();
    s.set_write_timeout(limit).unwrap();
}

#[test]
fn authenticated_tunnel_preserves_first_payload_bytes() {
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    let addr = listener.local_addr().unwrap();
    let peer = thread::spawn(move || {
        let (mut s, _) = listener.accept().unwrap();
        timeout(&s);
        let mut greeting = [0; 3];
        s.read_exact(&mut greeting).unwrap();
        assert_eq!(greeting, [5, 1, 2]);
        s.write_all(&[5, 2]).unwrap();
        let mut auth = [0; 5];
        s.read_exact(&mut auth).unwrap();
        assert_eq!(auth, [1, 1, b'u', 1, b'p']);
        s.write_all(&[1, 0]).unwrap();
        let mut target = [0; 16];
        s.read_exact(&mut target).unwrap();
        assert_eq!(&target, b"\x05\x01\x00\x03\x09localhost\x01\xbb");
        s.write_all(b"\x05\x00\x00\x03\x01x\x00\x00ready").unwrap();
        let mut ping = [0; 4];
        s.read_exact(&mut ping).unwrap();
        s.write_all(&ping).unwrap();
    });
    let mut s = TcpStream::connect(addr).unwrap();
    timeout(&s);
    connect(&mut s, "localhost", 443, Some((b"u", b"p"))).unwrap();
    let mut ready = [0; 5];
    s.read_exact(&mut ready).unwrap();
    assert_eq!(&ready, b"ready");
    s.write_all(b"ping").unwrap();
    let mut pong = [0; 4];
    s.read_exact(&mut pong).unwrap();
    assert_eq!(&pong, b"ping");
    peer.join().unwrap();
}

#[test]
fn rejects_authentication_downgrade_before_sending_credentials() {
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    let addr = listener.local_addr().unwrap();
    let peer = thread::spawn(move || {
        let (mut s, _) = listener.accept().unwrap();
        timeout(&s);
        let mut greeting = [0; 3];
        s.read_exact(&mut greeting).unwrap();
        s.write_all(&[5, 0]).unwrap();
        assert_eq!(s.read(&mut [0]).unwrap(), 0);
    });
    let mut s = TcpStream::connect(addr).unwrap();
    timeout(&s);
    let err = connect(&mut s, "::1", 443, Some((b"user", b"secret"))).unwrap_err();
    assert_eq!(err.kind(), std::io::ErrorKind::PermissionDenied);
    drop(s);
    peer.join().unwrap();
}
