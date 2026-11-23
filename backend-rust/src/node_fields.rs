//! Field conversion after Go has validated/parses the URL endpoint and name.
use std::collections::BTreeMap;

pub struct NodeInput<'a> {
    pub kind: &'a str,
    pub server: &'a str,
    pub port: i64,
    pub name: &'a str,
    pub secret: &'a str,
    pub query: &'a BTreeMap<String, String>,
}
#[derive(Debug, PartialEq, Clone)]
pub enum Value {
    String(String),
    Bool(bool),
    Int(i64),
    Array(Vec<Value>),
    Object(BTreeMap<String, Value>),
}
#[derive(Debug, PartialEq, Clone)]
pub enum ConversionError {
    Message(String),
    UnsupportedTransport(String),
}

fn trim(text: &str) -> &str {
    text.trim_matches(|c| matches!(c, '\u{0009}'..='\u{000d}' | '\u{0020}' | '\u{0085}' | '\u{00a0}' | '\u{1680}' | '\u{2000}'..='\u{200a}' | '\u{2028}' | '\u{2029}' | '\u{202f}' | '\u{205f}' | '\u{3000}'))
}
fn lower(text: &str) -> String {
    trim(text)
        .chars()
        .map(|c| {
            if c == '\u{0130}' {
                'i'
            } else {
                c.to_lowercase().next().unwrap_or(c)
            }
        })
        .collect()
}
fn first<'a>(a: &'a str, b: &'a str) -> &'a str {
    if trim(a).is_empty() {
        trim(b)
    } else {
        trim(a)
    }
}
// Matches atoiPort for the accepted positive range (ASCII decimal only).
fn port_number(value: &str) -> Option<i64> {
    let value = trim(value);
    let digits = value.strip_prefix('+').unwrap_or(value);
    if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let parsed = digits.parse::<u64>().ok()?;
    if (1..=65535).contains(&parsed) {
        Some(parsed as i64)
    } else {
        None
    }
}
fn insecure(value: &str) -> bool {
    matches!(lower(value).as_str(), "1" | "true" | "yes")
}
fn string(value: &str) -> Value {
    Value::String(value.to_owned())
}
fn insert_if(map: &mut BTreeMap<String, Value>, key: &str, value: &str) {
    if !value.is_empty() {
        map.insert(key.to_owned(), string(value));
    }
}
fn split_list(value: &str) -> Vec<Value> {
    value
        .split(',')
        .map(trim)
        .filter(|v| !v.is_empty())
        .map(string)
        .collect()
}

fn transport(
    proxy: &mut BTreeMap<String, Value>,
    network: &str,
    path: &str,
    host: &str,
    service: &str,
) -> Result<(), ConversionError> {
    match lower(network).as_str() {
        "" | "tcp" => {}
        "ws" | "websocket" => {
            proxy.insert("network".into(), string("ws"));
            let mut options = BTreeMap::new();
            insert_if(&mut options, "path", path);
            if !host.is_empty() {
                options.insert(
                    "headers".into(),
                    Value::Object(BTreeMap::from([("Host".into(), string(host))])),
                );
            }
            if !options.is_empty() {
                proxy.insert("ws-opts".into(), Value::Object(options));
            }
        }
        "grpc" => {
            proxy.insert("network".into(), string("grpc"));
            if !service.is_empty() {
                proxy.insert(
                    "grpc-opts".into(),
                    Value::Object(BTreeMap::from([(
                        "grpc-service-name".into(),
                        string(service),
                    )])),
                );
            }
        }
        "h2" | "http" => {
            proxy.insert("network".into(), string("h2"));
            let mut options = BTreeMap::new();
            insert_if(&mut options, "path", path);
            if !host.is_empty() {
                options.insert("host".into(), Value::Array(vec![string(host)]));
            }
            if !options.is_empty() {
                proxy.insert("h2-opts".into(), Value::Object(options));
            }
        }
        _ => return Err(ConversionError::UnsupportedTransport(network.to_owned())),
    }
    Ok(())
}

/// Preserves the original field values and omission rules. Endpoint/name parsing
/// and finishManualNode stay in Go. In particular secrets are validated trimmed
/// but stored verbatim, and transport path/host/service fields are not trimmed.
pub fn convert_node(input: NodeInput<'_>) -> Result<BTreeMap<String, Value>, ConversionError> {
    if !matches!(
        input.kind,
        "vless" | "trojan" | "hysteria2" | "tuic" | "hysteria" | "vmess" | "ss" | "ssr"
    ) {
        return Err(ConversionError::Message("unsupported node kind".into()));
    }
    if matches!(input.kind, "vless" | "trojan" | "hysteria2") && trim(input.secret).is_empty() {
        return Err(ConversionError::Message("节点链接缺少必要字段".into()));
    }
    let q = |key: &str| input.query.get(key).map(String::as_str).unwrap_or("");
    let mut proxy = BTreeMap::from([
        ("name".into(), string(input.name)),
        ("type".into(), string(input.kind)),
        ("server".into(), string(input.server)),
        ("port".into(), Value::Int(input.port)),
    ]);
    if input.kind == "vmess" {
        if trim(input.secret).is_empty() {
            return Err(ConversionError::Message("节点链接缺少必要字段".into()));
        }
        proxy.insert("uuid".into(), string(trim(input.secret)));
        let aid = q("__smartvpn_alter_id")
            .parse::<i64>()
            .map_err(|_| ConversionError::Message("invalid normalized alterId".into()))?;
        proxy.insert("alterId".into(), Value::Int(aid));
        let cipher = q("__smartvpn_cipher");
        proxy.insert(
            "cipher".into(),
            string(if matches!(lower(cipher).as_str(), "" | "auto") {
                "auto"
            } else {
                trim(cipher)
            }),
        );
        proxy.insert("udp".into(), Value::Bool(true));
        let tls = lower(q("__smartvpn_tls"));
        if !matches!(tls.as_str(), "" | "none" | "false") {
            proxy.insert("tls".into(), Value::Bool(true));
            insert_if(
                &mut proxy,
                "servername",
                first(q("__smartvpn_sni"), q("__smartvpn_host")),
            );
        }
        insert_if(&mut proxy, "client-fingerprint", trim(q("__smartvpn_fp")));
        let count = q("__smartvpn_alpn_count")
            .parse::<usize>()
            .map_err(|_| ConversionError::Message("invalid normalized ALPN count".into()))?;
        if count > input.query.len() {
            return Err(ConversionError::Message(
                "invalid normalized ALPN count".into(),
            ));
        }
        let mut alpn = Vec::new();
        for i in 0..count {
            let value = input
                .query
                .get(&format!("__smartvpn_alpn_{i}"))
                .ok_or_else(|| ConversionError::Message("missing normalized ALPN item".into()))?;
            alpn.push(string(value));
        }
        if !alpn.is_empty() {
            proxy.insert("alpn".into(), Value::Array(alpn));
        }
        transport(
            &mut proxy,
            q("__smartvpn_network"),
            q("__smartvpn_path"),
            q("__smartvpn_host"),
            q("__smartvpn_path"),
        )?;
    } else if input.kind == "ss" {
        let cipher = trim(q("__smartvpn_cipher"));
        if cipher.is_empty() || input.secret.is_empty() {
            return Err(ConversionError::Message("节点链接缺少必要字段".into()));
        }
        proxy.insert("cipher".into(), string(cipher));
        proxy.insert("password".into(), string(input.secret));
        proxy.insert("udp".into(), Value::Bool(true));
    } else if input.kind == "ssr" {
        proxy.insert("cipher".into(), string(trim(q("__smartvpn_cipher"))));
        proxy.insert("password".into(), string(input.secret));
        proxy.insert("protocol".into(), string(trim(q("__smartvpn_protocol"))));
        proxy.insert("obfs".into(), string(trim(q("__smartvpn_obfs"))));
        proxy.insert("udp".into(), Value::Bool(true));
        insert_if(&mut proxy, "obfs-param", q("__smartvpn_obfs_param"));
        insert_if(&mut proxy, "protocol-param", q("__smartvpn_proto_param"));
    } else if input.kind == "tuic" {
        // These reserved values come from trusted Go URL-userinfo parsing only.
        let mut uuid = q("__smartvpn_tuic_username");
        let password = q("__smartvpn_tuic_password");
        if trim(uuid).is_empty() && trim(password).is_empty() {
            return Err(ConversionError::Message("节点链接缺少必要字段".into()));
        }
        // Go falls back only on an exactly empty username, not whitespace.
        if uuid.is_empty() {
            uuid = password;
        }
        proxy.insert("uuid".into(), string(uuid));
        proxy.insert("password".into(), string(password));
        proxy.insert("udp".into(), Value::Bool(true));
        insert_if(&mut proxy, "sni", first(q("sni"), q("server_name")));
        if insecure(first(
            q("allow_insecure"),
            first(q("insecure"), q("allowInsecure")),
        )) {
            proxy.insert("skip-cert-verify".into(), Value::Bool(true));
        }
        insert_if(
            &mut proxy,
            "congestion-controller",
            first(
                q("congestion_controller"),
                first(q("congestion_control"), q("cc")),
            ),
        );
        let alpn = split_list(q("alpn"));
        if !alpn.is_empty() {
            proxy.insert("alpn".into(), Value::Array(alpn));
        }
        insert_if(&mut proxy, "udp-relay-mode", q("udp_relay_mode"));
    } else if input.kind == "hysteria" {
        insert_if(&mut proxy, "auth_str", first(input.secret, q("auth")));
        if let Some(up) = port_number(first(q("upmbps"), q("up"))) {
            proxy.insert("up".into(), Value::Int(up));
        }
        if let Some(down) = port_number(first(q("downmbps"), q("down"))) {
            proxy.insert("down".into(), Value::Int(down));
        }
        insert_if(&mut proxy, "sni", first(q("peer"), q("sni")));
        if insecure(first(q("insecure"), q("allowInsecure"))) {
            proxy.insert("skip-cert-verify".into(), Value::Bool(true));
        }
        let alpn = split_list(q("alpn"));
        if !alpn.is_empty() {
            proxy.insert("alpn".into(), Value::Array(alpn));
        }
        insert_if(&mut proxy, "obfs", q("obfs"));
    } else if input.kind == "vless" {
        proxy.insert("uuid".into(), string(input.secret));
        proxy.insert("udp".into(), Value::Bool(true));
        let security = lower(q("security"));
        match security.as_str() {
            "tls" | "reality" => {
                proxy.insert("tls".into(), Value::Bool(true));
                insert_if(&mut proxy, "servername", trim(q("sni")));
                insert_if(&mut proxy, "client-fingerprint", trim(q("fp")));
                if insecure(q("allowInsecure")) {
                    proxy.insert("skip-cert-verify".into(), Value::Bool(true));
                }
                if security == "reality" {
                    let key = trim(q("pbk"));
                    if key.is_empty() {
                        return Err(ConversionError::Message(
                            "vless reality 链接缺少 public key（pbk）".into(),
                        ));
                    }
                    let mut opts = BTreeMap::from([("public-key".into(), string(key))]);
                    insert_if(&mut opts, "short-id", trim(q("sid")));
                    proxy.insert("reality-opts".into(), Value::Object(opts));
                }
            }
            "" | "none" => {}
            _ => {
                return Err(ConversionError::Message(format!(
                    "暂不支持 vless 的 security={security}"
                )))
            }
        }
        insert_if(&mut proxy, "flow", trim(q("flow")));
        transport(
            &mut proxy,
            q("type"),
            q("path"),
            first(q("host"), q("sni")),
            q("serviceName"),
        )?;
    } else if input.kind == "trojan" {
        proxy.insert("password".into(), string(input.secret));
        proxy.insert("udp".into(), Value::Bool(true));
        insert_if(&mut proxy, "sni", first(q("sni"), q("peer")));
        if insecure(first(q("allowInsecure"), q("insecure"))) {
            proxy.insert("skip-cert-verify".into(), Value::Bool(true));
        }
        let alpn = split_list(q("alpn"));
        if !alpn.is_empty() {
            proxy.insert("alpn".into(), Value::Array(alpn));
        }
        transport(
            &mut proxy,
            q("type"),
            q("path"),
            q("host"),
            q("serviceName"),
        )?;
    } else {
        proxy.insert("password".into(), string(input.secret));
        insert_if(&mut proxy, "sni", trim(q("sni")));
        if insecure(q("insecure")) {
            proxy.insert("skip-cert-verify".into(), Value::Bool(true));
        }
        let obfs = trim(q("obfs"));
        if !obfs.is_empty() {
            proxy.insert("obfs".into(), string(obfs));
            insert_if(&mut proxy, "obfs-password", trim(q("obfs-password")));
        }
        let alpn = split_list(q("alpn"));
        if !alpn.is_empty() {
            proxy.insert("alpn".into(), Value::Array(alpn));
        }
    }
    Ok(proxy)
}
