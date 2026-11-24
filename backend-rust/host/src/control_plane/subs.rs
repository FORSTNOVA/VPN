use super::state::{QuotaInfo, SavedSubscription};
use base64::Engine;
use http_body_util::{BodyExt, Empty, Limited};
use hyper::{body::Bytes, Request};
use hyper_util::rt::TokioIo;
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    net::SocketAddr,
    sync::Arc,
    time::Duration,
};
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
            (authority.to_owned(), if scheme == "https" { 443 } else { 80 })
        }
    } else {
        (authority.to_owned(), if scheme == "https" { 443 } else { 80 })
    };

    let addrs: Vec<SocketAddr> = tokio::net::lookup_host((host.as_str(), port))
        .await
        .map_err(|e| format!("DNS lookup failed: {e}"))?
        .collect();
    let addr = *addrs.first().ok_or_else(|| "no address for host".to_owned())?;

    let std_stream = timeout(Duration::from_secs(12), async {
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
    .await
    .map_err(|_| "connect timeout".to_owned())??;

    let req = Request::get(path_and_query)
        .header("Host", authority)
        .header("User-Agent", "clash-verge/v2.0.0")
        .header("Connection", "close")
        .body(Empty::<Bytes>::new())
        .map_err(|e| e.to_string())?;

    let (status, user_info, body_bytes) = if scheme == "https" {
        let mut roots = RootCertStore::empty();
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
        let cfg = ClientConfig::builder()
            .with_root_certificates(roots)
            .with_no_client_auth();
        let connector = TlsConnector::from(Arc::new(cfg));
        let server_name =
            ServerName::try_from(host.clone()).map_err(|_| "invalid TLS server name".to_owned())?;
        let tls = connector
            .connect(server_name, std_stream)
            .await
            .map_err(|e| format!("TLS handshake failed: {e}"))?;
        send_http1(tls, req).await?
    } else {
        send_http1(std_stream, req).await?
    };

    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status}"));
    }
    let text = String::from_utf8_lossy(&body_bytes).into_owned();
    Ok((text, user_info))
}

async fn send_http1<S>(
    stream: S,
    req: Request<Empty<Bytes>>,
) -> Result<(u16, Option<QuotaInfo>, Bytes), String>
where
    S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send + 'static,
{
    let (mut tx, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
        .await
        .map_err(|e| e.to_string())?;
    tokio::spawn(async move {
        let _ = conn.await;
    });
    let res = timeout(Duration::from_secs(20), tx.send_request(req))
        .await
        .map_err(|_| "HTTP response timeout".to_owned())?
        .map_err(|e| e.to_string())?;
    let status = res.status().as_u16();
    let user_info = res
        .headers()
        .get("subscription-userinfo")
        .and_then(|v| v.to_str().ok())
        .and_then(parse_userinfo_header);
    let body = Limited::new(res.into_body(), 16 << 20)
        .collect()
        .await
        .map_err(|e| e.to_string())?
        .to_bytes();
    Ok((status, user_info, body))
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
            if params.get("allowInsecure").is_some_and(|v| v == "1" || v == "true") {
                map.insert("skip-cert-verify".into(), json!(true));
            }
            if params.get("security").is_some_and(|v| v == "tls" || v == "reality") {
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
            if params.get("insecure").is_some_and(|v| v == "1" || v == "true") {
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
                format!("{name} ({})", sub.name)
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
