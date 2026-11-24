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
use std::{collections::HashMap, io, net::SocketAddr, sync::Arc};

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
        NodeSpec::Direct => return Ok(underlay),
        NodeSpec::Shadowsocks(c) => {
            Arc::new(ShadowsocksDialer::with_underlay(c.clone(), under_tcp()))
        }
        NodeSpec::Trojan(c) => Arc::new(TrojanDialer::with_underlay(c.clone(), under_tcp())),
        NodeSpec::AnyTls(c) => Arc::new(AnyTlsDialer::with_underlay(c.clone(), under_tcp())),
        NodeSpec::Vless(c) => Arc::new(VlessDialer::with_underlay(c.clone(), under_tcp())),
        NodeSpec::Hysteria2(c) => {
            if underlay.is_some() {
                return Err(io::Error::new(
                    io::ErrorKind::Unsupported,
                    "Hysteria2 requires a UDP underlay and must be the first proxy hop",
                ));
            }
            Arc::new(Hysteria2Dialer::new(c.clone()))
        }
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
        let req = format!(
            "GET /generate_204 HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n"
        );
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
        let (action, destination) = self.route(src, target, sniffed_host, listener, false)?;
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
    pub(crate) fn route(
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
        let action = self
            .rules
            .evaluate(&RuleContext {
                domain: host.as_deref(),
                ip,
                process_name: process_name.as_deref(),
            })
            .ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::NotFound,
                    format!("no matching rule for {target}"),
                )
            })?;
        Ok((
            action,
            match host {
                Some(h) => TargetAddr::Domain(h, target.port()),
                None => target.clone(),
            },
        ))
    }
}
