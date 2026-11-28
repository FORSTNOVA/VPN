use anyhow::{bail, ensure, Context, Result};
use base64::{engine::general_purpose, Engine};
use percent_encoding::percent_decode_str;
use serde_json::{json, Value};
use url::Url;

pub(crate) fn decode(text: &str) -> Result<String> {
    let normalized = text.replace('-', "+").replace('_', "/");
    Ok(String::from_utf8(
        general_purpose::STANDARD_NO_PAD.decode(normalized.trim_end_matches('='))?,
    )?)
}
fn unescape(s: &str) -> Result<String> {
    Ok(percent_decode_str(s).decode_utf8()?.into_owned())
}
fn bandwidth(v: &Value) -> Result<u64> {
    if let Some(n) = v.as_u64() {
        return Ok(n.max(1));
    }
    let txt = v
        .as_str()
        .context("invalid bandwidth")?
        .trim()
        .to_ascii_lowercase();
    let split = txt.find(|c: char| !c.is_ascii_digit()).unwrap_or(txt.len());
    let n: u64 = txt[..split].parse()?;
    Ok(match txt[split..].trim() {
        "" | "mbps" => n,
        "gbps" => n.checked_mul(1000).context("bandwidth overflow")?,
        "kbps" => n.div_ceil(1000),
        _ => bail!("unknown bandwidth unit"),
    }
    .max(1))
}

pub(crate) fn prepare_for_kernel(node: &mut Value) -> Result<()> {
    let m = node.as_object_mut().context("expected node object")?;
    for (alias, key) in [
        ("upmbps", "up"),
        ("downmbps", "down"),
        ("reserved", "reserved-bits"),
        ("alterId", "alter-id"),
        ("hop_interval", "hop-interval"),
        ("congestion_control", "congestion-controller"),
        ("udp_relay_mode", "udp-relay-mode"),
    ] {
        if let Some(v) = m.remove(alias) {
            m.entry(key).or_insert(v);
        }
    }
    let sni_alias = match m.get("type").and_then(Value::as_str) {
        Some("vmess" | "vless") => Some(("sni", "servername")),
        Some("trojan" | "tuic" | "anytls" | "hysteria2") => Some(("servername", "sni")),
        _ => None,
    };
    if let Some((alias, key)) = sni_alias {
        if let Some(value) = m.remove(alias) {
            ensure!(
                m.get(key).is_none_or(|v| *v == value),
                "conflicting SNI options"
            );
            m.insert(key.into(), value);
        }
    }
    if m.get("type") == Some(&json!("ss")) && m.get("plugin") == Some(&json!("obfs-local")) {
        m.insert("plugin".into(), json!("obfs"));
    }
    if m.get("type") == Some(&json!("hysteria2")) {
        m.entry("skip-cert-verify").or_insert(json!(false));
        for k in ["up", "down"] {
            if let Some(v) = m.get_mut(k) {
                *v = json!(bandwidth(v)?);
            }
        }
    }
    if m.get("type") == Some(&json!("vmess")) {
        m.entry("alter-id").or_insert(json!(0));
        m.entry("cipher").or_insert(json!("auto"));
    }
    for k in [
        "port",
        "alter-id",
        "mtu",
        "heartbeat-interval",
        "request-timeout",
        "max-udp-relay-packet-size",
        "max-open-stream",
        "gc-interval",
        "gc-lifetime",
        "send-window",
        "receive-window",
        "hop-interval",
        "udp-mtu",
        "cwnd",
        "idle-session-check-interval",
        "idle-session-timeout",
        "min-idle-session",
    ] {
        if let Some(s) = m.get(k).and_then(Value::as_str) {
            let s = if matches!(
                k,
                "idle-session-check-interval" | "idle-session-timeout" | "hop-interval"
            ) {
                s.strip_suffix('s').unwrap_or(s)
            } else {
                s
            };
            m.insert(k.into(), json!(s.parse::<u64>()?));
        }
    }
    for k in [
        "udp",
        "tls",
        "skip-cert-verify",
        "remote-dns-resolve",
        "disable-sni",
        "reduce-rtt",
        "fast-open",
        "disable-mtu-discovery",
    ] {
        if let Some(s) = m.get(k).and_then(Value::as_str) {
            let flag = match s.to_ascii_lowercase().as_str() {
                "1" | "true" => true,
                "0" | "false" => false,
                _ => bail!("invalid boolean field {k}"),
            };
            m.insert(k.into(), json!(flag));
        }
    }
    for k in [
        "alpn",
        "dns",
        "allowed-ips",
        "host-key",
        "host-key-algorithms",
        "reserved-bits",
    ] {
        if let Some(s) = m.get(k).and_then(Value::as_str) {
            let parts = s.split(',').map(str::trim);
            let v = if k == "reserved-bits" {
                json!(parts.map(str::parse::<u8>).collect::<Result<Vec<_>, _>>()?)
            } else {
                json!(parts.collect::<Vec<_>>())
            };
            m.insert(k.into(), v);
        }
    }
    for k in ["screw", "step", "digits"] {
        if let Some(s) = node["totp-opt"]["common"][k].as_str() {
            node["totp-opt"]["common"][k] = json!(s.parse::<u64>()?);
        }
    }
    if node["network"] == "httpupgrade" {
        node["network"] = json!("ws");
        node["ws-opts"]["v2ray-http-upgrade"] = json!(true);
    }
    if node["network"] == "ws" && node.get("ws-opts").is_none() {
        node["ws-opts"] = json!({});
    }
    if let Some(headers) = node
        .pointer_mut("/ws-opts/headers")
        .and_then(Value::as_object_mut)
    {
        let keys: Vec<_> = headers
            .keys()
            .filter(|k| k.eq_ignore_ascii_case("host"))
            .cloned()
            .collect();
        ensure!(keys.len() <= 1, "duplicate WS Host header");
        if let Some(key) = keys.first() {
            let host = headers.remove(key).unwrap();
            if !host.as_str().context("invalid WS Host")?.is_empty() {
                headers.insert("Host".into(), host);
            }
        }
    }
    if let Some(s) = node
        .pointer("/ws-opts/max-early-data")
        .and_then(Value::as_str)
    {
        node["ws-opts"]["max-early-data"] = json!(s.parse::<i32>()?);
    }
    if let Some(s) = node.pointer("/h2-opts/host").and_then(Value::as_str) {
        node["h2-opts"]["host"] = json!(s.split(',').map(str::trim).collect::<Vec<_>>());
    }
    if matches!(node["type"].as_str(), Some("vless" | "trojan" | "vmess"))
        && node["network"] == "tcp"
    {
        node.as_object_mut().unwrap().remove("network");
    }
    Ok(())
}

pub(crate) fn ss_plugin(n: &mut Value, spec: &str) -> Result<()> {
    let mut fields = spec.split(';');
    let plugin = fields.next().unwrap();
    n["plugin"] = json!(if matches!(plugin, "simple-obfs" | "obfs-local") {
        "obfs"
    } else {
        plugin
    });
    for field in fields.filter(|s| !s.is_empty()) {
        let (key, value) = field.split_once('=').unwrap_or((field, "true"));
        let key = match key {
            "obfs" => "mode",
            "obfs-host" => "host",
            k => k,
        };
        n["plugin-opts"][key] = match key {
            "tls" | "mux" | "skip-cert-verify" => json!(value.parse::<bool>()?),
            _ => json!(value),
        };
    }
    Ok(())
}

pub fn parse(uri: &str, label: Option<&str>) -> Result<Value> {
    let (scheme, rest) = uri.split_once("://").context("missing URI scheme")?;
    let normalized_scheme = scheme.to_ascii_lowercase();
    let scheme = match normalized_scheme.as_str() {
        "hy2" => "hysteria2",
        "socks" => "socks5",
        s => s,
    };
    let mut node = if scheme == "vmess" {
        let v: Value = serde_json::from_str(&decode(rest)?)?;
        let port = match &v["port"] {
            Value::String(s) => s.parse::<u16>()?,
            x => u16::try_from(x.as_u64().context("invalid VMess port")?)?,
        };
        let tls = match v.get("tls") {
            None => json!(false),
            Some(Value::String(s)) if s == "tls" => json!(true),
            Some(Value::String(s)) if s.is_empty() || s == "none" => json!(false),
            Some(flag) => flag.clone(),
        };
        let mut n = json!({"type":"vmess","server":v["add"].as_str().context("missing VMess server")?,
            "port":port,"uuid":v["id"].as_str().context("missing VMess id")?,"name":v["ps"],"cipher":v.get("scy").cloned().unwrap_or(json!("auto")),"tls":tls});
        if let Some(aid) = v.get("aid") {
            n["alter-id"] = aid.clone();
        }
        if let Some(net) = v["net"].as_str() {
            n["network"] = json!(net);
            if net == "ws" {
                n["ws-opts"] = json!({});
                if let Some(path) = v["path"].as_str().filter(|s| !s.is_empty()) {
                    n["ws-opts"]["path"] = json!(path);
                }
                if let Some(host) = v["host"].as_str().filter(|s| !s.is_empty()) {
                    n["ws-opts"]["headers"] = json!({"Host":host});
                }
            }
        }
        if v["net"] == "grpc" {
            n["grpc-opts"] = json!({"grpc-service-name":v["path"]});
        }
        if v["net"] == "h2" {
            n["h2-opts"] = json!({"path":v["path"],"host":v["host"]});
        }
        for (source, dest) in [
            ("alpn", "alpn"),
            ("fp", "client-fingerprint"),
            ("allowInsecure", "skip-cert-verify"),
        ] {
            if let Some(value) = v.get(source) {
                n[dest] = value.clone();
            }
        }
        if let Some(sni) = v["sni"].as_str() {
            n["servername"] = json!(sni);
        }
        n
    } else {
        let expanded;
        let raw = if scheme == "ss" && !rest.split('#').next().unwrap().contains('@') {
            let (addr, fragment) = rest.split_once('#').unwrap_or((rest, ""));
            expanded = format!("ss://{}#{fragment}", decode(addr)?);
            &expanded
        } else {
            uri
        };
        let u = Url::parse(raw)?;
        let server = u
            .host_str()
            .context("missing server")?
            .trim_matches(&['[', ']'][..]);
        let port = u
            .port()
            .or_else(|| (scheme == "hysteria2").then_some(443))
            .context("missing port")?;
        let user = unescape(u.username())?;
        let mut n = json!({"type":scheme,"name":u.fragment().map(unescape).transpose()?,"server":server,"port":port});
        match scheme {
            "socks5" => {
                if !user.is_empty() {
                    n["username"] = json!(user);
                }
                if let Some(p) = u.password() {
                    n["password"] = json!(unescape(p)?);
                }
            }
            "tuic" => {
                n["uuid"] = json!(user);
                n["password"] = json!(unescape(u.password().context("missing TUIC password")?)?);
            }
            "ss" => {
                let decoded;
                let (cipher, password) = if let Some(p) = u.password() {
                    (user.as_str(), unescape(p)?)
                } else {
                    decoded = decode(&user)?;
                    let (c, p) = decoded.split_once(':').context("invalid SS user info")?;
                    (c, p.to_owned())
                };
                ensure!(
                    !cipher.is_empty() && !password.is_empty(),
                    "empty SS credentials"
                );
                n["cipher"] = json!(cipher);
                n["password"] = json!(password);
            }
            "vless" | "trojan" | "hysteria2" | "anytls" => {
                ensure!(!user.is_empty(), "missing proxy credential");
                n[if scheme == "vless" {
                    "uuid"
                } else {
                    "password"
                }] = json!(user);
            }
            _ => bail!("unsupported manual node scheme"),
        }
        for (k, v) in u.query_pairs() {
            match k.as_ref() {
                "sni" | "servername" | "peer" if k != "peer" || scheme == "trojan" => {
                    let key = if matches!(scheme, "trojan" | "hysteria2" | "tuic" | "anytls") {
                        "sni"
                    } else {
                        "servername"
                    };
                    ensure!(
                        n.get(key).is_none() || n[key] == v.as_ref(),
                        "conflicting SNI options"
                    );
                    n[key] = json!(v);
                }
                "security" if v == "tls" || v == "reality" => n["tls"] = json!(true),
                "security" if scheme == "vless" && matches!(v.as_ref(), "" | "none") => {
                    n["tls"] = json!(false)
                }
                "encryption" | "headerType" if scheme == "vless" && v == "none" => (),
                "flow" if scheme == "vless" => n["flow"] = json!(v),
                "allowInsecure" | "insecure" | "allow_insecure" => {
                    n["skip-cert-verify"] = json!(match v.as_ref() {
                        "1" | "true" => true,
                        "0" | "false" => false,
                        _ => bail!("invalid certificate verification option"),
                    });
                }
                "type" => n["network"] = json!(v),
                "path" => n["ws-opts"]["path"] = json!(v),
                "host" => n["ws-opts"]["headers"]["Host"] = json!(v),
                "security" | "encryption" => bail!("unsupported proxy encryption"),
                "pbk" => n["reality-opts"]["public-key"] = json!(v),
                "sid" => n["reality-opts"]["short-id"] = json!(v),
                "spx" => n["reality-opts"]["spider-x"] = json!(v),
                "fp" => n["client-fingerprint"] = json!(v),
                "alpn"
                | "obfs"
                | "obfs-password"
                | "upmbps"
                | "downmbps"
                | "up"
                | "down"
                | "ports"
                | "hop-interval"
                | "cwnd"
                | "congestion-controller"
                | "udp-relay-mode"
                | "disable-sni"
                | "reduce-rtt"
                | "idle-session-check-interval"
                | "idle-session-timeout"
                | "min-idle-session"
                | "packet-encoding"
                | "udp" => n[k.as_ref()] = json!(v),
                "serviceName" => n["grpc-opts"]["grpc-service-name"] = json!(v),
                "pinSHA256" => n["fingerprint"] = json!(v),
                "mport" => n["ports"] = json!(v),
                "congestion_control" => n["congestion-controller"] = json!(v),
                "udp_relay_mode" => n["udp-relay-mode"] = json!(v),
                "plugin" if scheme == "ss" => {
                    ss_plugin(&mut n, &v)?;
                }
                _ => (),
            }
        }
        if u.query_pairs()
            .any(|(k, v)| k == "security" && v == "reality")
        {
            ensure!(
                n["reality-opts"]["public-key"].is_string(),
                "missing Reality public key"
            );
            n["reality-opts"]
                .as_object_mut()
                .unwrap()
                .entry("short-id")
                .or_insert(json!(""));
        }
        if n["network"] == "h2" {
            if let Some(opts) = n.as_object_mut().unwrap().remove("ws-opts") {
                if let Some(path) = opts.get("path") {
                    n["h2-opts"]["path"] = path.clone();
                }
                if let Some(host) = opts.pointer("/headers/Host") {
                    n["h2-opts"]["host"] = host.clone();
                }
            }
        }
        // Plain TCP has no WebSocket path, even if an exporter supplies one.
        if n["network"] == "tcp" {
            n.as_object_mut().unwrap().remove("ws-opts");
        }
        n
    };
    ensure!(
        node["port"] != 0 && !node["server"].as_str().unwrap().is_empty(),
        "invalid node endpoint"
    );
    let name = label
        .or(node["name"].as_str())
        .filter(|s| !s.is_empty())
        .unwrap_or(node["server"].as_str().unwrap());
    ensure!(
        !matches!(name, "DIRECT" | "REJECT" | "SmartVPN" | "GLOBAL"),
        "reserved node name"
    );
    node["name"] = json!(name);
    prepare_for_kernel(&mut node)?;
    Ok(node)
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn vmess_share_preserves_cipher_tls_and_optional_ws_host() {
        for host in [Value::Null, json!(""), json!("ws.example")] {
            let share = json!({"add":"example.test","port":"443","id":"fixture","aid":"0","scy":"none","tls":true,"net":"ws","host":host});
            let uri = format!(
                "vmess://{}",
                general_purpose::STANDARD.encode(share.to_string())
            );
            let node = parse(&uri, None).unwrap();
            assert_eq!(node["tls"], true);
            assert_eq!(node["alter-id"], 0);
            assert_eq!(node["cipher"], "none");
            assert_eq!(
                node.pointer("/ws-opts/headers/Host"),
                host.as_str().filter(|s| !s.is_empty()).map(|_| &host)
            );
        }
    }
    #[test]
    fn trojan_tcp_uses_the_native_transport() {
        let tcp = json!({"type":"trojan","network":"tcp","sni":"unit","skip-cert-verify":false});
        let ws = json!({"type":"trojan","network":"ws","ws-opts":{"path":"/test"}});
        let mut nodes = [tcp, ws.clone()];
        for n in &mut nodes {
            prepare_for_kernel(n).unwrap();
        }
        assert_eq!(
            nodes,
            [
                json!({"type":"trojan","sni":"unit","skip-cert-verify":false}),
                ws
            ]
        );
    }
    #[test]
    fn subscription_transport_options() {
        let n = parse("vless://u@host:443?encryption=none&headerType=none&security=tls&type=tcp&path=%2F&flow=xtls-rprx-vision&allowInsecure=0", None).unwrap();
        assert_eq!(n["tls"], true);
        assert_eq!(n["flow"], "xtls-rprx-vision");
        assert_eq!(n["skip-cert-verify"], false);
        assert!(n.get("ws-opts").is_none());
        let n = parse("trojan://p@host:443?sni=a&peer=a&allowInsecure=1", None).unwrap();
        assert_eq!(n["sni"], "a");
        assert_eq!(n["skip-cert-verify"], true);
        for query in ["peer=b&sni=a", "allowInsecure=2"] {
            assert!(parse(&format!("trojan://p@host:443?{query}"), None).is_err());
        }
        assert!(parse("vless://u@host:443?encryption=unknown", None).is_err());
    }
    #[test]
    fn hysteria2_verifies_certificates_by_default() {
        let explicit = json!({"type":"hysteria2","skip-cert-verify":true,"alpn":["h3"]});
        let other = json!({"type":"ss","plugin":"obfs","plugin-opts":{"mode":"http"}});
        let mut nodes = [
            json!({"type":"hysteria2","alpn":["h3"]}),
            explicit.clone(),
            other.clone(),
        ];
        for n in &mut nodes {
            prepare_for_kernel(n).unwrap();
        }
        assert_eq!(
            nodes,
            [
                json!({"type":"hysteria2","skip-cert-verify":false,"alpn":["h3"]}),
                explicit,
                other
            ]
        );
    }
    #[test]
    fn basic_schemes() {
        let vm = general_purpose::STANDARD
            .encode(r#"{"add":"host","port":"443","id":"uuid","ps":"vm"}"#);
        for uri in [
            "socks5://a:b@host:80#one",
            "ss://YWVzLTEyOC1nY206cA@host:443",
            "vless://u@host:443",
            "trojan://p@host:443",
            "hysteria2://p@host:443",
            &format!("vmess://{vm}"),
        ] {
            assert_eq!(parse(uri, None).unwrap()["server"], "host");
        }
        assert!(parse("ss://bad@host:443", None).is_err());
    }
}
