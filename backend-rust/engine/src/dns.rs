use std::{
    collections::{HashMap, VecDeque},
    net::{IpAddr, Ipv4Addr},
    sync::Mutex,
};

const BASE: u32 = u32::from_be_bytes([198, 18, 0, 0]);
const CAPACITY: usize = 65533;

pub struct FakeIpPool {
    capacity: usize,
    state: Mutex<PoolState>,
}
#[derive(Default)]
struct PoolState {
    names: HashMap<String, Ipv4Addr>,
    addresses: HashMap<Ipv4Addr, String>,
    order: VecDeque<Ipv4Addr>,
}

impl Default for FakeIpPool {
    fn default() -> Self {
        Self::new(CAPACITY)
    }
}
impl FakeIpPool {
    pub fn new(capacity: usize) -> Self {
        assert!(
            (1..=CAPACITY).contains(&capacity),
            "invalid Fake-IP capacity"
        );
        Self {
            capacity,
            state: Mutex::default(),
        }
    }
    pub fn allocate(&self, domain: &str) -> Ipv4Addr {
        let domain = domain.to_ascii_lowercase();
        let mut s = self.state.lock().expect("Fake-IP pool poisoned");
        if let Some(ip) = s.names.get(&domain) {
            return *ip;
        }
        let ip = if s.order.len() == self.capacity {
            let ip = s.order.pop_front().expect("full pool");
            let old = s.addresses.remove(&ip).expect("allocated address");
            s.names.remove(&old);
            ip
        } else {
            Ipv4Addr::from(BASE + 2 + s.order.len() as u32)
        };
        s.order.push_back(ip);
        s.addresses.insert(ip, domain.clone());
        s.names.insert(domain, ip);
        ip
    }
    pub fn lookup(&self, ip: IpAddr) -> Option<String> {
        let IpAddr::V4(ip) = ip else { return None };
        self.state
            .lock()
            .expect("Fake-IP pool poisoned")
            .addresses
            .get(&ip)
            .cloned()
    }
    pub fn is_fake_ip(&self, ip: IpAddr) -> bool {
        matches!(ip,IpAddr::V4(a) if u32::from(a)>>16==BASE>>16)
    }
}

fn take<'a>(data: &mut &'a [u8], n: usize) -> Option<&'a [u8]> {
    let (head, tail) = data.split_at_checked(n)?;
    *data = tail;
    Some(head)
}
fn field<'a>(data: &mut &'a [u8], bytes: usize) -> Option<&'a [u8]> {
    let len = take(data, bytes)?
        .iter()
        .fold(0usize, |a, &b| (a << 8) | usize::from(b));
    take(data, len)
}

// A ClientHello can span multiple consecutive TLS handshake records.
pub fn sniff_tls_sni(mut prefix: &[u8]) -> Option<String> {
    let mut hello = Vec::new();
    loop {
        if take(&mut prefix, 2)? != [22, 3] {
            return None;
        }
        take(&mut prefix, 1)?;
        hello.extend_from_slice(field(&mut prefix, 2)?);
        if hello.first().is_some_and(|b| *b != 1) {
            return None;
        }
        if hello.len() >= 4 {
            let mut handshake = &hello[1..];
            if let Some(body) = field(&mut handshake, 3) {
                return hello_sni(body);
            }
        }
    }
}
fn hello_sni(mut body: &[u8]) -> Option<String> {
    take(&mut body, 34)?;
    field(&mut body, 1)?;
    field(&mut body, 2)?;
    field(&mut body, 1)?;
    let mut extensions = field(&mut body, 2)?;
    while !extensions.is_empty() {
        let kind = take(&mut extensions, 2)?;
        let mut value = field(&mut extensions, 2)?;
        if kind == [0, 0] {
            let mut names = field(&mut value, 2)?;
            while !names.is_empty() {
                let kind = take(&mut names, 1)?;
                let name = field(&mut names, 2)?;
                if kind == [0] {
                    let host = std::str::from_utf8(name).ok()?;
                    if host.is_empty()
                        || !host
                            .bytes()
                            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_'))
                    {
                        return None;
                    }
                    return Some(host.to_owned());
                }
            }
        }
    }
    None
}

pub fn sniff_http_host(prefix: &[u8]) -> Option<String> {
    let mut headers = [httparse::EMPTY_HEADER; 128];
    let mut req = httparse::Request::new(&mut headers);
    if !req.parse(prefix).ok()?.is_complete() {
        return None;
    }
    let host = req
        .headers
        .iter()
        .find(|h| h.name.eq_ignore_ascii_case("host"))?;
    let authority = std::str::from_utf8(host.value)
        .ok()?
        .parse::<http::uri::Authority>()
        .ok()?;
    if authority.as_str().contains('@') {
        return None;
    }
    Some(authority.host().trim_matches(['[', ']']).to_owned())
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn fake_ip_eviction() {
        let pool = FakeIpPool::new(2);
        let first = pool.allocate("Example.com");
        let second = pool.allocate("two");
        assert_eq!(pool.allocate("EXAMPLE.COM"), first);
        assert_eq!(pool.allocate("three"), first);
        assert_eq!(pool.lookup(first.into()).as_deref(), Some("three"));
        assert_eq!(pool.allocate("example.com"), second);
    }
    #[test]
    fn real_client_hello_and_fragmented_records() {
        use std::sync::Arc;
        crate::transport::tls::ensure_crypto_provider();
        let cfg = rustls::ClientConfig::builder()
            .with_root_certificates(rustls::RootCertStore::empty())
            .with_no_client_auth();
        let mut client =
            rustls::ClientConnection::new(Arc::new(cfg), "sni.example.com".try_into().unwrap())
                .unwrap();
        let mut bytes = Vec::new();
        client.write_tls(&mut bytes).unwrap();
        assert_eq!(sniff_tls_sni(&bytes).as_deref(), Some("sni.example.com"));
        for end in 0..bytes.len() {
            assert!(sniff_tls_sni(&bytes[..end]).is_none());
        }
        let mut fragments = Vec::new();
        for part in bytes[5..].chunks(37) {
            fragments.extend_from_slice(&bytes[..3]);
            fragments.extend_from_slice(&(part.len() as u16).to_be_bytes());
            fragments.extend_from_slice(part);
        }
        assert_eq!(
            sniff_tls_sni(&fragments).as_deref(),
            Some("sni.example.com")
        );
    }
}
