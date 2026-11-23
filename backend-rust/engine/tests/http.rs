use smartvpn_engine::{
    chain::{self, Transport},
    http_connect,
};
use std::io::{self, Cursor, Read, Write};

struct Peer {
    response: Cursor<Vec<u8>>,
    request: Vec<u8>,
}
impl Peer {
    fn new(response: &[u8]) -> Self {
        Self {
            response: Cursor::new(response.to_vec()),
            request: Vec::new(),
        }
    }
}
impl Read for Peer {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        self.response.read(buf)
    }
}
impl Write for Peer {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.request.write(buf)
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[test]
fn connect_preserves_payload_after_interim_reply() {
    let mut peer = Peer::new(b"HTTP/1.1 103 Early Hints\r\n\r\nHTTP/1.1 200 OK\r\n\r\nhello");
    http_connect::connect(&mut peer, "::1", 443, Some((b"a", b"b"))).unwrap();
    assert_eq!(
        peer.request,
        b"CONNECT [::1]:443 HTTP/1.1\r\nHost: [::1]:443\r\nProxy-Authorization: Basic YTpi\r\n\r\n"
    );
    let mut payload = String::new();
    peer.read_to_string(&mut payload).unwrap();
    assert_eq!(payload, "hello");
}

#[test]
fn first_hop_denial_prevents_exit_handshake() {
    let mut peer = Peer::new(b"HTTP/1.1 407 Proxy Authentication Required\r\n\r\n");
    assert!(chain::connect(
        &mut peer,
        Transport::Http(None),
        &[(
            "exit.invalid",
            80,
            Transport::Http(Some((b"private", b"secret")))
        )],
        ("target.invalid", 443)
    )
    .is_err());
    assert_eq!(
        peer.request,
        b"CONNECT exit.invalid:80 HTTP/1.1\r\nHost: exit.invalid:80\r\n\r\n"
    );
}

#[test]
fn rejects_invalid_authority_before_writing() {
    let mut peer = Peer::new(&[]);
    assert!(http_connect::connect(&mut peer, "a\r\nx: value", 443, None).is_err());
    assert!(peer.request.is_empty());
}
