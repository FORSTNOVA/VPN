use anyhow::{ensure, Context, Result};
use hickory_proto::{
    op::{Message, MessageType, Query, ResponseCode},
    rr::{Name, RData, RecordType},
};
use std::{
    io,
    net::{SocketAddr, UdpSocket},
    time::Duration,
};

fn lookup(server: SocketAddr, name: &Name, port: u16) -> Result<Vec<SocketAddr>> {
    let socket = UdpSocket::bind("0.0.0.0:0")?;
    socket.connect(server)?;
    socket.set_read_timeout(Some(Duration::from_millis(1200)))?;
    let mut query = Message::query();
    query.metadata.recursion_desired = true;
    query
        .queries
        .push(Query::query(name.clone(), RecordType::A));
    socket.send(&query.to_vec()?)?;
    let mut buf = [0; 4096];
    let len = socket.recv(&mut buf)?;
    let answer = Message::from_vec(&buf[..len])?;
    ensure!(
        answer.metadata.id == query.metadata.id
            && answer.metadata.message_type == MessageType::Response
            && !answer.metadata.truncation
            && answer.metadata.response_code == ResponseCode::NoError
            && answer.queries == query.queries,
        "invalid DNS response"
    );
    let mut owner = name;
    for _ in 0..=answer.answers.len() {
        let addresses: Vec<_> = answer
            .answers
            .iter()
            .filter(|r| &r.name == owner)
            .filter_map(|r| match &r.data {
                RData::A(a) => Some(SocketAddr::new(a.0.into(), port)),
                _ => None,
            })
            .collect();
        if !addresses.is_empty() {
            return Ok(addresses);
        }
        owner = answer
            .answers
            .iter()
            .find_map(|r| match &r.data {
                RData::CNAME(alias) if &r.name == owner => Some(&alias.0),
                _ => None,
            })
            .context("DNS response contains no IPv4 address")?;
    }
    anyhow::bail!("cyclic DNS alias")
}

pub fn resolve(netloc: &str) -> io::Result<Vec<SocketAddr>> {
    if let Ok(ip) = netloc.parse() {
        return Ok(vec![ip]);
    }
    let (host, port) = netloc
        .rsplit_once(':')
        .ok_or_else(|| io::Error::other("missing HTTP port"))?;
    let port = port.parse().map_err(io::Error::other)?;
    if host.eq_ignore_ascii_case("localhost") {
        return Ok(vec![([127, 0, 0, 1], port).into()]);
    }
    let name =
        Name::from_ascii(format!("{}.", host.trim_end_matches('.'))).map_err(io::Error::other)?;
    let mut errors = Vec::new();
    for ip in [[223, 5, 5, 5], [119, 29, 29, 29]] {
        match lookup((ip, 53).into(), &name, port) {
            Ok(addresses) => return Ok(addresses),
            Err(e) => errors.push(e.to_string()),
        }
    }
    Err(io::Error::other(errors.join("; ")))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn local_udp_reply_and_literal_addresses() {
        assert_eq!(
            resolve("localhost:8765").unwrap(),
            vec![SocketAddr::from(([127, 0, 0, 1], 8765))]
        );
        let mock = UdpSocket::bind("127.0.0.1:0").unwrap();
        let server = mock.local_addr().unwrap();
        let worker = std::thread::spawn(move || {
            let mut buf = [0; 512];
            let (n, peer) = mock.recv_from(&mut buf).unwrap();
            let mut reply = buf[..n].to_vec();
            reply[2..4].copy_from_slice(&[0x81, 0x80]);
            reply[6..8].copy_from_slice(&[0, 1]);
            reply.extend_from_slice(&[0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 8]);
            mock.send_to(&reply, peer).unwrap();
        });
        assert_eq!(
            lookup(server, &Name::from_ascii("test.invalid.").unwrap(), 443).unwrap(),
            vec![SocketAddr::from(([192, 0, 2, 8], 443))]
        );
        worker.join().unwrap();
    }
}
