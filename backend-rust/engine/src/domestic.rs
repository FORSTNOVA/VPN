//! Validated CIDR prefixes and a sorted, disjoint interval index.
use std::{io, net::IpAddr};

pub fn prefix(line: &str) -> io::Result<(IpAddr, u8)> {
    let invalid = || {
        io::Error::new(
            io::ErrorKind::InvalidData,
            format!("invalid domestic CIDR: {line}"),
        )
    };
    let (ip, bits) = line.split_once('/').ok_or_else(invalid)?;
    let ip: IpAddr = ip.parse().map_err(|_| invalid())?;
    let bits: u8 = bits.parse().map_err(|_| invalid())?;
    let (value, width) = number(ip);
    if bits == 0 || bits > width || value.trailing_zeros() < u32::from(width - bits) {
        return Err(invalid());
    }
    Ok((ip, bits))
}

fn number(ip: IpAddr) -> (u128, u8) {
    match ip {
        IpAddr::V4(v) => (u32::from(v) as u128, 32),
        IpAddr::V6(v) => (u128::from(v), 128),
    }
}

#[derive(Clone, Default)]
pub struct Ranges {
    v4: Vec<(u128, u128)>,
    v6: Vec<(u128, u128)>,
}

impl Ranges {
    pub fn new(lines: &[String]) -> io::Result<Self> {
        let mut result = Self::default();
        for line in lines {
            let (ip, bits) = prefix(line)?;
            let (start, width) = number(ip);
            let rows = if ip.is_ipv4() {
                &mut result.v4
            } else {
                &mut result.v6
            };
            rows.push((start, start | ((1u128 << (width - bits)) - 1)));
        }
        for rows in [&mut result.v4, &mut result.v6] {
            rows.sort_unstable();
            let mut compact: Vec<(u128, u128)> = Vec::new();
            for (start, end) in rows.drain(..) {
                if let Some(last) = compact
                    .last_mut()
                    .filter(|r| start <= r.1.saturating_add(1))
                {
                    last.1 = last.1.max(end);
                } else {
                    compact.push((start, end));
                }
            }
            *rows = compact;
        }
        Ok(result)
    }
    pub fn is_empty(&self) -> bool {
        self.v4.is_empty() && self.v6.is_empty()
    }
    pub fn contains(&self, ip: IpAddr) -> bool {
        let ip = ip.to_canonical();
        let rows = if ip.is_ipv4() { &self.v4 } else { &self.v6 };
        let (n, _) = number(ip);
        let i = rows.partition_point(|r| r.0 <= n);
        i > 0 && n <= rows[i - 1].1
    }
}

fn domestic_dns(name: &str) -> bool {
    let name = name.trim_end_matches('.').to_ascii_lowercase();
    include_str!("../../../backend/domestic_dns_suffixes.txt")
        .split_whitespace()
        .any(|suffix| {
            name.strip_suffix(suffix)
                .is_some_and(|prefix| prefix.is_empty() || prefix.ends_with('.'))
        })
}

/// Match the existing DNS protection policy before classifying the returned IP.
pub(crate) async fn encrypted_address(
    dialer: &dyn crate::dialer::OutboundDialer,
    name: &str,
) -> io::Result<(IpAddr, u32)> {
    use crate::{
        dialer::TargetAddr,
        transport::tls::{connect_tls, TlsConfig},
    };
    use http_body_util::{BodyExt, Empty, Limited};
    use hyper_util::rt::TokioIo;
    let query: String = name
        .bytes()
        .map(|b| {
            if b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'_') {
                (b as char).to_string()
            } else {
                format!("%{b:02X}")
            }
        })
        .collect();
    let (resolver, address, server_name, path): (&dyn crate::dialer::OutboundDialer, _, _, _) =
        if domestic_dns(name) {
            (
                &crate::dialer::DirectDialer,
                "223.5.5.5:443",
                "dns.alidns.com",
                "/resolve",
            )
        } else {
            (dialer, "1.1.1.1:443", "cloudflare-dns.com", "/dns-query")
        };
    for kind in [1, 28] {
        let raw = resolver
            .dial_tcp(&TargetAddr::Ip(address.parse().unwrap()))
            .await?;
        let tls = connect_tls(raw, server_name, &TlsConfig::default()).await?;
        let (mut tx, conn) = hyper::client::conn::http1::handshake(TokioIo::new(tls))
            .await
            .map_err(io::Error::other)?;
        tokio::spawn(conn);
        let req = http::Request::get(format!("{path}?name={query}&type={kind}"))
            .header("Host", server_name)
            .header("Accept", "application/dns-json")
            .body(Empty::<bytes::Bytes>::new())
            .map_err(io::Error::other)?;
        let response = tx.send_request(req).await.map_err(io::Error::other)?;
        if !response.status().is_success() {
            return Err(io::Error::other(format!(
                "domestic classification DoH HTTP {}",
                response.status()
            )));
        }
        let bytes = Limited::new(response.into_body(), 65536)
            .collect()
            .await
            .map_err(io::Error::other)?
            .to_bytes();
        #[derive(serde::Deserialize)]
        struct Answer {
            name: String,
            #[serde(rename = "type")]
            kind: u16,
            #[serde(rename = "TTL")]
            ttl: u32,
            data: String,
        }
        #[derive(serde::Deserialize)]
        struct Reply {
            #[serde(rename = "Status")]
            status: u16,
            #[serde(default, rename = "Answer")]
            answers: Vec<Answer>,
        }
        let reply: Reply = serde_json::from_slice(&bytes)?;
        if reply.status != 0 {
            return Err(io::Error::other(format!(
                "domestic classification DNS status {}",
                reply.status
            )));
        }
        let mut owner = name.trim_end_matches('.');
        let mut ttl = 300;
        for _ in 0..=reply.answers.len() {
            let matches = |r: &&Answer| r.name.trim_end_matches('.').eq_ignore_ascii_case(owner);
            if let Some(rr) = reply
                .answers
                .iter()
                .filter(matches)
                .find(|r| r.kind == kind)
            {
                let ip: IpAddr = rr.data.parse().map_err(io::Error::other)?;
                if ip.is_ipv4() != (kind == 1) {
                    return Err(io::Error::other("unexpected DNS address family"));
                }
                return Ok((ip, ttl.min(rr.ttl)));
            }
            let Some(alias) = reply.answers.iter().filter(matches).find(|r| r.kind == 5) else {
                break;
            };
            owner = alias.data.trim_end_matches('.');
            ttl = ttl.min(alias.ttl);
        }
    }
    Err(io::Error::other("no address for domestic classification"))
}

#[cfg(test)]
mod tests {
    #[test]
    fn domestic_resolver_uses_whole_labels() {
        for name in ["WWW.BAIDU.COM.", "example.cn", "baidu.com"] {
            assert!(super::domestic_dns(name));
        }
        for name in ["notbaidu.com", "baidu.com.example.org"] {
            assert!(!super::domestic_dns(name));
        }
    }
}
