use crate::{http, node, store::Store};
use anyhow::{anyhow, bail, ensure, Context, Result};
use rusqlite::{params, OptionalExtension};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeMap, HashSet},
    fs,
};

#[derive(Default, Serialize, Deserialize)]
pub struct Catalog {
    pub proxies: Vec<Value>,
    pub rules: Vec<String>,
    #[serde(default)]
    pub rule_providers: BTreeMap<String, Value>,
    #[serde(default)]
    pub skipped: usize,
    #[serde(default)]
    pub applied_format: String,
}
const NOTICES: [&str; 7] = [
    "剩余流量",
    "过期时间",
    "官网",
    "通知",
    "到期",
    "公告",
    "永久网址",
];
fn notice(text: &str) -> bool {
    NOTICES.iter().any(|mark| text.contains(mark))
}
#[derive(Debug)]
struct Announcement;
impl std::fmt::Display for Announcement {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("subscription contains only announcements")
    }
}
impl std::error::Error for Announcement {}
fn singbox(v: &Value) -> Result<Value> {
    let kind = match v["type"].as_str().context("missing outbound type")? {
        "shadowsocks" => "ss",
        "socks" => "socks5",
        k => k,
    };
    let mut n = json!({"type":kind,"name":v["tag"],"server":v["server"],"port":v["server_port"]});
    for (source, target) in [
        ("uuid", "uuid"),
        ("method", "cipher"),
        ("security", "cipher"),
        ("password", "password"),
        ("username", "username"),
        ("flow", "flow"),
        ("alter_id", "alter-id"),
        ("up_mbps", "up"),
        ("down_mbps", "down"),
        ("congestion_control", "congestion-controller"),
        ("udp_relay_mode", "udp-relay-mode"),
        ("private_key", "private-key"),
        ("peer_public_key", "public-key"),
        ("pre_shared_key", "pre-shared-key"),
        ("mtu", "mtu"),
        ("reserved", "reserved-bits"),
        ("min_idle_session", "min-idle-session"),
        ("idle_session_check_interval", "idle-session-check-interval"),
        ("idle_session_timeout", "idle-session-timeout"),
        ("plugin", "plugin"),
    ] {
        if let Some(x) = v.get(source) {
            n[target] = x.clone();
        }
    }
    if let Some(plugin) = v["plugin"].as_str() {
        let opts = v["plugin_opts"].as_str().unwrap_or("");
        node::ss_plugin(&mut n, &format!("{plugin};{opts}"))?;
    }
    if kind == "wireguard" {
        for addr in v["local_address"]
            .as_array()
            .context("missing wireguard addresses")?
        {
            let cidr = addr
                .as_str()
                .context("invalid WireGuard CIDR")?
                .parse::<ipnet::IpNet>()?;
            n[if cidr.addr().is_ipv4() { "ip" } else { "ipv6" }] = json!(cidr.to_string());
        }
    }
    if let Some(obfs) = v.get("obfs") {
        n["obfs"] = obfs["type"].clone();
        n["obfs-password"] = obfs["password"].clone();
    }
    if let Some(tls) = v.get("tls") {
        for (source, target) in [
            ("enabled", "tls"),
            (
                "server_name",
                if matches!(kind, "vless" | "vmess") {
                    "servername"
                } else {
                    "sni"
                },
            ),
            ("insecure", "skip-cert-verify"),
            ("alpn", "alpn"),
        ] {
            if let Some(x) = tls.get(source) {
                n[target] = x.clone();
            }
        }
        if tls
            .pointer("/reality/enabled")
            .is_some_and(|v| v == true || v == "true" || v == "1")
        {
            n["reality-opts"] = json!({"public-key":tls["reality"]["public_key"],"short-id":tls["reality"]["short_id"].as_str().unwrap_or("")});
        }
        if let Some(fp) = tls.pointer("/utls/fingerprint") {
            n["client-fingerprint"] = fp.clone();
        }
    }
    if let Some(t) = v.get("transport") {
        match t["type"].as_str() {
            Some("httpupgrade") => {
                n["network"] = json!("httpupgrade");
                n["ws-opts"] = json!({"path":t["path"],"headers":t["headers"]});
                if let Some(host) = t.get("host") {
                    n["ws-opts"]["headers"]["Host"] = host.clone();
                }
            }
            Some("ws") => {
                n["network"] = json!("ws");
                n["ws-opts"] = json!({"path":t["path"],"headers":t["headers"],"max-early-data":t["max_early_data"],"early-data-header-name":t["early_data_header_name"]});
            }
            Some("grpc") => {
                n["network"] = json!("grpc");
                n["grpc-opts"] = json!({"grpc-service-name":t["service_name"]});
            }
            Some("http" | "h2") => {
                n["network"] = json!("h2");
                n["h2-opts"] = json!({"path":t["path"],"host":t["host"]});
            }
            _ => bail!("unsupported outbound transport"),
        }
    }
    Ok(n)
}

pub(crate) fn validate_node(mut n: Value) -> Result<Value> {
    node::prepare_for_kernel(&mut n)?;
    let name = n["name"].as_str().context("missing node name")?;
    ensure!(
        !name.trim().is_empty()
            && !matches!(name, "SmartVPN" | "DIRECT" | "REJECT" | "GLOBAL")
            && n["server"].as_str().is_some_and(|s| !s.is_empty())
            && n["port"].as_u64().is_some_and(|p| (1..=65535).contains(&p)),
        "invalid node endpoint or name"
    );
    let required: &[&str] = match n["type"].as_str() {
        Some("socks5") => &[],
        Some("ss") => &["cipher", "password"],
        Some("vless" | "vmess") => &["uuid"],
        Some("tuic") => &["uuid", "password"],
        Some("trojan" | "anytls" | "hysteria2") => &["password"],
        Some("wireguard") => &["ip", "private-key", "public-key"],
        Some("ssh") => &["username"],
        _ => bail!("unsupported node type"),
    };
    ensure!(
        required
            .iter()
            .all(|k| n[k].as_str().is_some_and(|s| !s.is_empty())),
        "missing proxy credentials"
    );
    if let Some(reality) = n.get("reality-opts") {
        use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};
        let pk = reality["public-key"]
            .as_str()
            .context("missing Reality public key")?;
        ensure!(
            URL_SAFE_NO_PAD.decode(pk)?.len() == 32,
            "invalid Reality public key length"
        );
        let sid = reality["short-id"]
            .as_str()
            .context("missing Reality short id")?;
        ensure!(
            sid.len() <= 16 && sid.len() % 2 == 0 && sid.bytes().all(|b| b.is_ascii_hexdigit()),
            "invalid Reality short id"
        );
    }
    serde_json::from_value::<clash_lib::config::internal::proxy::OutboundProxyProtocol>(n.clone())
        .map_err(|_| anyhow!("invalid kernel proxy schema"))?;
    Ok(n)
}

fn read_catalog(raw: &str) -> Result<Catalog> {
    let mut c: Catalog = serde_json::from_str(raw)?;
    let count = c.proxies.len();
    c.proxies = c
        .proxies
        .into_iter()
        .filter_map(|n| validate_node(n).ok())
        .collect();
    ensure!(
        count == 0 || !c.proxies.is_empty(),
        "cached subscription contains no usable nodes"
    );
    c.skipped += count - c.proxies.len();
    Ok(c)
}
pub(crate) fn sanitize_provider(name: &str, def: &Value) -> Option<Value> {
    if name.is_empty()
        || name.trim() != name
        || name == "SmartVPNChina"
        || name.contains([',', '(', ')'])
        || name.chars().any(char::is_control)
        || def["type"] != "http"
    {
        return None;
    }
    let url = crate::store::web_url(def["url"].as_str()?).ok()?;
    url.as_str().parse::<http_uri::Uri>().ok()?;
    let behavior = def["behavior"].as_str()?;
    let format = match def.get("format") {
        Some(value) => value.as_str()?,
        None => "yaml",
    };
    if !matches!(behavior, "domain" | "ipcidr" | "classical")
        || !matches!(format, "yaml" | "text" | "mrs")
        || (format == "mrs" && behavior == "classical")
    {
        return None;
    }
    let interval = match def.get("interval") {
        Some(value) => value.as_u64()?.clamp(300, 31_536_000),
        None => 86400,
    };
    let hash = Sha256::digest(
        serde_json::to_vec(&(url.as_str(), behavior, format, name)).expect("strings serialize"),
    );
    let ext = if format == "text" { "txt" } else { format };
    Some(
        json!({"type":"http","url":url.as_str(),"behavior":behavior,"format":format,"interval":interval,
        "path":format!("providers/sub-rules/{hash:x}.{ext}")}),
    )
}

fn parse(text: &str) -> Result<Catalog> {
    let text = text.trim_start_matches('\u{feff}');
    let plain = text.trim();
    if !plain.contains("://")
        && !plain.contains("proxies:")
        && !plain.starts_with('{')
        && notice(plain)
    {
        return Err(Announcement.into());
    }
    let doc: Value = if plain.lines().next().is_some_and(|l| {
        l.split_once("://")
            .is_some_and(|(k, _)| k.chars().all(|c| c.is_ascii_alphanumeric()))
    }) {
        Value::Null
    } else {
        yaml_serde::from_str(text).map_err(|_| anyhow!("invalid subscription syntax"))?
    };
    let mut rule_providers = BTreeMap::new();
    let (nodes, rules) = if doc.is_object() {
        let nodes = if let Some(outbounds) = doc.get("outbounds") {
            outbounds
                .as_array()
                .context("expected outbound list")?
                .iter()
                .map(singbox)
                .collect::<Vec<_>>()
        } else {
            doc["proxies"]
                .as_array()
                .context("expected proxies list")?
                .iter()
                .cloned()
                .map(Ok)
                .collect()
        };
        if let Some(defs) = doc["rule-providers"].as_object() {
            for (name, def) in defs {
                if let Some(provider) = sanitize_provider(name, def) {
                    rule_providers.insert(name.clone(), provider);
                }
            }
        }
        let mut rules = Vec::new();
        if let Some(rs) = doc.get("rules") {
            for raw in rs
                .as_array()
                .context("expected rules list")?
                .iter()
                .filter_map(Value::as_str)
            {
                if let Some(rule) = crate::config::import_rule(raw, |n| {
                    rule_providers.get_key_value(n).map(|(k, _)| k.clone())
                }) {
                    rules.push(rule.line);
                }
            }
        }
        (nodes, rules)
    } else {
        let decoded;
        let lines = if text.contains("://") {
            text
        } else {
            decoded = node::decode(&text.split_whitespace().collect::<String>())
                .map_err(|_| anyhow!("invalid subscription encoding"))?;
            &decoded
        };
        if !lines.contains("://") && notice(lines) {
            return Err(Announcement.into());
        }
        let nodes = lines
            .lines()
            .map(str::trim)
            .filter(|l| !l.is_empty())
            .map(|line| {
                // Providers also put notices in fragments of intentionally unusable URIs.
                if let Some((_, fragment)) = line.rsplit_once('#') {
                    let name = percent_encoding::percent_decode_str(fragment).decode_utf8()?;
                    if notice(&name) {
                        return Err(Announcement.into());
                    }
                }
                node::parse(line, None).map_err(|_| anyhow!("invalid subscription node URI"))
            })
            .collect::<Vec<_>>();
        (nodes, Vec::new())
    };
    let mut proxies = Vec::new();
    let mut seen = HashSet::new();
    let mut announcements = 0;
    let total = nodes.len();
    for candidate in nodes {
        if candidate
            .as_ref()
            .is_err_and(|err| err.is::<Announcement>())
        {
            announcements += 1;
        } else if let Ok(n) = candidate {
            if n["name"].as_str().is_some_and(notice) {
                announcements += 1;
                continue;
            }
            if let Ok(valid) = validate_node(n) {
                if seen.insert(valid["name"].as_str().unwrap().to_owned()) {
                    proxies.push(valid);
                }
            }
        }
    }
    if total > 0 && announcements == total {
        return Err(Announcement.into());
    }
    ensure!(!proxies.is_empty(), "subscription contains no usable nodes");
    Ok(Catalog {
        skipped: total - proxies.len(),
        proxies,
        rule_providers,
        rules,
        applied_format: if doc.get("outbounds").is_some() {
            "sing-box"
        } else if doc.is_object() {
            "clash"
        } else {
            "uri"
        }
        .into(),
    })
}
// Providers may omit fields, particularly expiry on non-expiring plans.
fn userinfo(header: Option<&str>) -> Option<Value> {
    let mut info = json!({});
    for field in header?
        .split(';')
        .map(str::trim)
        .filter(|field| !field.is_empty())
    {
        let (key, value) = field.split_once('=')?;
        let key = key.trim();
        if info.get(key).is_some() {
            return None;
        }
        info[key] = match key {
            "expire" => json!(value.trim().parse::<i64>().ok()?),
            "upload" | "download" | "total" => json!(value.trim().parse::<u64>().ok()?),
            _ => return None,
        };
    }
    (!info.as_object().unwrap().is_empty()).then_some(info)
}
pub(crate) fn source_label(source: &str) -> Result<String> {
    if is_web_source(source) {
        return Ok(crate::store::web_url(source)?.host_str().unwrap().into());
    }
    let node = validate_node(node::parse(source, None)?)?;
    Ok(node["name"].as_str().unwrap().into())
}
fn is_web_source(source: &str) -> bool {
    source.split_once(':').is_some_and(|(scheme, _)| {
        scheme.eq_ignore_ascii_case("http") || scheme.eq_ignore_ascii_case("https")
    })
}
fn download(url: &str) -> Result<(Catalog, Option<Value>)> {
    if !is_web_source(url) {
        return Ok((parse(url)?, None));
    }
    let resp = http::download_agent()
        .get(url)
        .call()
        .map_err(http::error)?;
    let quota = userinfo(resp.header("Subscription-Userinfo"));
    let bytes = http::body(resp)?;
    let text = std::str::from_utf8(&bytes)?;
    let primary = parse(text);
    if primary.as_ref().is_err_and(|e| e.is::<Announcement>()) {
        let mut alternate = crate::store::web_url(url)?;
        if !alternate.query_pairs().any(|(k, v)| k == "cla" && v == "1") {
            let pairs: Vec<_> = alternate
                .query_pairs()
                .filter(|(k, _)| k != "cla")
                .map(|(k, v)| (k.into_owned(), v.into_owned()))
                .collect();
            alternate
                .query_pairs_mut()
                .clear()
                .extend_pairs(pairs)
                .append_pair("cla", "1");
            let retry = http::download_agent()
                .get(alternate.as_str())
                .call()
                .map_err(http::error)?;
            let quota = userinfo(retry.header("Subscription-Userinfo"));
            let mut doc = parse(std::str::from_utf8(&http::body(retry)?)?)?;
            doc.applied_format = "cla=1".into();
            return Ok((doc, quota));
        }
    }
    primary.map(|doc| (doc, quota))
}

pub(crate) fn start_refresh(dir: std::path::PathBuf) -> std::thread::JoinHandle<Result<()>> {
    std::thread::spawn(move || {
        let mut store = Store::background(&dir)?;
        let sources = store.subscriptions()?;
        let last = store.pref("mergeAt")?;
        let hours = store.pref("mergeHours")?;
        if !due(
            hours.as_i64().unwrap(),
            last.as_i64().unwrap(),
            chrono::Utc::now().timestamp(),
        ) {
            return Ok(());
        }
        let mut downloads = Vec::new();
        for source in sources.iter().filter(|source| source["enabled"] == true) {
            let (doc, quota) = download(source["url"].as_str().unwrap())?;
            downloads.push((source["id"].as_str().unwrap(), doc, quota));
        }
        if downloads.is_empty() {
            return Ok(());
        }
        // The worker owns this connection. Dropping it rolls back on any error.
        store.db.execute_batch("BEGIN IMMEDIATE")?;
        if store.subscriptions()? != sources
            || store.pref("mergeAt")? != last
            || store.pref("mergeHours")? != hours
        {
            return Ok(());
        }
        for (id, doc, quota) in downloads {
            store.save_download(id, &doc, quota)?;
        }
        store.merge_sources(false)?;
        store.db.execute_batch("COMMIT")?;
        Ok(())
    })
}
pub(crate) fn due(hours: i64, last: i64, now: i64) -> bool {
    hours > 0 && now.saturating_sub(last) >= hours * 3600
}

impl Store {
    pub fn source_id(&self, id: Option<&str>) -> Result<String> {
        if let Some(id) = id {
            return Ok(id.into());
        }
        let active = self.settings()?["subscriptionUrl"]
            .as_str()
            .unwrap()
            .to_owned();
        if active == "__merged__" {
            return Ok(active);
        }
        if active.is_empty() {
            return Ok(self
                .db
                .query_row(
                    "SELECT id FROM subscriptions WHERE enabled ORDER BY rowid LIMIT 1",
                    [],
                    |r| r.get(0),
                )
                .context("no enabled subscription")?);
        }
        Ok(self
            .db
            .query_row("SELECT id FROM subscriptions WHERE url=?", [active], |r| {
                r.get(0)
            })
            .context("unknown current subscription")?)
    }
    fn sub_url(&self, id: &str) -> Result<String> {
        Ok(self
            .db
            .query_row("SELECT url FROM subscriptions WHERE id=?", [id], |r| {
                r.get(0)
            })
            .context("unknown subscription")?)
    }
    fn cached(&self, id: &str) -> Result<Option<Catalog>> {
        let raw: Option<String> = self
            .db
            .query_row("SELECT doc FROM catalogs WHERE id=?", [id], |r| r.get(0))
            .optional()?;
        raw.map(|s| read_catalog(&s)).transpose()
    }
    fn save_download(&mut self, id: &str, doc: &Catalog, quota: Option<Value>) -> Result<()> {
        let tx = self.db.savepoint()?;
        tx.execute(
            "INSERT OR REPLACE INTO catalogs VALUES(?,?,unixepoch())",
            params![id, serde_json::to_string(doc)?],
        )?;
        if let Some(info) = quota {
            tx.execute(
                "INSERT OR REPLACE INTO quotas VALUES(?,?,unixepoch())",
                params![id, info.to_string()],
            )?;
        }
        tx.commit()?;
        Ok(())
    }
    fn fetch_one(&mut self, id: &str) -> Result<(Catalog, Option<String>)> {
        let fresh = download(&self.sub_url(id)?);
        match fresh {
            Ok((doc, quota)) => {
                self.save_download(id, &doc, quota)?;
                Ok((doc, None))
            }
            Err(err) => match self.cached(id)? {
                Some(doc) => Ok((doc, Some(err.to_string()))),
                None => Err(err),
            },
        }
    }
    pub fn active_catalog(&self) -> Result<Catalog> {
        let raw: Option<String> = self
            .db
            .query_row("SELECT doc FROM active_catalog", [], |r| r.get(0))
            .optional()?;
        match raw {
            Some(s) => read_catalog(&s),
            None => Ok(Catalog::default()),
        }
    }
    fn publish(&mut self, url: &str, doc: &mut Catalog) -> Result<()> {
        let raw = serde_json::to_string(doc)?;
        let providers = self.dir.join("providers");
        fs::create_dir_all(&providers)?;
        let tx = self.db.savepoint()?;
        tx.execute("INSERT OR REPLACE INTO active_catalog VALUES(1,?)", [&raw])?;
        tx.execute(
            "UPDATE settings SET value=json_quote(?) WHERE key='subscriptionUrl'",
            [url],
        )?;
        tx.execute("DELETE FROM selection WHERE name NOT IN(SELECT name FROM manual_nodes UNION SELECT json_extract(value,'$.name') FROM json_each(?,'$.proxies'))",[&raw])?;
        let conflicts:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM manual_nodes JOIN json_each(?,'$.proxies') ON name=json_extract(value,'$.name'))",[&raw],|r|r.get(0))?;
        ensure!(!conflicts, "subscription name conflicts with manual node");
        let tmp = providers.join("subscription.tmp");
        fs::write(&tmp, &raw)?;
        fs::rename(tmp, providers.join("subscription.yaml"))?;
        tx.commit()?;
        Ok(())
    }
    pub fn fetch(&mut self, id: &str, activate: bool) -> Result<Value> {
        if id == "__merged__" {
            let mut doc = self.cached(id)?.context("no merged catalog")?;
            self.publish(id, &mut doc)?;
            return Ok(json!({"nodes":doc.proxies.len(),"skipped":doc.skipped,"fromCache":true}));
        }
        let (mut doc, warning) = self.fetch_one(id)?;
        if warning.is_none() {
            self.set_pref("mergeAt", &json!(chrono::Utc::now().timestamp()))?;
        }
        let url = self.sub_url(id)?;
        let active = self.settings()?["subscriptionUrl"].clone();
        if activate || active == url || active == "" {
            self.publish(&url, &mut doc)?;
        } else if active == "__merged__" {
            self.merge_sources(false)?;
        }
        Ok(
            json!({"nodes":doc.proxies.len(),"skipped":doc.skipped,"fromCache":warning.is_some(),"warning":warning,"appliedFormat":doc.applied_format}),
        )
    }
    pub fn merge_sources(&mut self, download: bool) -> Result<Value> {
        let mut doc = Catalog::default();
        let mut names = HashSet::new();
        let mut warnings = Vec::new();
        for (index, sub) in self.subscriptions()?.into_iter().enumerate() {
            if sub["enabled"] != true {
                continue;
            }
            let id = sub["id"].as_str().unwrap();
            let fetched = if download {
                self.fetch_one(id)
                    .with_context(|| format!("subscription {id} has no usable cache"))
            } else {
                self.cached(id)?
                    .with_context(|| format!("subscription {id} has no usable cache"))
                    .map(|cached| (cached, None))
            };
            let (cached, warning) = match fetched {
                Ok(pair) => pair,
                Err(reason) => {
                    warnings.push(json!({"id":id,"error":reason.to_string()}));
                    continue;
                }
            };
            if let Some(reason) = warning {
                warnings.push(json!({"id":id,"error":reason}))
            }
            doc.skipped += cached.skipped;
            for mut n in cached.proxies {
                n["smartvpn-source"] = sub["name"].clone();
                let name = format!(
                    "{}{}",
                    sub["prefix"].as_str().unwrap(),
                    n["name"].as_str().unwrap()
                );
                if names.insert(name.clone()) {
                    n["name"] = json!(name);
                    doc.proxies.push(n)
                }
            }
            let mut renamed = BTreeMap::new();
            for (name, provider) in cached.rule_providers {
                let key = format!("source-{index}-{name}");
                renamed.insert(name, key.clone());
                doc.rule_providers.insert(key, provider);
            }
            doc.rules.extend(cached.rules.iter().filter_map(|raw| {
                crate::config::import_rule(raw, |name| renamed.get(name).cloned())
                    .map(|rule| rule.line)
            }));
        }
        ensure!(!doc.proxies.is_empty(), "no enabled subscription nodes");
        self.db.execute(
            "INSERT OR REPLACE INTO catalogs VALUES('__merged__',?,unixepoch())",
            [serde_json::to_string(&doc)?],
        )?;
        self.publish("__merged__", &mut doc)?;
        self.set_pref("mergeAt", &json!(chrono::Utc::now().timestamp()))?;
        Ok(json!({"nodes":doc.proxies.len(),"warnings":warnings}))
    }
    pub fn quota(&self, id: &str) -> Result<Value> {
        let url = self.sub_url(id)?;
        ensure!(is_web_source(&url), "inline node has no subscription quota");
        let resp = http::download_agent()
            .get(&url)
            .call()
            .map_err(http::error)?;
        let quota = userinfo(resp.header("Subscription-Userinfo"))
            .context("missing or invalid quota header")?;
        self.db.execute(
            "INSERT OR REPLACE INTO quotas VALUES(?,?,unixepoch())",
            params![id, quota.to_string()],
        )?;
        Ok(json!({"id":id,"userInfo":quota}))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn malformed_notice_endpoints_trigger_format_negotiation() {
        use base64::Engine;
        let raw = ["永久网址", "剩余流量", "过期时间"]
            .map(|name| {
                format!(
                    "vless://fixture@sample.invalid:1000:10086#{}",
                    percent_encoding::utf8_percent_encode(name, percent_encoding::NON_ALPHANUMERIC)
                )
            })
            .join("\n");
        let encoded = base64::engine::general_purpose::STANDARD.encode(raw);
        assert!(parse(&encoded).err().unwrap().is::<Announcement>());
    }
    #[test]
    fn disabled_inline_subscription_can_be_added_and_fetched() {
        let dir = tempfile::tempdir().unwrap();
        let mut store = Store::open(dir.path()).unwrap();
        let sub = store
            .add_sub(&json!({
                "label":"sample", "enabled":false,
                "url":"hysteria2://fixture@sample.invalid:443?security=tls&fastopen=0#sample"
            }))
            .unwrap();
        let result = store.fetch(sub["id"].as_str().unwrap(), true).unwrap();
        assert_eq!(result["nodes"], 1);
        assert_eq!(result["skipped"], 0);
        assert_eq!(result["appliedFormat"], "uri");
        let saved = &store.subscriptions().unwrap()[0];
        assert_eq!(saved["enabled"], false);
        assert!(saved["userInfo"].is_null());
    }
    #[test]
    fn headers_and_schedule() {
        assert_eq!(
            userinfo(Some("upload=1; download=2; total=3; expire=-1")),
            Some(json!({"upload":1,"download":2,"total":3,"expire":-1}))
        );
        assert_eq!(
            userinfo(Some("total=18446744073709551615")),
            Some(json!({"total":u64::MAX}))
        );
        for header in [
            None,
            Some(""),
            Some("total=-1"),
            Some("upload=1;total=x"),
            Some("upload=1;upload=2"),
        ] {
            assert!(userinfo(header).is_none());
        }
        assert!(!due(0, 0, 3601));
        assert!(!due(1, 2, 3601));
        assert!(due(1, 2, 3602));
    }
    #[test]
    fn sanitized_policy_parses_on_both_platforms() {
        let nodes = json!([{"type":"socks5","name":"exit","server":"localhost","port":1080}]);
        let mut defs = json!({});
        for (key, behavior, format) in [
            ("zones", "domain", "yaml"),
            ("net", "ipcidr", "mrs"),
            ("ports", "classical", "text"),
        ] {
            defs[key] = json!({"type":"http","url":"https://example.invalid/feed","path":"../../outside",
                "behavior":behavior,"format":format,"interval":1,"payload":["ignored"]});
        }
        defs["default"] = json!({"type":"http","url":"http://localhost/list","behavior":"domain"});
        defs["SmartVPNChina"] = defs["zones"].clone();
        defs["local"] = json!({"type":"file","behavior":"domain","path":"secrets"});
        defs["invalid"] = json!({"type":"http","behavior":"domain","url":"file:///outside"});
        defs["unsupported"] =
            json!({"type":"http","behavior":"classical","format":"mrs","url":"http://localhost/a"});
        let valid = [
            (
                "DOMAIN-REGEX,^(foo|bar)\\.,Group",
                "DOMAIN-REGEX,^(foo|bar)\\.,SmartVPN",
            ),
            ("GEOIP,CN,DIRECT", "GEOIP,CN,DIRECT"),
            ("GEOIP,LAN,DIRECT,no-resolve", "GEOIP,LAN,DIRECT,no-resolve"),
            (
                "DST-PORT,42/80-85/100:200,REJECT-DROP",
                "DST-PORT,42/80-85/100:200,REJECT",
            ),
            ("SRC-PORT,1000,Group", "SRC-PORT,1000,SmartVPN"),
            (
                "SRC-IP-CIDR,::1/128,DIRECT,no-resolve",
                "SRC-IP-CIDR,::1/128,DIRECT,no-resolve",
            ),
            (
                "PROCESS-NAME,browser,Proxy",
                "PROCESS-NAME,browser,SmartVPN",
            ),
            (
                "PROCESS-PATH,/bin/app,REJECT",
                "PROCESS-PATH,/bin/app,REJECT",
            ),
            ("NETWORK,udp,Group", "NETWORK,UDP,SmartVPN"),
            (
                "RULE-SET,net,DIRECT,no-resolve",
                "RULE-SET,net,DIRECT,no-resolve",
            ),
            (
                "AND,((NETWORK,TCP),(OR,((RULE-SET,zones),(NOT,((DST-PORT,443)))))),Group",
                "AND,((NETWORK,TCP),(OR,((RULE-SET,zones),(NOT,((DST-PORT,443)))))),SmartVPN",
            ),
        ];
        let mut rules: Vec<_> = valid.iter().map(|(input, _)| json!(input)).collect();
        for bad in [
            "DOMAIN-WILDCARD,*,Group",
            "GEOSITE,CN,DIRECT",
            "UID,1,DIRECT",
            "DSCP,1,DIRECT",
            "SUB-RULE,(NETWORK,TCP),group",
            "IP-ASN,123,DIRECT",
            "RULE-SET,local,DIRECT",
            "DOMAIN-REGEX,[,DIRECT",
            "NOT,((NETWORK,TCP),(NETWORK,UDP)),DIRECT",
            "OR,((NETWORK,both)),DIRECT",
            "RULE-SET,absent,Group",
            "AND,((RULE-SET,absent)),DIRECT",
            "AND,((NETWORK,TCP),DIRECT",
            "IP-CIDR,10.0.0.0/33,DIRECT",
            "SRC-PORT,65536,DIRECT",
            "DST-PORT,9-1,Group",
            "NETWORK,udp,DIRECT,no-resolve",
        ] {
            rules.push(json!(bad));
        }
        rules.push(Value::Null);
        let doc = parse(&json!({"proxies":nodes,"rules":rules,"rule-providers":defs}).to_string())
            .unwrap();
        assert_eq!(doc.rules, valid.map(|(_, out)| out));
        assert_eq!(doc.rule_providers.len(), 4);
        for (name, provider) in &doc.rule_providers {
            assert_eq!(
                provider["interval"],
                if name == "default" { 86400 } else { 300 }
            );
            assert!(provider.get("payload").is_none());
            assert!(
                regex::Regex::new(r"^providers/sub-rules/[0-9a-f]{64}\.(txt|yaml|mrs)$")
                    .unwrap()
                    .is_match(provider["path"].as_str().unwrap())
            );
        }
        for android in [true, false] {
            for domestic in [true, false] {
                let profile:crate::config::Profile=serde_json::from_value(json!({"mixedPort":18080,"controllerPort":18081,
                    "choices":["exit"],"proxies":[{"name":"exit","server":"localhost","port":1080}],"proxyProviders":{},
                    "ruleProviders":{"SmartVPNChina":"china.txt"},"domesticDirect":domestic,"preventDnsLeaks":true,"webRtcProtection":true,
                    "importPolicy":true,"subscriptionRules":doc.rules,"subscriptionRuleProviders":doc.rule_providers})).unwrap();
                let cfg = profile.generate_for("fixture", android).unwrap();
                clash_lib::Config::Str(cfg.to_string())
                    .try_parse_strict()
                    .unwrap();
                assert_eq!(cfg["rule-providers"].as_object().unwrap().len(), 5);
                let rules = cfg["rules"].as_array().unwrap();
                let idx = |s: &str| rules.iter().position(|r| r == s).unwrap();
                assert_eq!(
                    &rules[..3],
                    &[
                        json!("DST-PORT,53,REJECT"),
                        json!("DST-PORT,853,REJECT"),
                        json!("AND,((NETWORK,UDP),(DST-PORT,3478/5349/19302-19309)),REJECT")
                    ]
                );
                for (kind, nets) in [
                    ("IP-CIDR", crate::config::LAN_V4),
                    ("IP-CIDR6", crate::config::LAN_V6),
                ] {
                    for net in nets {
                        let pos = idx(&format!("{kind},{net},DIRECT,no-resolve"));
                        assert!(pos > 2 && pos < idx(valid[0].1));
                    }
                }
                assert_eq!(rules.last().unwrap(), "MATCH,SmartVPN");
                assert_eq!(rules.contains(&json!("GEOIP,CN,DIRECT")), domestic);
                assert_eq!(
                    rules.contains(&json!(if android { "DOMAIN-SUFFIX,bilibili.com,DIRECT" } else { "AND,((NETWORK,TCP),(DOMAIN-SUFFIX,bilibili.com)),DIRECT" })),
                    domestic
                );
                let china = if android {
                    "RULE-SET,SmartVPNChina,DIRECT,no-resolve"
                } else {
                    "AND,((NETWORK,TCP),(RULE-SET,SmartVPNChina)),DIRECT"
                };
                assert_eq!(rules.contains(&json!(china)), domestic);
                assert_eq!(
                    cfg["dns"]["default-nameserver"],
                    json!(["223.5.5.5", "119.29.29.29"])
                );
                assert_eq!(
                    cfg["dns"]["proxy-server-nameserver"],
                    json!(["223.5.5.5", "119.29.29.29", "https://223.5.5.5/dns-query"])
                );
                assert_eq!(
                    cfg["dns"]["nameserver"],
                    json!([
                        "https://1.1.1.1/dns-query#SmartVPN",
                        "https://8.8.8.8/dns-query#SmartVPN"
                    ])
                );
                if android {
                    for filter in ["*.localdomain", "+.home.arpa"] {
                        assert!(cfg["dns"]["fake-ip-filter"]
                            .as_array()
                            .unwrap()
                            .contains(&json!(filter)));
                    }
                }
            }
        }
    }
    #[test]
    fn mixed_subscription_and_legacy_db() {
        let nodes = json!([
            {"name":"bad","type":"ssr"},{"name":"skip","type":"snell"},
            {"name":"hy","server":"localhost","type":"hysteria2","password":"fixture","port":"443","upmbps":"100 mbps","down":"3000 Mbps","skip-cert-verify":"0","alpn":"h3"}
        ]);
        let yaml = yaml_serde::to_string(&json!({"proxies":nodes})).unwrap();
        let parsed = parse(&yaml).unwrap();
        assert_eq!(parsed.proxies.len(), 1);
        let n = &parsed.proxies[0];
        assert_eq!(
            (n["up"].as_u64(), n["down"].as_u64()),
            (Some(100), Some(3000))
        );
        assert_eq!(n["alpn"], json!(["h3"]));
        assert_eq!(n["skip-cert-verify"], false);
        let temp = tempfile::tempdir().unwrap();
        let store = Store::open(temp.path()).unwrap();
        store
            .db
            .execute(
                "INSERT INTO active_catalog VALUES(1,?)",
                [json!({"rules":[],"proxies":nodes}).to_string()],
            )
            .unwrap();
        drop(store);
        assert_eq!(
            Store::open(temp.path()).unwrap().configs().unwrap(),
            parsed.proxies
        );
        for (raw, expected) in [("1 Gbps", 1000), ("500 kbps", 1), ("200", 200)] {
            let mut n = n.clone();
            n["up"] = json!(raw);
            node::prepare_for_kernel(&mut n).unwrap();
            assert_eq!(n["up"], expected);
        }
    }
    #[test]
    fn mixed_uri_formats() {
        use base64::{engine::general_purpose::STANDARD, Engine};
        let vm = STANDARD.encode(r#"{"ps":"vm","port":"443","add":"localhost","id":"fixture"}"#);
        let text=format!("hy2://pw@host:443?upmbps=9&x=1#hy\nvless://id@host:443?security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=ff&fp=chrome#vl\ntuic://00000000-0000-4000-8000-000000000001:pw@host:443?alpn=h3#tu\ntrojan://pw@host:443?future=1#tr\nvmess://{vm}\nss://YWVzLTEyOC1nY206cA@host:443#ss\nsocks://host:1080#sk\nmalformed\nssr://bad");
        for encoded in [&text, &STANDARD.encode(&text)] {
            let d = parse(encoded).unwrap();
            assert_eq!(d.proxies.len(), 7);
            assert_eq!(d.proxies[0]["up"], 9);
            assert_eq!(d.proxies[1]["reality-opts"]["short-id"], "ff");
        }
    }
    #[test]
    fn sing_box_outbounds() {
        let d=parse(&json!({"outbounds":[{"type":"direct"},{"type":"trojan","tag":"one","server":"localhost","server_port":"443","password":"pw","tls":{"enabled":true,"insecure":"false","server_name":"example.invalid","alpn":"h2"},"transport":{"type":"grpc","service_name":"test"}}]}).to_string()).unwrap();
        assert_eq!(d.applied_format, "sing-box");
        assert_eq!(d.proxies.len(), 1);
        assert_eq!(d.proxies[0]["grpc-opts"]["grpc-service-name"], "test");
        assert_eq!(d.proxies[0]["alpn"], json!(["h2"]));
    }
    #[test]
    fn uri_lists_and_bad_subscriptions() {
        let doc = parse("socks5://one:1080#a\nsocks5://two:1080#b").unwrap();
        assert_eq!(doc.proxies.len(), 2);
        for input in [
            "",
            "proxies: []",
            "proxies: [{name: 到期}]",
            "proxies: [{name: invalid, server: a, type: unknown, port: 1}]",
            "proxies: [{name: invalid, server: a, type: trojan, port: 1}]",
        ] {
            assert!(parse(input).is_err());
        }
        assert_eq!(
            http::redact("contact https://user:pass@example.invalid/path?token=secret"),
            "contact [redacted URL]"
        );
    }
}

#[cfg(test)]
mod download_tests {
    use super::*;
    #[test]
    fn follows_redirect_and_retries_announcements_with_clash_format() {
        let dir = tempfile::tempdir().unwrap();
        let mut store = Store::open(dir.path()).unwrap();
        let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
        let addr = server.server_addr();
        let mock = std::thread::spawn(move || {
            for (path,payload) in [("/entry?cla=0",""),("/notice","线路公告"),("/entry?cla=1","\u{feff}proxies: [{name: sample, type: socks5, server: localhost, port: 1080}]")]{
                let request=server.recv().unwrap();
                assert_eq!(request.url(),path);
                assert!(request.headers().iter().any(|h|h.field.equiv("User-Agent") && h.value.as_str()=="clash.meta"));
                let mut resp=tiny_http::Response::from_string(payload);
                if path.ends_with("cla=0"){
                    resp=resp.with_status_code(301).with_header(tiny_http::Header::from_bytes("Location","/notice").unwrap());
                }
                request.respond(resp).unwrap();
            }
        });
        let sub = store
            .add_sub(&json!({"label":"fixture","url":format!("http://{addr}/entry?cla=0")}))
            .unwrap();
        let result = store.fetch(sub["id"].as_str().unwrap(), true).unwrap();
        assert_eq!(result["appliedFormat"], "cla=1");
        assert_eq!(result["nodes"], 1);
        mock.join().unwrap();
    }

    #[test]
    fn merge_sources_skips_failing_source_without_cache_when_other_succeeds() {
        let dir = tempfile::tempdir().unwrap();
        let mut store = Store::open(dir.path()).unwrap();
        store
            .add_sub(&json!({"label":"ok","url":"socks5://127.0.0.1:1080#ok-node","enabled":true}))
            .unwrap();
        store
            .add_sub(&json!({"label":"bad","url":"http://127.0.0.1:1/unreachable","enabled":true}))
            .unwrap();
        let merged = store.merge_sources(true).unwrap();
        assert_eq!(merged["nodes"], 1);
        assert_eq!(merged["warnings"].as_array().unwrap().len(), 1);
        assert_eq!(store.active_catalog().unwrap().proxies.len(), 1);
    }
}
