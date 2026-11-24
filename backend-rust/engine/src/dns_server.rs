use crate::{
    dns::FakeIpPool,
    rule::{RuleAction, RuleContext, RuleSet},
};
use std::{io, net::SocketAddr, sync::Arc, time::Duration};
use tokio::{net::UdpSocket, task::JoinSet, time::timeout};

pub struct DnsServer {
    socket: UdpSocket,
    pool: Arc<FakeIpPool>,
    rules: Arc<RuleSet>,
    upstream: Option<SocketAddr>,
}
struct Question {
    name: String,
    kind: u16,
    class: u16,
    end: usize,
}
fn question(packet: &[u8]) -> Option<Question> {
    if packet.len() < 12 || packet[4..6] != [0, 1] {
        return None;
    }
    let mut pos = 12;
    let mut labels = Vec::new();
    loop {
        let len = *packet.get(pos)? as usize;
        pos += 1;
        if len == 0 {
            break;
        }
        if len > 63 || pos + len - 12 >= 255 {
            return None;
        }
        let label = packet.get(pos..pos + len)?;
        if !label.iter().all(|b| b.is_ascii_graphic() && *b != b'.') {
            return None;
        }
        labels.push(std::str::from_utf8(label).ok()?);
        pos += len;
    }
    let fields = packet.get(pos..pos + 4)?;
    Some(Question {
        name: labels.join("."),
        kind: u16::from_be_bytes([fields[0], fields[1]]),
        class: u16::from_be_bytes([fields[2], fields[3]]),
        end: pos + 4,
    })
}
fn reply(query: &[u8], q: Option<&Question>, code: u8) -> Vec<u8> {
    let mut out = vec![
        query[0],
        query[1],
        0x80 | (query[2] & 0x79),
        0x80 | code,
        0,
        u8::from(q.is_some()),
        0,
        0,
        0,
        0,
        0,
        0,
    ];
    if let Some(q) = q {
        out.extend_from_slice(&query[12..q.end]);
    }
    out
}

impl DnsServer {
    pub async fn bind(
        listen_addr: SocketAddr,
        fake_ip_pool: Arc<FakeIpPool>,
        rules: Arc<RuleSet>,
        upstream: Option<SocketAddr>,
    ) -> io::Result<Self> {
        Ok(Self {
            socket: UdpSocket::bind(listen_addr).await?,
            pool: fake_ip_pool,
            rules,
            upstream,
        })
    }
    pub fn local_addr(&self) -> io::Result<SocketAddr> {
        self.socket.local_addr()
    }
    pub async fn serve(self) -> io::Result<()> {
        let server = Arc::new(self);
        let mut packet = vec![0; 65535];
        let mut tasks = JoinSet::new();
        loop {
            tokio::select! {
                result = tasks.join_next(), if !tasks.is_empty() => {
                    result.expect("active DNS task").map_err(io::Error::other)??;
                }
                received = server.socket.recv_from(&mut packet), if tasks.len() < 128 => {
                    let (len, peer) = received?;
                    // A response or an incomplete header cannot be answered as a DNS query.
                    if len < 12 || packet[2] & 0x80 != 0 {
                        continue;
                    }
                    let query = packet[..len].to_vec();
                    let server = server.clone();
                    tasks.spawn(async move {
                        let answer = answer(&query, &server.pool, &server.rules, server.upstream).await;
                        server.socket.send_to(&answer, peer).await
                    });
                }
            }
        }
    }
}
pub(crate) async fn answer(
    query: &[u8],
    pool: &FakeIpPool,
    rules: &RuleSet,
    upstream: Option<SocketAddr>,
) -> Vec<u8> {
    let Some(q) = question(query) else {
        return reply(query, None, 1);
    };
    if query[2] & 0x78 != 0 || q.class != 1 {
        return reply(query, Some(&q), 4);
    }
    let action = rules.evaluate(&RuleContext {
        domain: Some(&q.name),
        ..Default::default()
    });
    let fake =
        upstream.is_none() || matches!(action, Some(RuleAction::Proxy(_) | RuleAction::Reject));
    if fake && matches!(q.kind, 1 | 28) {
        let mut out = reply(query, Some(&q), 0);
        if q.kind == 1 {
            out[7] = 1;
            out.extend_from_slice(&[0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 1, 0, 4]);
            out.extend_from_slice(&pool.allocate(&q.name).octets());
        }
        return out;
    }
    let Some(upstream) = upstream else {
        return reply(query, Some(&q), 3);
    };
    match timeout(Duration::from_secs(4), forward(query, &q, upstream))
        .await
        .map_err(io::Error::other)
        .and_then(|result| result)
    {
        Ok(response) => response,
        Err(error) => {
            eprintln!("DNS upstream {upstream} for {}: {error}", q.name);
            reply(query, Some(&q), 2)
        }
    }
}
async fn forward(query: &[u8], q: &Question, upstream: SocketAddr) -> io::Result<Vec<u8>> {
    let socket = UdpSocket::bind(if upstream.is_ipv4() {
        "0.0.0.0:0"
    } else {
        "[::]:0"
    })
    .await?;
    #[cfg(unix)]
    crate::dialer::protect_socket_fd(std::os::fd::AsRawFd::as_raw_fd(&socket))?;
    socket.connect(upstream).await?;
    socket.send(query).await?;
    let mut response = vec![0; 65535];
    let n = socket.recv(&mut response).await?;
    response.truncate(n);
    if !question(&response).is_some_and(|answer| {
        response[..2] == query[..2]
            && response[2] & 0x80 != 0
            && answer.name.eq_ignore_ascii_case(&q.name)
            && answer.kind == q.kind
            && answer.class == q.class
    }) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "mismatched DNS reply",
        ));
    }
    Ok(response)
}
