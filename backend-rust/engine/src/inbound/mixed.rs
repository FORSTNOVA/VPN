use crate::{
    dialer::TargetAddr,
    dns::{sniff_http_host, sniff_tls_sni},
    router::{RouteResult, Router},
};
use http::{uri::Authority, Uri};
use std::{
    io,
    net::{IpAddr, SocketAddr},
    sync::Arc,
    time::Duration,
};
use tokio::{
    io::{copy_bidirectional, AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    time::timeout,
};

pub struct MixedServer {
    listener: TcpListener,
    router: Arc<Router>,
}
impl MixedServer {
    pub async fn bind(addr: SocketAddr, router: Arc<Router>) -> io::Result<Self> {
        Ok(Self {
            listener: TcpListener::bind(addr).await?,
            router,
        })
    }
    pub fn local_addr(&self) -> io::Result<SocketAddr> {
        self.listener.local_addr()
    }
    pub async fn serve(self) -> io::Result<()> {
        loop {
            let (client, src) = self.listener.accept().await?;
            let router = self.router.clone();
            tokio::spawn(async move {
                if let Err(e) = handle(client, src, &router).await {
                    eprintln!("mixed connection from {src}: {e}");
                }
            });
        }
    }
}
fn invalid(msg: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg)
}
enum Protocol {
    Socks,
    Connect,
    Http,
}
struct Request {
    protocol: Protocol,
    target: TargetAddr,
    prefix: Vec<u8>,
    host: Option<String>,
}
async fn socks_reply(client: &mut TcpStream, status: u8) -> io::Result<()> {
    client.write_all(&[5, status, 0, 1, 0, 0, 0, 0, 0, 0]).await
}
async fn socks(client: &mut TcpStream) -> io::Result<Request> {
    let n = client.read_u8().await?;
    let mut methods = vec![0; n as usize];
    client.read_exact(&mut methods).await?;
    if !methods.contains(&0) {
        client.write_all(&[5, 255]).await?;
        return Err(invalid("SOCKS5 requires no-auth method"));
    }
    client.write_all(&[5, 0]).await?;
    let mut header = [0; 3];
    client.read_exact(&mut header).await?;
    if header[0] != 5 || header[2] != 0 {
        socks_reply(client, 1).await?;
        return Err(invalid("invalid SOCKS5 request header"));
    }
    if header[1] != 1 {
        socks_reply(client, 7).await?;
        return Err(invalid("unsupported SOCKS5 command"));
    }
    let target = match TargetAddr::read_socks_addr(client).await {
        Ok(TargetAddr::Domain(h, _)) if h.is_empty() => {
            socks_reply(client, 8).await?;
            return Err(invalid("empty SOCKS5 domain"));
        }
        Ok(t) => t,
        Err(e) => {
            socks_reply(client, 8).await?;
            return Err(e);
        }
    };
    Ok(Request {
        protocol: Protocol::Socks,
        target,
        prefix: vec![],
        host: None,
    })
}

fn destination(authority: &Authority, default_port: Option<u16>) -> io::Result<TargetAddr> {
    if authority.as_str().contains('@') {
        return Err(invalid("userinfo in proxy authority"));
    }
    let host = authority.host();
    let port = if authority.as_str() == host {
        default_port.ok_or_else(|| invalid("missing target port"))?
    } else {
        authority
            .port_u16()
            .ok_or_else(|| invalid("invalid target port"))?
    };
    if host.is_empty() {
        return Err(invalid("empty target host"));
    }
    let host = host.trim_matches(['[', ']']);
    Ok(match host.parse::<IpAddr>() {
        Ok(ip) => TargetAddr::Ip((ip, port).into()),
        Err(_) => TargetAddr::domain(host, port),
    })
}
async fn http_request(client: &mut TcpStream, first: u8) -> io::Result<Request> {
    let mut data = vec![first];
    let mut buf = [0; 4096];
    let mut scan = 0;
    let end = loop {
        if let Some(i) = data[scan..].windows(4).position(|w| w == b"\r\n\r\n") {
            break scan + i + 4;
        }
        if data.len() >= 32768 {
            return Err(invalid("proxy header exceeds 32 KiB"));
        }
        scan = data.len().saturating_sub(3);
        let limit = buf.len().min(32768 - data.len());
        let n = client.read(&mut buf[..limit]).await?;
        if n == 0 {
            return Err(invalid("truncated HTTP header"));
        }
        data.extend_from_slice(&buf[..n]);
    };
    let mut headers = [httparse::EMPTY_HEADER; 128];
    let mut req = httparse::Request::new(&mut headers);
    req.parse(&data[..end])
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
    let path = req.path.ok_or_else(|| invalid("missing request target"))?;
    if req.method == Some("CONNECT") {
        let authority = path
            .parse::<Authority>()
            .map_err(|_| invalid("invalid CONNECT authority"))?;
        return Ok(Request {
            protocol: Protocol::Connect,
            target: destination(&authority, None)?,
            prefix: data.split_off(end),
            host: None,
        });
    }
    let uri = path
        .parse::<Uri>()
        .map_err(|_| invalid("invalid HTTP URI"))?;
    if uri.scheme_str() != Some("http") {
        return Err(invalid("expected absolute http URI"));
    }
    let authority = uri
        .authority()
        .ok_or_else(|| invalid("missing URI authority"))?;
    let target = destination(authority, Some(80))?;
    let host = sniff_http_host(&data[..end]).filter(|h| h.parse::<IpAddr>().is_err());
    let origin = uri.path_and_query().map_or("/", |p| p.as_str());
    let method = req.method.ok_or_else(|| invalid("missing HTTP method"))?;
    let version = req.version.ok_or_else(|| invalid("missing HTTP version"))?;
    let mut prefix =
        format!("{method} {origin} HTTP/1.{version}\r\nHost: {authority}\r\n").into_bytes();
    for header in req.headers.iter() {
        if ["host", "proxy-authorization", "proxy-connection"]
            .iter()
            .any(|name| header.name.eq_ignore_ascii_case(name))
        {
            continue;
        }
        prefix.extend_from_slice(header.name.as_bytes());
        prefix.extend_from_slice(b": ");
        prefix.extend_from_slice(header.value);
        prefix.extend_from_slice(b"\r\n");
    }
    prefix.extend_from_slice(b"\r\n");
    prefix.extend_from_slice(&data[end..]);
    Ok(Request {
        protocol: Protocol::Http,
        target,
        prefix,
        host,
    })
}

// Retain all bytes consumed before the dial, including an incomplete timed-out prefix.
pub(crate) async fn sniff(
    client: &mut (impl tokio::io::AsyncRead + Unpin),
    data: &mut Vec<u8>,
) -> io::Result<Option<String>> {
    let read = async {
        let mut buf = [0; 2048];
        loop {
            if let Some(host) = sniff_tls_sni(data).or_else(|| sniff_http_host(data)) {
                return Ok(Some(host));
            }
            if data.len() >= 32768
                || data
                    .first()
                    .is_some_and(|b| *b != 22 && !b.is_ascii_alphabetic())
            {
                return Ok(None);
            }
            let n = client.read(&mut buf).await?;
            if n == 0 {
                return Ok(None);
            }
            data.extend_from_slice(&buf[..n]);
        }
    };
    match timeout(Duration::from_millis(100), read).await {
        Ok(result) => result,
        Err(_) => Ok(None),
    }
}
async fn handle(mut client: TcpStream, src: SocketAddr, router: &Router) -> io::Result<()> {
    let first = client.read_u8().await?;
    let mut req = if first == 5 {
        socks(&mut client).await?
    } else {
        match http_request(&mut client, first).await {
            Ok(req) => req,
            Err(e) => {
                client
                    .write_all(b"HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
                    .await?;
                return Err(e);
            }
        }
    };
    if let TargetAddr::Ip(a) = req.target {
        if !matches!(req.protocol, Protocol::Http)
            && !router
                .fake_ip_pool
                .as_ref()
                .is_some_and(|p| p.is_fake_ip(a.ip()))
        {
            req.host = sniff(&mut client, &mut req.prefix).await?;
        }
    }
    let result = router
        .dial(
            src,
            &req.target,
            req.host.as_deref(),
            Some(client.local_addr()?),
        )
        .await;
    let routed = match result {
        Ok(RouteResult::Connected { stream, .. }) => Ok(stream),
        Ok(RouteResult::Reject) => Err((
            2,
            "403 Forbidden",
            io::Error::new(io::ErrorKind::PermissionDenied, "rejected by rule"),
        )),
        Err(e) => {
            let status = match e.kind() {
                io::ErrorKind::ConnectionRefused => 5,
                io::ErrorKind::NetworkUnreachable => 3,
                io::ErrorKind::HostUnreachable => 4,
                _ => 1,
            };
            Err((status, "502 Bad Gateway", e))
        }
    };
    let mut upstream = match routed {
        Ok(stream) => stream,
        Err((status, http, error)) => {
            match req.protocol {
                Protocol::Socks => socks_reply(&mut client, status).await?,
                _ => {
                    client
                        .write_all(
                            format!("HTTP/1.1 {http}\r\nConnection: close\r\n\r\n").as_bytes(),
                        )
                        .await?
                }
            }
            return Err(error);
        }
    };
    match req.protocol {
        Protocol::Socks => socks_reply(&mut client, 0).await?,
        Protocol::Connect => {
            client
                .write_all(b"HTTP/1.1 200 Connection Established\r\n\r\n")
                .await?
        }
        Protocol::Http => {}
    }
    upstream.write_all(&req.prefix).await?;
    upstream.flush().await?;
    copy_bidirectional(&mut client, &mut upstream).await?;
    Ok(())
}
