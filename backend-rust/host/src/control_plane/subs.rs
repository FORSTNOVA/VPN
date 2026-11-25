use super::state::{QuotaInfo, SavedSubscription};
use base64::Engine;
use http_body_util::{BodyExt, Empty, Limited};
use hyper::{body::Bytes, Request};
use hyper_util::rt::TokioIo;
use serde_json::{json, Value};
use std::{collections::HashMap, net::SocketAddr, sync::Arc, time::Duration};
use tokio::time::timeout;
use tokio_rustls::{
    rustls::{pki_types::ServerName, ClientConfig, RootCertStore},
    TlsConnector,
};

pub fn parse_userinfo_header(header: &str) -> Option<QuotaInfo> {
    let mut upload = 0_i64;
    let mut download = 0_i64;
    let mut total = 0_i64;
    let mut expire = 0_i64;
    let mut seen = false;
    for pair in header.split(';') {
        let Some((k, v)) = pair.split_once('=') else {
            continue;
        };
        let Ok(val) = v.trim().parse::<i64>() else {
            continue;
        };
        seen = true;
        match k.trim().to_ascii_lowercase().as_str() {
            "upload" => upload = val,
            "download" => download = val,
            "total" => total = val,
            "expire" => expire = val,
            _ => {}
        }
    }
    seen.then_some(QuotaInfo {
        upload,
        download,
        total,
        expire,
    })
}

pub async fn fetch_subscription_text(
    url: &str,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
) -> Result<(String, Option<QuotaInfo>), String> {
    let (bytes, quota) = fetch_bytes(url, protect, 16 << 20, false).await?;
    Ok((String::from_utf8_lossy(&bytes).into_owned(), quota))
}

pub(super) async fn fetch_https_list(
    url: &str,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    limit: usize,
) -> Result<String, String> {
    let (bytes, _) = fetch_bytes(url, protect, limit, true).await?;
    String::from_utf8(bytes.to_vec()).map_err(|e| e.to_string())
}

async fn fetch_bytes(
    url: &str,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    limit: usize,
    https_only: bool,
) -> Result<(Bytes, Option<QuotaInfo>), String> {
    let mut current = url.trim().to_owned();
    let mut last_user_info = None;
    for _hop in 0..6 {
        if https_only && !current.starts_with("https://") {
            return Err("list download requires HTTPS".into());
        }
        let (status, location, user_info, body_bytes) =
            fetch_once(&current, protect, limit).await?;
        if user_info.is_some() {
            last_user_info = user_info;
        }
        if matches!(status, 301 | 302 | 303 | 307 | 308) {
            if let Some(loc) = location {
                current = resolve_redirect(&current, &loc)?;
                continue;
            }
        }
        if !(200..300).contains(&status) {
            return Err(format!("HTTP {status}"));
        }
        return Ok((body_bytes, last_user_info));
    }
    Err("too many redirects".to_owned())
}

fn resolve_redirect(base: &str, location: &str) -> Result<String, String> {
    let loc = location.trim();
    if loc.starts_with("http://") || loc.starts_with("https://") {
        return Ok(loc.to_owned());
    }
    let (scheme, rest) = base
        .split_once("://")
        .ok_or_else(|| "invalid redirect base URL".to_owned())?;
    let authority = rest.split('/').next().unwrap_or(rest);
    if loc.starts_with('/') {
        Ok(format!("{scheme}://{authority}{loc}"))
    } else {
        Ok(format!("{scheme}://{authority}/{loc}"))
    }
}

async fn fetch_once(
    url: &str,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    limit: usize,
) -> Result<(u16, Option<String>, Option<QuotaInfo>, Bytes), String> {
    let trimmed = url.trim();
    let (scheme, rest) = trimmed
        .split_once("://")
        .ok_or_else(|| "invalid subscription URL".to_owned())?;
    if scheme != "http" && scheme != "https" {
        return Err("invalid subscription URL".to_owned());
    }
    let (authority, path_and_query) = match rest.find('/') {
        Some(idx) => (&rest[..idx], &rest[idx..]),
        None => (rest, "/"),
    };
    let (host, port) = if let Some((h, p)) = authority.rsplit_once(':') {
        if let Ok(port_num) = p.parse::<u16>() {
            (h.to_owned(), port_num)
        } else {
            (
                authority.to_owned(),
                if scheme == "https" { 443 } else { 80 },
            )
        }
    } else {
        (
            authority.to_owned(),
            if scheme == "https" { 443 } else { 80 },
        )
    };

    let mut addrs: Vec<SocketAddr> = tokio::net::lookup_host((host.as_str(), port))
        .await
        .map_err(|e| format!("DNS lookup failed: {e}"))?
        .collect();
    if addrs.is_empty() {
        return Err("no address for host".to_owned());
    }
    addrs.sort_by_key(|a| if a.is_ipv4() { 0 } else { 1 });

    let mut last_err = String::from("connect failed");
    if scheme == "https" {
        smartvpn_engine::transport::tls::ensure_crypto_provider();
    }

    let mut roots = RootCertStore::empty();
    roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
    let mut cfg = ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    cfg.alpn_protocols = vec![b"http/1.1".to_vec()];
    let tls_cfg = Arc::new(cfg);
    for addr in addrs.into_iter().take(8) {
        let tcp_res = timeout(Duration::from_secs(3), async {
            let sock = if addr.is_ipv4() {
                tokio::net::TcpSocket::new_v4()
            } else {
                tokio::net::TcpSocket::new_v6()
            }
            .map_err(|e| e.to_string())?;
            #[cfg(unix)]
            if let Some(hook) = protect {
                use std::os::fd::AsRawFd;
                let rc = hook(sock.as_raw_fd());
                if rc <= 0 {
                    return Err(format!("protect socket failed: {rc}"));
                }
            }
            #[cfg(not(unix))]
            let _ = protect;
            sock.connect(addr).await.map_err(|e| e.to_string())
        })
        .await;
        let std_stream = match tcp_res {
            Ok(Ok(s)) => s,
            Ok(Err(e)) => {
                last_err = e;
                continue;
            }
            Err(_) => {
                last_err = "connect timeout".to_owned();
                continue;
            }
        };

        let req = Request::get(path_and_query)
            .header("Host", authority)
            .header("User-Agent", "clash-verge/v2.0.0")
            .header("Connection", "close")
            .body(Empty::<Bytes>::new())
            .map_err(|e| e.to_string())?;

        if scheme == "https" {
            let connector = TlsConnector::from(Arc::clone(&tls_cfg));
            let server_name = ServerName::try_from(host.clone())
                .map_err(|_| "invalid TLS server name".to_owned())?;
            let tls = match timeout(
                Duration::from_secs(6),
                connector.connect(server_name, std_stream),
            )
            .await
            {
                Ok(Ok(t)) => t,
                Ok(Err(e)) => {
                    last_err = format!("TLS handshake failed: {e}");
                    continue;
                }
                Err(_) => {
                    last_err = "TLS handshake timeout".to_owned();
                    continue;
                }
            };
            return send_http1(tls, req, limit).await;
        } else {
            return send_http1(std_stream, req, limit).await;
        }
    }
    Err(last_err)
}

async fn send_http1<S>(
    stream: S,
    req: Request<Empty<Bytes>>,
    limit: usize,
) -> Result<(u16, Option<String>, Option<QuotaInfo>, Bytes), String>
where
    S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send + 'static,
{
    let (mut tx, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
        .await
        .map_err(|e| e.to_string())?;
    tokio::spawn(async move {
        let _ = conn.await;
    });
    let res = timeout(Duration::from_secs(12), tx.send_request(req))
        .await
        .map_err(|_| "HTTP response timeout".to_owned())?
        .map_err(|e| e.to_string())?;
    let status = res.status().as_u16();
    let location = res
        .headers()
        .get("location")
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);
    let user_info = res
        .headers()
        .get("subscription-userinfo")
        .and_then(|v| v.to_str().ok())
        .and_then(parse_userinfo_header);
    let body = Limited::new(res.into_body(), limit)
        .collect()
        .await
        .map_err(|e| e.to_string())?
        .to_bytes();
    Ok((status, location, user_info, body))
}

pub fn parse_catalog(payload: &str) -> Result<(Vec<Value>, Vec<String>), String> {
    let nodes = parse_proxies_from_payload(payload)?;
    let rules = match serde_yaml_ng::from_str::<Value>(payload) {
        Ok(doc) if doc.get("rules").is_some() => serde_json::from_value(doc["rules"].clone())
            .map_err(|e| format!("invalid subscription rules: {e}"))?,
        _ => Vec::new(), // URI lists have no rules section.
    };
    Ok((nodes, rules))
}

pub fn active_rules(state: &super::state::PersistedState) -> impl Iterator<Item = &String> {
    state
        .subscriptions
        .iter()
        .filter(move |s| {
            if state.subscription_url == "__merged__" {
                s.enabled
            } else {
                s.url == state.subscription_url
            }
        })
        .flat_map(|s| &s.rules)
}

pub fn applied_rule(settings: &super::state::PersistedState, source: &str) -> Option<String> {
    let parts: Vec<_> = source.split(',').map(str::trim).collect();
    let [kind @ ("DOMAIN" | "DOMAIN-SUFFIX" | "DOMAIN-KEYWORD" | "IP-CIDR" | "IP-CIDR6"), value, target, rest @ ..] =
        parts.as_slice()
    else {
        return None;
    };
    if !rest.is_empty() && rest != ["no-resolve"] {
        return None;
    }
    let action = match *target {
        "DIRECT"
            if !settings.domestic_web
                && (settings.dns_protection || settings.webrtc_protection) =>
        {
            return None
        }
        "DIRECT" => "DIRECT",
        "REJECT" | "REJECT-DROP" => "REJECT",
        "" => return None,
        _ => "SmartVPN",
    };
    let line = format!("{kind},{value},{action}");
    smartvpn_engine::rule::RuleSet::from_lines(&[&line])
        .ok()
        .map(|_| line)
}

pub fn parse_proxies_from_payload(payload: &str) -> Result<Vec<Value>, String> {
    let trimmed = payload.trim();
    if trimmed.is_empty() {
        return Err("empty subscription payload".to_owned());
    }
    // 1. Try Mihomo YAML (`proxies:` list)
    if let Ok(yaml_val) = serde_yaml_ng::from_str::<serde_yaml_ng::Value>(trimmed) {
        if let Some(proxies) = yaml_val.get("proxies").and_then(|v| v.as_sequence()) {
            let mut out = Vec::new();
            for p in proxies {
                if let Ok(json_v) = serde_json::to_value(p) {
                    if json_v.get("name").and_then(|n| n.as_str()).is_some() {
                        out.push(json_v);
                    }
                }
            }
            if !out.is_empty() {
                return Ok(out);
            }
        }
    }
    // 2. Try Base64 decode -> URI lines
    let decoded = if trimmed.contains("://") {
        trimmed.to_owned()
    } else if let Ok(bytes) = base64::engine::general_purpose::STANDARD
        .decode(trimmed.replace(['\r', '\n', ' '], ""))
        .or_else(|_| {
            base64::engine::general_purpose::URL_SAFE_NO_PAD
                .decode(trimmed.replace(['\r', '\n', ' ', '='], ""))
        })
    {
        String::from_utf8_lossy(&bytes).into_owned()
    } else {
        trimmed.to_owned()
    };

    let mut out = Vec::new();
    for line in decoded.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        if let Some(proxy) = parse_proxy_uri(line) {
            out.push(proxy);
        }
    }
    if out.is_empty() {
        return Err("no supported proxy nodes found in subscription".to_owned());
    }
    Ok(out)
}

pub fn parse_proxy_uri(uri: &str) -> Option<Value> {
    let (scheme, rest) = uri.split_once("://")?;
    let (main, fragment) = rest.split_once('#').unwrap_or((rest, ""));
    let raw_name = percent_decode(fragment);
    match scheme.to_ascii_lowercase().as_str() {
        "vless" | "trojan" | "anytls" => {
            let (creds, host_q) = main.split_once('@')?;
            let (host_port, query) = host_q.split_once('?').unwrap_or((host_q, ""));
            let (server, port) = split_host_port(host_port)?;
            let params = parse_query(query);
            let name = if raw_name.is_empty() {
                format!("{scheme}-{server}:{port}")
            } else {
                raw_name
            };
            let mut map = serde_json::Map::new();
            map.insert("name".into(), json!(name));
            map.insert("type".into(), json!(scheme.to_ascii_lowercase()));
            map.insert("server".into(), json!(server));
            map.insert("port".into(), json!(port));
            if scheme.eq_ignore_ascii_case("vless") {
                map.insert("uuid".into(), json!(creds));
            } else {
                map.insert("password".into(), json!(creds));
            }
            if let Some(sni) = params.get("sni").or_else(|| params.get("peer")) {
                if !sni.is_empty() {
                    map.insert("servername".into(), json!(sni));
                    map.insert("sni".into(), json!(sni));
                }
            }
            if params
                .get("allowInsecure")
                .is_some_and(|v| v == "1" || v == "true")
            {
                map.insert("skip-cert-verify".into(), json!(true));
            }
            if params
                .get("security")
                .is_some_and(|v| v == "tls" || v == "reality")
            {
                map.insert("tls".into(), json!(true));
            }
            if let Some(pbk) = params.get("pbk") {
                map.insert(
                    "reality-opts".into(),
                    json!({
                        "public-key": pbk,
                        "short-id": params.get("sid").cloned().unwrap_or_default()
                    }),
                );
            }
            if let Some(net) = params.get("type") {
                map.insert("network".into(), json!(net));
                if net == "ws" {
                    let mut ws = serde_json::Map::new();
                    if let Some(p) = params.get("path") {
                        ws.insert("path".into(), json!(p));
                    }
                    if let Some(h) = params.get("host") {
                        ws.insert("headers".into(), json!({"Host": h}));
                    }
                    map.insert("ws-opts".into(), Value::Object(ws));
                } else if net == "grpc" {
                    if let Some(sn) = params.get("serviceName") {
                        map.insert("grpc-opts".into(), json!({"grpc-service-name": sn}));
                    }
                }
            }
            Some(Value::Object(map))
        }
        "hysteria2" | "hy2" => {
            let (password, host_q) = main.split_once('@')?;
            let (host_port, query) = host_q.split_once('?').unwrap_or((host_q, ""));
            let (server, port) = split_host_port(host_port)?;
            let params = parse_query(query);
            let name = if raw_name.is_empty() {
                format!("hy2-{server}:{port}")
            } else {
                raw_name
            };
            let mut map = serde_json::Map::new();
            map.insert("name".into(), json!(name));
            map.insert("type".into(), json!("hysteria2"));
            map.insert("server".into(), json!(server));
            map.insert("port".into(), json!(port));
            map.insert("password".into(), json!(password));
            if let Some(sni) = params.get("sni") {
                map.insert("sni".into(), json!(sni));
            }
            if params
                .get("insecure")
                .is_some_and(|v| v == "1" || v == "true")
            {
                map.insert("skip-cert-verify".into(), json!(true));
            }
            Some(Value::Object(map))
        }
        "ss" => {
            let (creds, host_q) = main.split_once('@')?;
            let host_port = host_q.split('?').next().unwrap_or(host_q);
            let (server, port) = split_host_port(host_port)?;
            let decoded_creds = base64::engine::general_purpose::STANDARD
                .decode(creds)
                .or_else(|_| base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(creds))
                .ok()
                .and_then(|b| String::from_utf8(b).ok())
                .unwrap_or_else(|| creds.to_owned());
            let (cipher, password) = decoded_creds.split_once(':')?;
            let name = if raw_name.is_empty() {
                format!("ss-{server}:{port}")
            } else {
                raw_name
            };
            Some(json!({
                "name": name,
                "type": "ss",
                "server": server,
                "port": port,
                "cipher": cipher,
                "password": password,
            }))
        }
        _ => None,
    }
}

fn split_host_port(s: &str) -> Option<(String, u16)> {
    if let Some(rest) = s.strip_prefix('[') {
        let (h, p) = rest.split_once("]:")?;
        return Some((h.to_owned(), p.parse().ok()?));
    }
    let (h, p) = s.rsplit_once(':')?;
    Some((h.to_owned(), p.parse().ok()?))
}

fn parse_query(q: &str) -> HashMap<String, String> {
    let mut map = HashMap::new();
    for pair in q.split('&') {
        if let Some((k, v)) = pair.split_once('=') {
            map.insert(percent_decode(k), percent_decode(v));
        }
    }
    map
}

fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let Ok(val) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(val);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

pub fn merge_enabled_subscriptions(subs: &[SavedSubscription]) -> Vec<Value> {
    let mut merged = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for sub in subs.iter().filter(|s| s.enabled) {
        for p in &sub.raw_proxies {
            let Some(name) = p.get("name").and_then(|n| n.as_str()) else {
                continue;
            };
            let unique = if seen.insert(name.to_owned()) {
                name.to_owned()
            } else {
                format!("{name} ({})", sub.label)
            };
            seen.insert(unique.clone());
            let mut item = p.clone();
            if let Some(obj) = item.as_object_mut() {
                obj.insert("name".into(), json!(unique));
            }
            merged.push(item);
        }
    }
    merged
}
#[cfg(test)]
mod rule_tests {
    use super::{applied_rule, parse_catalog};
    use crate::control_plane::state::PersistedState;

    #[test]
    fn validates_rule_list_and_applies_supported_policies() {
        let catalog = "proxies: [{name: example, type: socks5, server: localhost, port: 1080}]\n";
        let (_, rules) = parse_catalog(&format!("{catalog}rules: ['DOMAIN,example.org,DIRECT', 'DOMAIN,blocked.example,REJECT', 'DOMAIN,proxy.example,Group']")).unwrap();
        assert!(parse_catalog(&format!("{catalog}rules: true")).is_err());
        let mut settings = PersistedState::default();
        settings.domestic_web = false;
        settings.dns_protection = true;
        assert_eq!(applied_rule(&settings, &rules[0]), None);
        assert_eq!(applied_rule(&settings, &rules[1]).as_deref(), Some("DOMAIN,blocked.example,REJECT"));
        assert_eq!(applied_rule(&settings, &rules[2]).as_deref(), Some("DOMAIN,proxy.example,SmartVPN"));
        settings.domestic_web = true;
        assert_eq!(applied_rule(&settings, &rules[0]).as_ref(), Some(&rules[0]));
    }
}
