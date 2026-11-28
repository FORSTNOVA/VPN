use crate::{
    dialer::TargetAddr,
    dns::FakeIpPool,
    protocol::{
        anytls::AnyTlsConfig,
        hysteria2::Hysteria2Config,
        ss::{ShadowsocksCipher, ShadowsocksConfig},
        trojan::TrojanConfig,
        vless::{VlessConfig, VlessNetwork, VlessSecurity},
    },
    proxy_group::ProbeConfig,
    router::{build_dialer, NodeSpec, ProcessResolver, Router},
    rule::{RuleAction, RuleSet},
    transport::{grpc::GrpcConfig, reality::RealityConfig, tls::TlsConfig, ws::WsConfig},
};
use serde::Deserialize;
use std::{
    collections::{HashMap, HashSet},
    io,
    net::SocketAddr,
    sync::Arc,
    time::Duration,
};

pub struct EngineYamlBundle {
    pub mixed_port: u16,
    pub router: Arc<Router>,
    pub fake_ip_pool: Arc<FakeIpPool>,
    pub dns_listen: Option<SocketAddr>,
}
#[derive(Deserialize)]
#[serde(rename_all = "kebab-case")]
struct Config {
    mixed_port: u16,
    #[serde(default)]
    smartvpn_china_ranges: Vec<String>,
    #[serde(default)]
    smartvpn_block_stun: bool,
    #[serde(default)]
    proxies: Vec<serde_yaml_ng::Value>,
    #[serde(default)]
    proxy_groups: Vec<Group>,
    rules: Vec<String>,
    #[serde(default)]
    dns: Dns,
}
#[derive(Default, Deserialize)]
struct Dns {
    #[serde(default)]
    enable: bool,
    listen: Option<SocketAddr>,
}
#[derive(Deserialize)]
#[serde(rename_all = "kebab-case")]
struct Proxy {
    name: String,
    server: String,
    port: u16,
    dialer_proxy: Option<String>,
    smartvpn_session: Option<String>,
    #[serde(alias = "servername")]
    sni: Option<String>,
    #[serde(default)]
    skip_cert_verify: bool,
    #[serde(flatten)]
    protocol: Protocol,
}
#[derive(Deserialize)]
#[serde(
    tag = "type",
    rename_all = "lowercase",
    rename_all_fields = "kebab-case"
)]
enum Protocol {
    Vmess {
        uuid: String,
        cipher: Option<String>,
        #[serde(default)]
        alter_id: u16,
        #[serde(default)]
        tls: bool,
        network: Option<String>,
        #[serde(default)]
        ws_opts: Ws,
        #[serde(default)]
        grpc_opts: Grpc,
        #[serde(default)]
        alpn: Vec<String>,
    },
    Tuic {
        uuid: String,
        password: String,
        alpn: Option<Vec<String>>,
        congestion_controller: Option<String>,
        udp_relay_mode: Option<String>,
        #[serde(default)]
        disable_sni: bool,
        #[serde(default)]
        reduce_rtt: bool,
        heartbeat_interval: Option<u64>,
        request_timeout: Option<u64>,
    },
    Socks5 {
        username: Option<String>,
        password: Option<String>,
    },
    Ss {
        cipher: String,
        password: String,
    },
    Trojan {
        password: String,
    },
    Anytls {
        password: String,
    },
    Vless {
        uuid: String,
        flow: Option<String>,
        #[serde(default)]
        tls: bool,
        reality_opts: Option<Reality>,
        network: Option<String>,
        #[serde(default)]
        ws_opts: Ws,
        #[serde(default)]
        grpc_opts: Grpc,
    },
    Hysteria2 {
        password: String,
        up: Option<Quantity>,
        down: Option<Quantity>,
        obfs: Option<String>,
        obfs_password: Option<String>,
        ports: Option<Quantity>,
    },
}
#[derive(Deserialize)]
#[serde(rename_all = "kebab-case")]
struct Reality {
    public_key: String,
    #[serde(default)]
    short_id: String,
}
#[derive(Default, Deserialize)]
struct Ws {
    path: Option<String>,
    #[serde(default)]
    headers: HashMap<String, String>,
}
#[derive(Default, Deserialize)]
#[serde(rename_all = "kebab-case")]
struct Grpc {
    grpc_service_name: Option<String>,
    #[serde(alias = "grpc-authority")]
    authority: Option<String>,
}
#[derive(Deserialize)]
#[serde(rename_all = "kebab-case")]
struct Group {
    name: String,
    #[serde(rename = "type")]
    kind: GroupKind,
    #[serde(default)]
    proxies: Vec<String>,
    url: Option<String>,
    interval: Option<u64>,
    hop_timeout: Option<u64>,
}
#[derive(Clone, Copy, Deserialize)]
#[serde(rename_all = "kebab-case")]
enum GroupKind {
    OrderedFailover,
    Select,
    UrlTest,
    Fallback,
    Relay,
}
fn invalid(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, msg.into())
}

#[derive(Deserialize)]
#[serde(untagged)]
enum Quantity {
    Number(u64),
    Text(String),
}
impl Quantity {
    fn text(self) -> String {
        match self {
            Self::Number(n) => n.to_string(),
            Self::Text(s) => s,
        }
    }
}
fn bandwidth(value: Option<Quantity>) -> io::Result<u64> {
    let Some(value) = value else { return Ok(0) };
    let value = value.text();
    let lower = value.trim().to_ascii_lowercase();
    lower
        .strip_suffix("mbps")
        .unwrap_or(&lower)
        .trim()
        .parse()
        .map_err(|_| invalid(format!("invalid bandwidth in Mbps: {value}")))
}
fn ports(value: Option<Quantity>) -> io::Result<Vec<u16>> {
    let mut result = Vec::new();
    if let Some(value) = value {
        for range in value.text().split(',').map(str::trim) {
            let port = |s: &str| {
                s.trim()
                    .parse::<u16>()
                    .ok()
                    .filter(|n| *n != 0)
                    .ok_or_else(|| invalid(format!("invalid hop port: {s}")))
            };
            let (first, last) = range
                .split_once('-')
                .or_else(|| range.split_once(':'))
                .unwrap_or((range, range));
            let (a, b) = (port(first)?, port(last)?);
            if a > b {
                return Err(invalid(format!("reversed port range: {range}")));
            }
            result.extend(a..=b);
        }
    }
    Ok(result)
}
fn stream_transport(
    network: Option<String>,
    ws_opts: Ws,
    grpc_opts: Grpc,
) -> io::Result<VlessNetwork> {
    Ok(match network.as_deref().unwrap_or("tcp") {
        "tcp" => VlessNetwork::Tcp,
        "ws" => {
            let mut ws = WsConfig::default();
            if let Some(path) = ws_opts.path {
                if !path.starts_with('/') {
                    return Err(invalid("ws path must start with /"));
                }
                ws.path = path;
            }
            let mut headers = http::HeaderMap::new();
            for (key, value) in ws_opts.headers {
                let key = http::header::HeaderName::try_from(key).map_err(io::Error::other)?;
                let header = http::HeaderValue::from_str(&value).map_err(io::Error::other)?;
                if key == http::header::HOST {
                    ws.host = Some(value);
                } else {
                    headers.insert(key, header);
                }
            }
            VlessNetwork::WsHeaders(ws, headers)
        }
        "grpc" => {
            let mut grpc = GrpcConfig::default();
            if let Some(name) = grpc_opts.grpc_service_name {
                grpc.service_name = name;
            }
            grpc.authority = grpc_opts.authority;
            VlessNetwork::Grpc(grpc)
        }
        other => return Err(invalid(format!("unsupported stream transport {other}"))),
    })
}

impl Proxy {
    fn into_spec(self) -> io::Result<NodeSpec> {
        if self.server.is_empty() || self.port == 0 {
            return Err(invalid("invalid proxy endpoint"));
        }
        let server = match self.server.parse() {
            Ok(ip) => TargetAddr::Ip(SocketAddr::new(ip, self.port)),
            Err(_) => TargetAddr::domain(&self.server, self.port),
        };
        let mut tls = TlsConfig {
            sni: self.sni,
            insecure: self.skip_cert_verify,
            ..Default::default()
        };
        Ok(match self.protocol {
            Protocol::Vmess {
                uuid,
                cipher,
                alter_id,
                tls: enabled,
                network,
                ws_opts,
                grpc_opts,
                alpn,
            } => {
                use marmo_proto::{vless::Uuid, vmess::Security};
                if alter_id != 0 {
                    return Err(invalid("VMess AEAD requires alterId 0"));
                }
                let security = match cipher.as_deref().unwrap_or("auto") {
                    "auto" | "aes-128-gcm" => Security::Aes128Gcm,
                    "chacha20-poly1305" => Security::ChaCha20Poly1305,
                    _ => return Err(invalid("unsupported VMess body cipher")),
                };
                tls.alpn = alpn.into_iter().map(String::into_bytes).collect();
                if tls.alpn.is_empty() && network.as_deref() == Some("grpc") {
                    tls.alpn.push(b"h2".to_vec());
                }
                NodeSpec::Vmess(crate::protocol::vmess::Config {
                    server,
                    uuid: Uuid::parse(&uuid).map_err(io::Error::other)?,
                    security,
                    tls: enabled.then_some(tls),
                    network: stream_transport(network, ws_opts, grpc_opts)?,
                })
            }
            Protocol::Tuic {
                uuid,
                password,
                alpn,
                congestion_controller,
                udp_relay_mode,
                disable_sni,
                reduce_rtt,
                heartbeat_interval,
                request_timeout,
            } => {
                use wind_tuic::quinn::UdpRelayMode;
                tls.alpn = alpn
                    .unwrap_or_else(|| vec!["h3".into()])
                    .into_iter()
                    .map(String::into_bytes)
                    .collect();
                let relay = match udp_relay_mode.as_deref().unwrap_or("native") {
                    "native" => UdpRelayMode::Native,
                    "quic" => UdpRelayMode::Quic,
                    _ => return Err(invalid("invalid TUIC UDP relay mode")),
                };
                let heartbeat = Duration::from_millis(heartbeat_interval.unwrap_or(10000));
                let timeout = Duration::from_millis(request_timeout.unwrap_or(10000));
                if heartbeat.is_zero() || timeout.is_zero() || password.is_empty() {
                    return Err(invalid("invalid TUIC timeout or empty password"));
                }
                NodeSpec::Tuic(crate::protocol::tuic::Config {
                    server,
                    uuid: uuid::Uuid::parse_str(&uuid).map_err(io::Error::other)?,
                    password,
                    tls,
                    relay,
                    congestion: congestion_controller
                        .as_deref()
                        .unwrap_or("bbr")
                        .parse()
                        .map_err(invalid)?,
                    disable_sni,
                    reduce_rtt,
                    heartbeat,
                    timeout,
                })
            }
            Protocol::Socks5 { username, password } => {
                let credentials = match (username, password) {
                    (None, None) => None,
                    (Some(user), Some(pass)) => Some((user, pass)),
                    _ => return Err(invalid("SOCKS5 needs both username and password")),
                };
                NodeSpec::Socks5(server, credentials)
            }
            Protocol::Ss { cipher, password } => NodeSpec::Shadowsocks(ShadowsocksConfig {
                server,
                cipher: ShadowsocksCipher::from_str(&cipher)?,
                password,
            }),
            Protocol::Trojan { password } => NodeSpec::Trojan(TrojanConfig {
                server,
                tls,
                password,
            }),
            Protocol::Anytls { password } => NodeSpec::AnyTls(AnyTlsConfig {
                server,
                tls,
                password,
            }),
            Protocol::Hysteria2 {
                password,
                up,
                down,
                obfs,
                obfs_password,
                ports: hop_ports,
            } => {
                let obfs_salamander = match (obfs.as_deref(), obfs_password) {
                    (None, None) => None,
                    (Some("salamander"), Some(password)) if !password.is_empty() => Some(password),
                    _ => {
                        return Err(invalid(
                            "expected obfs: salamander and a nonempty obfs-password",
                        ))
                    }
                };
                NodeSpec::Hysteria2(Hysteria2Config {
                    server,
                    password,
                    sni: tls.sni,
                    insecure: tls.insecure,
                    keepalive: false,
                    up_mbps: bandwidth(up)?,
                    down_mbps: bandwidth(down)?,
                    obfs_salamander,
                    hop_ports: ports(hop_ports)?,
                })
            }
            Protocol::Vless {
                uuid,
                flow,
                tls: enabled,
                reality_opts,
                network,
                ws_opts,
                grpc_opts,
            } => {
                if network.as_deref() == Some("grpc") {
                    tls.alpn = vec![b"h2".to_vec()];
                }
                let security = match reality_opts {
                    Some(r) => {
                        let mut reality = RealityConfig::from_strings(
                            tls.sni.unwrap_or(self.server),
                            &r.public_key,
                            &r.short_id,
                        )?;
                        reality.alpn = tls.alpn;
                        VlessSecurity::Reality(reality)
                    }
                    None if enabled => VlessSecurity::Tls(tls),
                    None => VlessSecurity::None,
                };
                let network = stream_transport(network, ws_opts, grpc_opts)?;
                if flow
                    .as_deref()
                    .is_some_and(|f| !matches!(f, "" | "xtls-rprx-vision"))
                {
                    return Err(invalid("unsupported VLESS flow"));
                }
                NodeSpec::Vless(VlessConfig {
                    server,
                    uuid: VlessConfig::parse_uuid(&uuid)?,
                    flow,
                    security,
                    network,
                })
            }
        })
    }
}

enum Definition {
    Node(NodeSpec, Option<String>),
    Group(Group),
}
fn resolve(
    name: &str,
    defs: &HashMap<String, Definition>,
    visiting: &mut HashSet<String>,
    cache: &mut HashMap<String, Option<NodeSpec>>,
) -> io::Result<Option<NodeSpec>> {
    match name {
        "DIRECT" => return Ok(Some(NodeSpec::Direct)),
        "REJECT" => return Ok(Some(NodeSpec::Reject)),
        _ => {}
    }
    if let Some(spec) = cache.get(name) {
        return Ok(spec.clone());
    }
    if !visiting.insert(name.into()) {
        return Err(invalid(format!("cyclic outbound reference at {name}")));
    }
    let spec = match defs.get(name) {
        None => None,
        Some(Definition::Node(node, None)) => Some(node.clone()),
        Some(Definition::Node(node, Some(parent))) => resolve(parent, defs, visiting, cache)?
            .map(|hop| NodeSpec::Chain(vec![hop, node.clone()])),
        Some(Definition::Group(g)) => {
            let members = g
                .proxies
                .iter()
                .map(|m| resolve(m, defs, visiting, cache))
                .collect::<io::Result<Vec<_>>>()?;
            let incomplete_chain =
                matches!(g.kind, GroupKind::Relay) && members.iter().any(Option::is_none);
            let members: Vec<_> = members.into_iter().flatten().collect();
            if members.is_empty() || incomplete_chain {
                None
            } else {
                Some(match g.kind {
                    GroupKind::OrderedFailover => {
                        let url = g
                            .url
                            .as_deref()
                            .unwrap_or("https://www.gstatic.com/generate_204")
                            .parse::<http::Uri>()
                            .map_err(|e| invalid(e.to_string()))?;
                        if !matches!(url.scheme_str(), Some("http" | "https"))
                            || url.host().is_none()
                        {
                            return Err(invalid("invalid hop health-check URL"));
                        }
                        NodeSpec::OrderedFailover {
                            members,
                            url,
                            budget: Duration::from_secs(g.hop_timeout.unwrap_or(6)),
                        }
                    }
                    GroupKind::Select => members.into_iter().next().unwrap(),
                    GroupKind::Relay => NodeSpec::Chain(members),
                    GroupKind::UrlTest | GroupKind::Fallback => {
                        let url = g
                            .url
                            .as_deref()
                            .unwrap_or("https://www.gstatic.com/generate_204")
                            .parse::<http::Uri>()
                            .map_err(|e| invalid(e.to_string()))?;
                        if !matches!(url.scheme_str(), Some("http" | "https"))
                            || url.host().is_none()
                        {
                            return Err(invalid("health check requires an HTTP(S) URL"));
                        }
                        NodeSpec::Tested(
                            ProbeConfig {
                                url,
                                interval: Duration::from_secs(g.interval.unwrap_or(300)),
                                fastest: matches!(g.kind, GroupKind::UrlTest),
                            },
                            members,
                        )
                    }
                })
            }
        }
    };
    visiting.remove(name);
    cache.insert(name.into(), spec.clone());
    Ok(spec)
}

pub fn build_router_from_clash_yaml(
    yaml: &str,
    process_resolver: Option<Arc<dyn ProcessResolver>>,
) -> io::Result<EngineYamlBundle> {
    let config: Config = serde_yaml_ng::from_str(yaml).map_err(|e| invalid(e.to_string()))?;
    let mut defs = HashMap::new();
    let mut insert = |name: String, def| {
        if name.trim().is_empty()
            || matches!(name.as_str(), "DIRECT" | "REJECT")
            || defs.insert(name.clone(), def).is_some()
        {
            return Err(invalid(format!(
                "invalid or duplicate outbound name {name}"
            )));
        }
        Ok(())
    };
    for row in config.proxies {
        // Providers can mix protocols and incomplete rows. Keep only nodes the
        // engine can construct, before resolving group and dialer-proxy edges.
        let Ok(mut proxy) = serde_yaml_ng::from_value::<Proxy>(row) else {
            continue;
        };
        if proxy.name.trim().is_empty() {
            continue;
        }
        let name = std::mem::take(&mut proxy.name);
        let parent = proxy.dialer_proxy.take();
        if let Some(mode) = proxy.smartvpn_session.take() {
            let spec = match (mode.as_str(), proxy.into_spec()?) {
                ("mux", node @ (NodeSpec::Vmess(_) | NodeSpec::Vless(_) | NodeSpec::Shadowsocks(_) | NodeSpec::Trojan(_))) => NodeSpec::Mux(Box::new(node)),
                ("keepalive", NodeSpec::Hysteria2(mut cfg)) => { cfg.keepalive = true; NodeSpec::Hysteria2(cfg) }
                ("keepalive", NodeSpec::Tuic(mut cfg)) => { cfg.heartbeat = cfg.heartbeat.min(Duration::from_secs(3)); NodeSpec::Tuic(cfg) }
                _ => return Err(invalid(format!("unsupported session mode {mode} for {name}"))),
            };
            insert(name, Definition::Node(spec, parent))?;
        } else if let Ok(spec) = proxy.into_spec() {
            insert(name, Definition::Node(spec, parent))?;
        }
    }
    for g in config.proxy_groups {
        insert(g.name.clone(), Definition::Group(g))?;
    }
    let mut specs = HashMap::new();
    let mut visiting = HashSet::new();
    for name in defs.keys() {
        resolve(name, &defs, &mut visiting, &mut specs)?;
    }
    let mut rules = RuleSet::from_clash_lines(
        &config.rules.iter().map(String::as_str).collect::<Vec<_>>(),
        crate::domestic::Ranges::new(&config.smartvpn_china_ranges)?,
    )?;
    rules.resolve_actions(|name| match specs.get(name) {
        Some(Some(NodeSpec::Direct)) => Ok(RuleAction::Direct),
        Some(Some(NodeSpec::Reject)) => Ok(RuleAction::Reject),
        Some(None) => Err(invalid(format!("no available outbound for {name}"))),
        Some(Some(_)) => Ok(RuleAction::Proxy(name.into())),
        None => Err(invalid(format!("unknown rule outbound {name}"))),
    })?;
    let mut outbounds = HashMap::new();
    for (name, spec) in specs {
        let Some(spec) = spec else { continue };
        outbounds.insert(
            name.clone(),
            build_dialer(&spec).map_err(|e| invalid(format!("outbound {name}: {e}")))?,
        );
    }
    let fake_ip_pool = Arc::new(FakeIpPool::default());
    let mut router = Router::new(rules, outbounds);
    router.block_stun = config.smartvpn_block_stun;
    if config.dns.enable && !config.smartvpn_china_ranges.is_empty() {
        router.classification_proxy = Some(
            router
                .outbounds
                .get("SmartVPN")
                .ok_or_else(|| invalid("missing DNS proxy for domestic classification"))?
                .clone(),
        );
    }
    router.fake_ip_pool = Some(fake_ip_pool.clone());
    router.process_resolver = process_resolver;
    Ok(EngineYamlBundle {
        mixed_port: config.mixed_port,
        router: Arc::new(router),
        fake_ip_pool,
        dns_listen: if config.dns.enable {
            config.dns.listen
        } else {
            None
        },
    })
}
