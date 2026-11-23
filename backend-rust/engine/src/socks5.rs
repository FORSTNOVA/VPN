use std::io::{self, Read, Write};
use std::net::IpAddr;

/// Negotiate CONNECT on an already connected upstream stream. The caller owns
/// the socket, its timeouts, and its lifecycle. Leave all tunnel data unread.
pub fn connect(
    stream: &mut (impl Read + Write),
    host: &str,
    port: u16,
    credentials: Option<(&[u8], &[u8])>,
) -> io::Result<()> {
    let mut request = vec![5, 1, 0];
    match host.parse::<IpAddr>() {
        Ok(IpAddr::V4(ip)) => {
            request.push(1);
            request.extend(ip.octets());
        }
        Ok(IpAddr::V6(ip)) => {
            request.push(4);
            request.extend(ip.octets());
        }
        Err(_) => {
            request.push(3);
            append_field(&mut request, host.as_bytes())?;
        }
    }
    request.extend(port.to_be_bytes());
    let mut auth = vec![1];
    let method = if let Some((user, password)) = credentials {
        append_field(&mut auth, user)?;
        append_field(&mut auth, password)?;
        2
    } else {
        0
    };
    exchange(stream, &[5, 1, method], [5, method])?;
    if credentials.is_some() {
        exchange(stream, &auth, [1, 0])?;
    }
    stream.write_all(&request)?;
    stream.flush()?;
    let mut header = [0; 4];
    stream.read_exact(&mut header)?;
    if header[0] != 5 || header[2] != 0 {
        return Err(invalid_reply());
    }
    if header[1] != 0 {
        return Err(io::Error::other(format!(
            "SOCKS5 CONNECT failed with reply {}",
            header[1]
        )));
    }
    let addr_len = match header[3] {
        1 => 4,
        4 => 16,
        3 => {
            let mut len = [0];
            stream.read_exact(&mut len)?;
            if len[0] == 0 {
                return Err(invalid_reply());
            }
            usize::from(len[0])
        }
        _ => return Err(invalid_reply()),
    };
    let mut bound = [0; 257];
    stream.read_exact(&mut bound[..addr_len + 2])
}

fn append_field(out: &mut Vec<u8>, value: &[u8]) -> io::Result<()> {
    if value.is_empty() || value.len() > 255 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "SOCKS5 field must contain 1 to 255 bytes",
        ));
    }
    out.push(value.len() as u8);
    out.extend_from_slice(value);
    Ok(())
}

fn invalid_reply() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "invalid SOCKS5 reply")
}

fn exchange(stream: &mut (impl Read + Write), request: &[u8], expected: [u8; 2]) -> io::Result<()> {
    stream.write_all(request)?;
    stream.flush()?;
    let mut reply = [0; 2];
    stream.read_exact(&mut reply)?;
    if reply != expected {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "SOCKS5 authentication rejected",
        ));
    }
    Ok(())
}
