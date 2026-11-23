use base64::{engine::general_purpose::STANDARD, Engine};
use std::io::{self, Read, Write};
use std::net::Ipv6Addr;

/// Establish a TCP tunnel through a plain HTTP proxy on the supplied stream.
pub fn connect(
    stream: &mut (impl Read + Write),
    host: &str,
    port: u16,
    credentials: Option<(&[u8], &[u8])>,
) -> io::Result<()> {
    let authority = if let Ok(ip) = host.parse::<Ipv6Addr>() {
        format!("[{ip}]:{port}")
    } else {
        if host.is_empty()
            || !host
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'.' | b'-' | b'_'))
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid proxy destination host",
            ));
        }
        format!("{host}:{port}")
    };
    let mut request = format!("CONNECT {authority} HTTP/1.1\r\nHost: {authority}\r\n");
    if let Some((username, password)) = credentials {
        if username.contains(&b':') {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid Basic username",
            ));
        }
        let encoded = STANDARD.encode([username, b":", password].concat());
        request.push_str(&format!("Proxy-Authorization: Basic {encoded}\r\n"));
    }
    request.push_str("\r\n");
    stream.write_all(request.as_bytes())?;
    stream.flush()?;

    // Read only the header. A proxy can send the next hop's greeting in the
    // same TCP segment, and the caller continues using this exact stream.
    let mut header = Vec::new();
    for _ in 0..32768 {
        let mut byte = [0];
        stream.read_exact(&mut byte)?;
        header.push(byte[0]);
        if !header.ends_with(b"\r\n\r\n") {
            continue;
        }
        let mut fields = [httparse::EMPTY_HEADER; 128];
        let mut reply = httparse::Response::new(&mut fields);
        if !reply.parse(&header).is_ok_and(|r| r.is_complete()) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid HTTP CONNECT response",
            ));
        }
        match reply.code {
            Some(200..=299) => return Ok(()),
            Some(100 | 102..=199) => header.clear(),
            Some(status) => {
                return Err(io::Error::other(format!(
                    "HTTP CONNECT failed with status {status}"
                )))
            }
            None => {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "missing HTTP status",
                ))
            }
        }
    }
    Err(io::Error::new(
        io::ErrorKind::InvalidData,
        "HTTP CONNECT response header too large",
    ))
}
