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

struct HopMember {
    dialer: Dialer,
    // Expiry and outcome of the last check, shared by new flows through this hop.
    health: Mutex<Option<(Instant, bool)>>,
}

pub(crate) struct OrderedFailover {
    members: Vec<HopMember>,
    check_url: http::Uri,
    budget: Duration,
}

impl OrderedFailover {
    pub(crate) fn new(members: Vec<Dialer>, check_url: http::Uri, budget: Duration) -> Self {
        Self {
            members: members
                .into_iter()
                .map(|dialer| HopMember {
                    dialer,
                    health: Mutex::new(None),
                })
                .collect(),
            check_url,
            budget,
        }
    }

    async fn ready(&self, index: usize, member: &HopMember) -> io::Result<()> {
        let mut state = member.health.lock().await;
        if let Some((until, healthy)) = *state {
            if Instant::now() < until {
                return if healthy {
                    Ok(())
                } else {
                    Err(io::Error::other("node is cooling down"))
                };
            }
        }
        let checked = timeout(self.budget, probe(&member.dialer, &self.check_url))
            .await
            .map_err(io::Error::other)
            .and_then(|r| r);
        #[cfg(target_os = "android")]
        if state.as_ref().map(|(_, ok)| *ok) != Some(checked.is_ok()) {
            let result = match &checked {
                Ok(()) => "healthy".to_string(),
                Err(error) => error.to_string(),
            };
            crate::android_network_log(&format!("hop check (budget={}s) member {}: {result}", self.budget.as_secs(), index + 1));
        }
        #[cfg(not(target_os = "android"))]
        let _ = index;
        *state = Some((Instant::now() + Duration::from_secs(30), checked.is_ok()));
        checked
    }

    async fn connect<T>(
        &self,
        mut dial: impl FnMut(&Dialer) -> BoxFuture<'_, io::Result<T>>,
        udp: bool,
    ) -> io::Result<T> {
        let mut errors = Vec::new();
        for (i, member) in self.members.iter().enumerate() {
            if udp && member.dialer.udp().is_none() {
                errors.push(format!("member {}: UDP unsupported", i + 1));
                continue;
            }
            let result = match self.ready(i, member).await {
                Ok(()) => {
                    let result =
                        timeout(self.budget + Duration::from_secs(2), dial(&member.dialer))
                            .await
                            .map_err(io::Error::other)
                            .and_then(|r| r);
                    if let Err(error) = &result {
                        // A destination-specific dial failure does not prove the whole hop is down.
                        // Expire a positive check so the next flow can verify it independently.
                        let mut health = member.health.lock().await;
                        if health.is_some_and(|(_, ok)| ok) {
                            *health = None;
                            #[cfg(target_os = "android")]
                            crate::android_network_log(&format!("hop dial (budget={}s) member {}: {error}", self.budget.as_secs(), i + 1));
                            #[cfg(not(target_os = "android"))]
                            let _ = error;
                        }
                    }
                    result
                }
                Err(e) => Err(e),
            };
            match result {
                Ok(value) => return Ok(value),
                Err(e) => errors.push(format!("member {}: {e}", i + 1)),
            }
        }
        Err(io::Error::other(format!(
            "hop has no available member: {}",
            errors.join(", ")
        )))
    }
}

impl OutboundDialer for OrderedFailover {
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            self.connect(
                |d| {
                    let target = target.clone();
                    Box::pin(async move { d.dial_tcp(&target).await })
                },
                false,
            )
            .await
        })
    }
    fn udp(&self) -> Option<&dyn crate::dialer::OutboundUdpDialer> {
        self.members
            .iter()
            .any(|m| m.dialer.udp().is_some())
            .then_some(self)
    }
}

impl crate::dialer::OutboundUdpDialer for OrderedFailover {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn crate::dialer::UdpPacketSocket>>> {
        Box::pin(async move {
            self.connect(
                |d| d.udp().expect("checked UDP capability").dial_udp(),
                true,
            )
            .await
        })
    }
}

#[cfg(test)]
mod failover_test {
    use super::*;
    use crate::dialer::DirectDialer;
    use tokio::net::TcpListener;

    #[tokio::test]
    async fn refused_destination_does_not_disable_the_member_for_other_targets() {
        let health = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/", health.local_addr().unwrap()).parse().unwrap();
        let target = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let closed = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let closed_addr = closed.local_addr().unwrap();
        drop(closed);
        let respond = tokio::spawn(async move {
            for _ in 0..2 {
                let (mut client, _) = health.accept().await.unwrap();
                let mut request = Vec::new();
                while !request.ends_with(b"\r\n\r\n") {
                    request.push(client.read_u8().await.unwrap());
                }
                client.write_all(b"HTTP/1.1 204 No Content\r\n\r\n").await.unwrap();
            }
        });
        let group = OrderedFailover::new(vec![Arc::new(DirectDialer)], url, Duration::from_secs(2));
        assert!(group.dial_tcp(&TargetAddr::Ip(closed_addr)).await.is_err());
        group.dial_tcp(&TargetAddr::Ip(target.local_addr().unwrap())).await.unwrap();
        respond.await.unwrap();
    }
}
