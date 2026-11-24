use crate::{
    dialer::{BoxFuture, BoxedStream, OutboundDialer, TargetAddr},
    transport::tls::{connect_tls, TlsConfig},
};
use futures_util::{stream::FuturesUnordered, StreamExt};
use std::{io, sync::Arc, time::Duration};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    sync::Mutex,
    time::{timeout, Instant},
};

#[derive(Clone)]
pub struct ProbeConfig {
    pub(crate) url: http::Uri,
    pub(crate) interval: Duration,
    pub(crate) fastest: bool,
}
type Dialer = Arc<dyn OutboundDialer>;
pub(crate) struct TestedGroup {
    config: ProbeConfig,
    members: Vec<Dialer>,
    selected: Mutex<Option<(Instant, usize)>>,
}
impl TestedGroup {
    pub(crate) fn new(config: ProbeConfig, members: Vec<Dialer>) -> Self {
        Self {
            config,
            members,
            selected: Mutex::new(None),
        }
    }
    async fn choose(&self) -> io::Result<usize> {
        let mut selected = self.selected.lock().await;
        if let Some((at, index)) = *selected {
            if at.elapsed() < self.config.interval {
                return Ok(index);
            }
        }
        let mut probes = FuturesUnordered::new();
        for (index, node) in self.members.iter().enumerate() {
            let url = &self.config.url;
            probes.push(async move {
                let start = Instant::now();
                let result = timeout(Duration::from_secs(5), probe(node, url))
                    .await
                    .map_err(io::Error::other)
                    .and_then(|r| r);
                (index, start.elapsed(), result)
            });
        }
        let mut best = None;
        let mut errors = Vec::new();
        while let Some((index, elapsed, result)) = probes.next().await {
            match result {
                Ok(()) => {
                    let score = if self.config.fastest {
                        (elapsed, index)
                    } else {
                        (Duration::ZERO, index)
                    };
                    if best.is_none_or(|old| score < old) {
                        best = Some(score)
                    }
                }
                Err(e) => errors.push(format!("member {index}: {e}")),
            }
        }
        let (_, index) = best
            .ok_or_else(|| io::Error::other(format!("no healthy proxy: {}", errors.join(", "))))?;
        *selected = Some((Instant::now(), index));
        Ok(index)
    }
}
async fn probe(node: &Dialer, url: &http::Uri) -> io::Result<()> {
    let host = url
        .host()
        .expect("validated probe URI")
        .trim_matches(['[', ']']);
    let tls = url.scheme_str() == Some("https");
    let target = TargetAddr::domain(host, url.port_u16().unwrap_or(if tls { 443 } else { 80 }));
    let mut stream = node.dial_tcp(&target).await?;
    if tls {
        stream = connect_tls(stream, host, &TlsConfig::default()).await?;
    }
    let path = url.path_and_query().map_or("/", |p| p.as_str());
    let request = format!(
        "GET {path} HTTP/1.1\r\nHost: {}\r\nConnection: close\r\n\r\n",
        url.authority().unwrap()
    );
    stream.write_all(request.as_bytes()).await?;
    let mut status = [0; 13];
    stream.read_exact(&mut status).await?;
    if !(status.starts_with(b"HTTP/1.0 ") || status.starts_with(b"HTTP/1.1 "))
        || !matches!(status[9], b'2' | b'3')
        || !status[10..12].iter().all(u8::is_ascii_digit)
    {
        return Err(io::Error::other(
            "health probe returned unsuccessful HTTP status",
        ));
    }
    Ok(())
}
impl OutboundDialer for TestedGroup {
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move { self.members[self.choose().await?].dial_tcp(target).await })
    }
}
pub(crate) struct Reject;
impl OutboundDialer for Reject {
    fn dial_tcp<'a>(&'a self, _: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async { Err(io::Error::new(io::ErrorKind::PermissionDenied, "REJECT")) })
    }
}
