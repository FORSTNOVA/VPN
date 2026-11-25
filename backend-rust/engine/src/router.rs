use crate::{
    dialer::{BoxedStream, DirectDialer, OutboundDialer, TargetAddr},
    dns::FakeIpPool,
    protocol::{
        anytls::{AnyTlsConfig, AnyTlsDialer},
        hysteria2::{Hysteria2Config, Hysteria2Dialer},
        ss::{ShadowsocksConfig, ShadowsocksDialer},
        trojan::{TrojanConfig, TrojanDialer},
        vless::{VlessConfig, VlessDialer},
    },
    rule::{RuleAction, RuleContext, RuleSet},
};
use std::{
    collections::HashMap,
    io,
    net::{IpAddr, SocketAddr},
    sync::Arc,
};

#[derive(Clone)]
pub enum NodeSpec {
    Direct,
    Reject,
    Tested(crate::proxy_group::ProbeConfig, Vec<NodeSpec>),
    Shadowsocks(ShadowsocksConfig),
    Trojan(TrojanConfig),
    AnyTls(AnyTlsConfig),
    Vless(VlessConfig),
    Hysteria2(Hysteria2Config),
    Chain(Vec<NodeSpec>),
    OrderedFailover {
        members: Vec<NodeSpec>,
        url: http::Uri,
        budget: std::time::Duration,
    },
}

type Dialer = Arc<dyn OutboundDialer>;
pub fn build_dialer(spec: &NodeSpec) -> io::Result<Dialer> {
    Ok(extend(spec, None)?.unwrap_or_else(|| Arc::new(DirectDialer)))
}

// None represents a direct transport, including an explicit Direct chain member.
fn extend(spec: &NodeSpec, underlay: Option<Dialer>) -> io::Result<Option<Dialer>> {
    let under_tcp = || underlay.clone().unwrap_or_else(|| Arc::new(DirectDialer));
    let dialer: Dialer = match spec {
        NodeSpec::Reject => Arc::new(crate::proxy_group::Reject),
        NodeSpec::Tested(config, nodes) => {
            let members = nodes
                .iter()
                .map(|node| {
                    Ok(extend(node, underlay.clone())?.unwrap_or_else(|| Arc::new(DirectDialer)))
                })
                .collect::<io::Result<_>>()?;
            Arc::new(crate::proxy_group::TestedGroup::new(
                config.clone(),
                members,
            ))
        }
        NodeSpec::OrderedFailover {
            members: nodes,
            url,
            budget,
        } => {
            let members = nodes
                .iter()
                .map(|n| {
                    extend(n, underlay.clone()).map(|d| d.unwrap_or_else(|| Arc::new(DirectDialer)))
                })
                .collect::<io::Result<Vec<_>>>()?;
            if members.len() == 1 {
                return Ok(members.into_iter().next());
            }
            Arc::new(crate::proxy_group::OrderedFailover::new(
                members,
                url.clone(),
                *budget,
            ))
        }
        NodeSpec::Direct => return Ok(underlay),
        NodeSpec::Shadowsocks(c) => {
            Arc::new(ShadowsocksDialer::with_underlay(c.clone(), under_tcp()))
        }
        NodeSpec::Trojan(c) => Arc::new(TrojanDialer::with_underlay(c.clone(), under_tcp())),
        NodeSpec::AnyTls(c) => Arc::new(AnyTlsDialer::with_underlay(c.clone(), under_tcp())),
        NodeSpec::Vless(c) => Arc::new(VlessDialer::with_underlay(c.clone(), under_tcp())),
        NodeSpec::Hysteria2(c) => match underlay {
            Some(u) => {
                if u.udp().is_none() {
                    return Err(io::Error::new(
                        io::ErrorKind::Unsupported,
                        "previous hop does not support UDP relay for Hysteria2",
                    ));
                }
                Arc::new(Hysteria2Dialer::with_underlay(c.clone(), u))
            }
            None => Arc::new(Hysteria2Dialer::new(c.clone())),
        },
        NodeSpec::Chain(hops) => {
            if hops.is_empty() {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "empty outbound chain",
                ));
            }
            return hops
                .iter()
                .try_fold(underlay, |previous, hop| extend(hop, previous));
        }
    };
    Ok(Some(dialer))
}

/// For IP targets dst is the original IP endpoint (including Fake-IP). For a
/// domain request on Mixed it is the local endpoint of the accepted socket.
pub trait ProcessResolver: Send + Sync {
    fn resolve(&self, src: SocketAddr, dst: SocketAddr, is_udp: bool) -> Option<String>;
    fn try_resolve(
        &self,
        src: SocketAddr,
        dst: SocketAddr,
        is_udp: bool,
    ) -> io::Result<Option<String>> {
        Ok(self.resolve(src, dst, is_udp))
    }
}
pub struct Router {
    pub(crate) classification_proxy: Option<Dialer>,
    classification_cache: Arc<std::sync::Mutex<HashMap<String, (IpAddr, std::time::Instant)>>>,
    pub rules: RuleSet,
    pub outbounds: HashMap<String, Dialer>,
    pub fake_ip_pool: Option<Arc<FakeIpPool>>,
    pub process_resolver: Option<Arc<dyn ProcessResolver>>,
}

pub enum RouteResult {
    Connected {
        action: RuleAction,
        stream: BoxedStream,
    },
    Reject,
}
impl Router {
    pub fn new(rules: RuleSet, outbounds: HashMap<String, Dialer>) -> Self {
        Self {
            classification_proxy: None,
            classification_cache: Default::default(),
            rules,
            outbounds,
            fake_ip_pool: None,
            process_resolver: None,
        }
    }
    pub async fn dial_tcp(
        &self,
        src: SocketAddr,
        target: &TargetAddr,
        sniffed_host: Option<&str>,
    ) -> io::Result<RouteResult> {
        self.dial(src, target, sniffed_host, None).await
    }
    pub async fn probe_http_204(
        &self,
        host: &str,
        port: u16,
        budget: std::time::Duration,
    ) -> io::Result<i64> {
        if tokio::runtime::Handle::try_current().is_err() {
            static FALLBACK_RT: std::sync::OnceLock<io::Result<tokio::runtime::Runtime>> =
                std::sync::OnceLock::new();
            let rt = FALLBACK_RT
                .get_or_init(|| {
                    tokio::runtime::Builder::new_multi_thread()
                        .worker_threads(2)
                        .enable_all()
                        .build()
                })
                .as_ref()
                .map_err(|e| io::Error::other(e.to_string()))?;
            let router = Arc::new(self.clone_for_probe());
            let host = host.to_owned();
            return rt
                .spawn(async move { router.probe_http_204_inner(&host, port, budget).await })
                .await
                .map_err(|e| io::Error::other(format!("probe task join error: {e}")))?;
        }
        self.probe_http_204_inner(host, port, budget).await
    }

    fn clone_for_probe(&self) -> Self {
        Self {
            classification_proxy: self.classification_proxy.clone(),
            classification_cache: self.classification_cache.clone(),
            rules: self.rules.clone(),
            outbounds: self.outbounds.clone(),
            fake_ip_pool: self.fake_ip_pool.clone(),
            process_resolver: self.process_resolver.clone(),
        }
    }

    async fn probe_http_204_inner(
        &self,
        host: &str,
        port: u16,
        budget: std::time::Duration,
    ) -> io::Result<i64> {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let t0 = std::time::Instant::now();
        let src = SocketAddr::from(([127, 0, 0, 1], 0));
        let target = TargetAddr::domain(host, port);
        let mut stream = match tokio::time::timeout(budget, self.dial_tcp(src, &target, None))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "connect timeout"))??
        {
            RouteResult::Connected { stream, .. } => stream,
            RouteResult::Reject => {
                return Err(io::Error::new(
                    io::ErrorKind::PermissionDenied,
                    "rejected by rules",
                ))
            }
        };
        let req =
            format!("GET /generate_204 HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n");
        tokio::time::timeout(budget, stream.write_all(req.as_bytes()))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "write timeout"))??;
        tokio::time::timeout(budget, stream.flush())
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "flush timeout"))??;
        let mut buf = [0u8; 64];
        let n = tokio::time::timeout(budget, stream.read(&mut buf))
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "read timeout"))??;
        if n == 0 {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "empty HTTP response",
            ));
        }
        Ok(t0.elapsed().as_millis() as i64)
    }
    pub(crate) async fn dial(
        &self,
        src: SocketAddr,
        target: &TargetAddr,
        sniffed_host: Option<&str>,
        listener: Option<SocketAddr>,
    ) -> io::Result<RouteResult> {
        let (action, destination) = self
            .route(src, target, sniffed_host, listener, false)
            .await?;
        let outbound: &dyn OutboundDialer = match action {
            RuleAction::Reject => return Ok(RouteResult::Reject),
            RuleAction::Direct => &DirectDialer,
            RuleAction::Proxy(tag) => self
                .outbounds
                .get(tag)
                .ok_or_else(|| {
                    io::Error::new(io::ErrorKind::NotFound, format!("unknown outbound {tag}"))
                })?
                .as_ref(),
        };
        let stream = outbound.dial_tcp(&destination).await?;
        Ok(RouteResult::Connected {
            action: action.clone(),
            stream,
        })
    }
    async fn classify_address(&self, name: &str, port: u16) -> io::Result<SocketAddr> {
        if let Some(proxy) = &self.classification_proxy {
            if let Some(&(ip, until)) = self.classification_cache.lock().unwrap().get(name) {
                if until > std::time::Instant::now() {
                    return Ok(SocketAddr::new(ip, port));
                }
            }
            let (ip, ttl) = tokio::time::timeout(
                std::time::Duration::from_secs(12),
                crate::domestic::encrypted_address(proxy.as_ref(), name),
            )
            .await
            .map_err(io::Error::other)??;
            let mut cache = self.classification_cache.lock().unwrap();
            if cache.len() >= 4096 {
                cache.retain(|_, (_, until)| *until > std::time::Instant::now());
            }
            if cache.len() < 4096 {
                cache.insert(
                    name.to_owned(),
                    (
                        ip,
                        std::time::Instant::now() + std::time::Duration::from_secs(ttl.into()),
                    ),
                );
            }
            return Ok(SocketAddr::new(ip, port));
        }
        tokio::time::timeout(
            std::time::Duration::from_secs(5),
            tokio::net::lookup_host((name, port)),
        )
        .await
        .map_err(io::Error::other)??
        .next()
        .ok_or_else(|| io::Error::other(format!("no address for domestic classification: {name}")))
    }
    pub(crate) async fn route(
        &self,
        src: SocketAddr,
        target: &TargetAddr,
        sniffed_host: Option<&str>,
        listener: Option<SocketAddr>,
        is_udp: bool,
    ) -> io::Result<(&RuleAction, TargetAddr)> {
        let ip = match target {
            TargetAddr::Ip(a) => Some(a.ip()),
            _ => None,
        };
        let dst = match target {
            TargetAddr::Ip(a) => Some(*a),
            TargetAddr::Domain(..) => listener,
        };
        let process_name = match (&self.process_resolver, dst) {
            (Some(resolver), Some(dst)) if self.rules.needs_process_name() => {
                resolver.try_resolve(src, dst, is_udp)?
            }
            _ => None,
        };
        let host = match target {
            TargetAddr::Domain(host, _) => Some(host.clone()),
            TargetAddr::Ip(addr) => match &self.fake_ip_pool {
                Some(pool) if pool.is_fake_ip(addr.ip()) => {
                    Some(pool.lookup(addr.ip()).ok_or_else(|| {
                        io::Error::new(
                            io::ErrorKind::NotFound,
                            format!("unassigned Fake-IP {addr}"),
                        )
                    })?)
                }
                _ => sniffed_host.map(str::to_owned),
            },
        };
        let real_ip = ip.filter(|i| !self.fake_ip_pool.as_ref().is_some_and(|p| p.is_fake_ip(*i)));
        let ctx = RuleContext {
            domain: host.as_deref(),
            ip,
            process_name: process_name.as_deref(),
        };
        let candidate = !is_udp
            && !self.rules.china.is_empty()
            && real_ip.map_or(host.is_some(), |ip| self.rules.china.contains(ip));
        let mut selected = self.rules.match_rule(&ctx, candidate);
        let mut direct_addr = None;
        if let Some((crate::rule::Rule::China, _)) = selected {
            let addr = match real_ip {
                Some(ip) => SocketAddr::new(ip, target.port()),
                None => {
                    self.classify_address(
                        host.as_deref().expect("domain classification candidate"),
                        target.port(),
                    )
                    .await?
                }
            };
            if self.rules.china.contains(addr.ip()) {
                direct_addr = Some(addr);
            } else {
                selected = self.rules.match_rule(&ctx, false);
            }
        }
        let action = selected.map(|(_, a)| a).ok_or_else(|| {
            io::Error::new(
                io::ErrorKind::NotFound,
                format!("no matching rule for {target}"),
            )
        })?;
        Ok((
            action,
            match (direct_addr, host) {
                (Some(addr), _) if matches!(action, RuleAction::Direct) => TargetAddr::Ip(addr),
                (_, Some(h)) => TargetAddr::Domain(h, target.port()),
                (_, None) => target.clone(),
            },
        ))
    }
}

#[cfg(test)]
mod domestic_tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[tokio::test]
    async fn domestic_routing_preserves_precedence_and_pins_direct_tcp() {
        let mut rules = RuleSet::from_lines(&[
            "DOMAIN,blocked.invalid,REJECT",
            "GEOIP,CN,DIRECT",
            "MATCH,chain",
        ])
        .unwrap();
        rules.china = Arc::new(
            crate::domestic::Ranges::new(&[
                "127.0.0.0/8".into(),
                "::1/128".into(),
                "2001:db8::/126".into(),
            ])
            .unwrap(),
        );
        let mut router = Router::new(rules, HashMap::new());
        let pool = Arc::new(FakeIpPool::default());
        let fake = pool.allocate("localhost");
        router.fake_ip_pool = Some(pool);
        let src = "127.0.0.1:4321".parse().unwrap();
        for (ip, udp, expected) in [
            ("127.0.0.1", true, RuleAction::Proxy("chain".into())),
            ("2001:db8::3", false, RuleAction::Direct),
            ("2001:db8::4", false, RuleAction::Proxy("chain".into())),
        ] {
            let target = TargetAddr::Ip(SocketAddr::new(ip.parse().unwrap(), 80));
            assert_eq!(
                router.route(src, &target, None, None, udp).await.unwrap().0,
                &expected
            );
        }
        let target = TargetAddr::Ip(SocketAddr::new(fake.into(), 80));
        let (action, dest) = router.route(src, &target, None, None, false).await.unwrap();
        assert_eq!(action, &RuleAction::Direct);
        assert!(matches!(dest,TargetAddr::Ip(addr) if addr.ip().is_loopback()));
        let server = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let target = TargetAddr::Ip(server.local_addr().unwrap());
        assert_eq!(
            router
                .route(src, &target, Some("blocked.invalid"), None, false)
                .await
                .unwrap()
                .0,
            &RuleAction::Reject
        );
        let RouteResult::Connected { action, mut stream } = router
            .dial_tcp(src, &target, Some("unresolvable.invalid"))
            .await
            .unwrap()
        else {
            panic!("expected direct connection")
        };
        assert_eq!(action, RuleAction::Direct);
        stream.write_all(b"domestic").await.unwrap();
        let (mut peer, _) = server.accept().await.unwrap();
        let mut received = [0; 8];
        peer.read_exact(&mut received).await.unwrap();
        assert_eq!(&received, b"domestic");
    }
}
