use anyhow::{ensure, Context, Result};
use serde::Deserialize;
use serde_json::{json, Value};
use std::{collections::BTreeMap, num::NonZeroU16, path::PathBuf};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Connect {
    pub directory: PathBuf,
    pub config: Profile,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Profile {
    pub mixed_port: NonZeroU16,
    pub controller_port: NonZeroU16,
    pub proxies: Vec<SocksNode>,
    pub choices: Vec<String>,
    pub proxy_providers: BTreeMap<String, PathBuf>,
    pub rule_providers: BTreeMap<String, PathBuf>,
    pub domestic_direct: bool,
    pub prevent_dns_leaks: bool,
    #[serde(default)]
    pub web_rtc_protection: bool,
    #[serde(default)]
    pub import_policy: bool,
    #[serde(default)]
    pub subscription_rules: Vec<String>,
    #[serde(default)]
    pub subscription_rule_providers: BTreeMap<String, Value>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SocksNode {
    name: String,
    server: String,
    port: NonZeroU16,
    dialer_proxy: Option<String>,
}

/// The exit is the only selectable member. Each hop dials through its predecessor.
pub fn probe_config(
    nodes: &[Value],
    mixed: u16,
    controller: std::net::SocketAddr,
    secret: &str,
) -> Value {
    let exit = nodes.last().unwrap()["name"].as_str().unwrap();
    json!({
        "mode":"rule", "bind-address":"127.0.0.1", "mixed-port":mixed,
        "external-controller":controller.to_string(),"secret":secret,
        "dns":{"enable":true,"ipv6":false,"default-nameserver":["223.5.5.5","119.29.29.29"],
               "nameserver":["223.5.5.5","119.29.29.29"]},
        "proxies":nodes,"rules":[format!("MATCH,{exit}")]
    })
}

pub(crate) fn chain_nodes(mut catalog: Vec<Value>, hops: &[String]) -> Result<Vec<Value>> {
    ensure!((1..=8).contains(&hops.len()), "expected 1 to 8 hops");
    let mut chain = Vec::with_capacity(hops.len());
    for (index, name) in hops.iter().enumerate() {
        let position = catalog
            .iter()
            .position(|node| node["name"] == *name)
            .with_context(|| format!("unknown or repeated hop: {name}"))?;
        let mut node = catalog.swap_remove(position);
        let fields = node.as_object_mut().unwrap();
        fields.remove("smartvpn-source");
        fields.remove("dialer-proxy");
        if index > 0 {
            fields.insert("dialer-proxy".into(), json!(hops[index - 1]));
        }
        chain.push(node);
    }
    Ok(chain)
}

// The kernel splits on commas at depth zero, including in regular expressions.
fn fields(rule: &str) -> Option<Vec<&str>> {
    let mut start = 0;
    let mut depth = 0u32;
    let mut parts = Vec::new();
    for (i, c) in rule.char_indices() {
        match c {
            '(' if depth < 64 => depth += 1,
            '(' => return None,
            ')' => depth = depth.checked_sub(1)?,
            ',' if depth == 0 => {
                parts.push(rule[start..i].trim());
                start = i + 1;
            }
            _ => {}
        }
    }
    parts.push(rule[start..].trim());
    (depth == 0 && parts.iter().all(|s| !s.is_empty())).then_some(parts)
}

fn valid_port(item: &str) -> bool {
    if let Some((lo, hi)) = item.split_once(['-', ':']) {
        matches!((lo.trim().parse::<u16>(), hi.trim().parse::<u16>()), (Ok(a), Ok(b)) if a <= b)
    } else {
        item.trim().parse::<u16>().is_ok()
    }
}

fn ungroup(s: &str) -> Option<&str> {
    s.strip_prefix('(')?.strip_suffix(')')
}

fn predicate(
    kind: &str,
    payload: &str,
    option: &[&str],
    provider: &impl Fn(&str) -> Option<String>,
) -> Option<String> {
    let suffix = match option {
        [] => "",
        ["no-resolve"]
            if matches!(
                kind,
                "GEOIP" | "IP-CIDR" | "IP-CIDR6" | "SRC-IP-CIDR" | "RULE-SET"
            ) =>
        {
            ",no-resolve"
        }
        _ => return None,
    };
    let value = match kind {
        "AND" | "OR" | "NOT" => {
            let children = fields(ungroup(payload)?)?;
            if kind == "NOT" && children.len() != 1 {
                return None;
            }
            let children = children
                .into_iter()
                .map(|child| {
                    let parts = fields(ungroup(child)?)?;
                    let [op, value, opts @ ..] = parts.as_slice() else {
                        return None;
                    };
                    predicate(op, value, opts, provider).map(|p| format!("({p})"))
                })
                .collect::<Option<Vec<_>>>()?;
            format!("({})", children.join(","))
        }
        "RULE-SET" => provider(payload)?,
        "DOMAIN" | "DOMAIN-SUFFIX" | "DOMAIN-KEYWORD" | "PROCESS-NAME" | "PROCESS-PATH"
        | "IN-USER" => payload.into(),
        "DOMAIN-REGEX"
            if regex::RegexBuilder::new(payload)
                .case_insensitive(true)
                .build()
                .is_ok() =>
        {
            payload.into()
        }
        "IP-CIDR" if payload.parse::<ipnet::Ipv4Net>().is_ok() => payload.into(),
        "IP-CIDR6" if payload.parse::<ipnet::Ipv6Net>().is_ok() => payload.into(),
        "SRC-IP-CIDR" if payload.parse::<ipnet::IpNet>().is_ok() => payload.into(),
        "GEOIP"
            if (payload.len() == 2 || payload.eq_ignore_ascii_case("LAN"))
                && payload.bytes().all(|b| b.is_ascii_alphabetic()) =>
        {
            payload.to_ascii_uppercase()
        }
        "SRC-PORT" | "DST-PORT" | "PORT-RANGE" | "DST-PORT-RANGE" | "SRC-PORT-RANGE"
            if payload.split('/').all(valid_port) =>
        {
            payload.into()
        }
        "NETWORK" if payload.eq_ignore_ascii_case("tcp") || payload.eq_ignore_ascii_case("udp") => {
            payload.to_ascii_uppercase()
        }
        "IN-TYPE"
            if matches!(
                payload.to_ascii_uppercase().as_str(),
                "HTTP"
                    | "HTTPCONNECT"
                    | "HTTP-CONNECT"
                    | "SOCKS"
                    | "SOCKS5"
                    | "TUN"
                    | "TPROXY"
                    | "REDIR"
                    | "TUNNEL"
                    | "SHADOWSOCKS"
                    | "SS"
                    | "ANYTLS"
                    | "HYSTERIA2"
                    | "HY2"
                    | "IGNORE"
                    | "INNER"
            ) =>
        {
            payload.into()
        }
        _ => return None,
    };
    Some(format!("{kind},{value}{suffix}"))
}

pub(crate) struct ImportedRule {
    pub line: String,
    pub target: &'static str,
}

pub(crate) fn import_rule(
    raw: &str,
    provider: impl Fn(&str) -> Option<String>,
) -> Option<ImportedRule> {
    if raw.chars().any(char::is_control) {
        return None;
    }
    let parts = fields(raw)?;
    let [kind, payload, target, opts @ ..] = parts.as_slice() else {
        return None;
    };
    let target = match *target {
        "DIRECT" => "DIRECT",
        "REJECT" | "REJECT-DROP" => "REJECT",
        _ => "SmartVPN",
    };
    let mut line = predicate(kind, payload, opts, &provider)?;
    if !opts.is_empty() {
        line.truncate(line.len() - ",no-resolve".len());
    }
    line.push(',');
    line.push_str(target);
    if !opts.is_empty() {
        line.push_str(",no-resolve");
    }
    Some(ImportedRule { line, target })
}

pub(crate) const LAN_V4: &[&str] = &[
    "127.0.0.0/8",
    "10.0.0.0/8",
    "172.16.0.0/12",
    "192.168.0.0/16",
    "169.254.0.0/16",
    "100.64.0.0/10",
    "224.0.0.0/4",
    "255.255.255.255/32",
];
pub(crate) const LAN_V6: &[&str] = &["::1/128", "fc00::/7", "fe80::/10", "ff00::/8"];
impl Profile {
    // JSON is a YAML subset accepted by clash-rs. Keep controller credentials
    // out of the caller's profile and provide no generic YAML overrides.
    pub fn generate(&self, secret: &str) -> Result<Value> {
        self.generate_for(secret, cfg!(target_os = "android"))
    }
    pub fn prepare_runtime(&self, dir: &std::path::Path) -> Result<()> {
        if self.import_policy {
            crate::store::install_country_db(dir)?;
            std::fs::create_dir_all(dir.join("providers/sub-rules"))?;
        }
        Ok(())
    }
    pub(crate) fn generate_for(&self, secret: &str, android: bool) -> Result<Value> {
        ensure!(
            self.mixed_port != self.controller_port
                && [self.mixed_port.get(), self.controller_port.get()]
                    .iter()
                    .all(|p| *p > 1023),
            "expected distinct unprivileged listener ports"
        );
        ensure!(
            !self.choices.is_empty() || !self.proxy_providers.is_empty(),
            "empty SmartVPN group"
        );
        let mut rules = Vec::new();
        if self.prevent_dns_leaks {
            for port in [53, 853] {
                rules.push(format!("DST-PORT,{port},REJECT"));
            }
        }
        if self.web_rtc_protection {
            rules.push("AND,((NETWORK,UDP),(DST-PORT,3478/5349/19302-19309)),REJECT".into());
        }
        if android && (!self.web_rtc_protection || self.domestic_direct) {
            rules.push("AND,((NETWORK,UDP),(DST-PORT,443)),REJECT".into());
        }
        let mut group = json!({"name":"SmartVPN","type":"select","proxies":self.choices});
        let nodes: Vec<_> = self
            .proxies
            .iter()
            .map(|node| {
                let mut out =
                    json!({"name":node.name,"type":"socks5","server":node.server,"port":node.port});
                if let Some(v) = &node.dialer_proxy {
                    out["dialer-proxy"] = json!(v);
                }
                out
            })
            .collect();
        let mut cfg = json!({"mixed-port":self.mixed_port,"bind-address":"127.0.0.1","allow-lan":false,
            "external-controller":format!("127.0.0.1:{}",self.controller_port),"secret":secret,
            "mode":"rule","proxies":nodes});
        if !self.proxy_providers.is_empty() {
            group["use"] = json!(self.proxy_providers.keys().collect::<Vec<_>>());
            for (name, path) in &self.proxy_providers {
                cfg["proxy-providers"][name] = json!({"type":"file","path":path});
            }
        }
        for (name, path) in &self.rule_providers {
            cfg["rule-providers"][name] =
                json!({"type":"file","behavior":"ipcidr","format":"text","path":path});
            if !android && self.domestic_direct && self.web_rtc_protection {
                let common = format!("(NETWORK,UDP),(DST-PORT,443),(RULE-SET,{name})");
                for exe in [
                    "chrome.exe",
                    "msedge.exe",
                    "firefox.exe",
                    "brave.exe",
                    "opera.exe",
                    "vivaldi.exe",
                    "arc.exe",
                ] {
                    rules.push(format!("AND,({common},(PROCESS-NAME,{exe})),REJECT"));
                }
            }
        }
        for (kind, networks) in [("IP-CIDR", LAN_V4), ("IP-CIDR6", LAN_V6)] {
            rules.extend(
                networks
                    .iter()
                    .map(|cidr| format!("{kind},{cidr},DIRECT,no-resolve")),
            );
        }
        let allow_direct =
            self.domestic_direct || !(self.prevent_dns_leaks || self.web_rtc_protection);
        if self.import_policy {
            cfg["mmdb"] = json!("Country.mmdb");
            for (name, def) in &self.subscription_rule_providers {
                if let Some(provider) = crate::subscriptions::sanitize_provider(name, def) {
                    cfg["rule-providers"][name] = provider;
                }
            }
            for raw in &self.subscription_rules {
                if let Some(rule) = import_rule(raw, |name| {
                    cfg["rule-providers"].get(name).map(|_| name.to_owned())
                }) {
                    if allow_direct || rule.target != "DIRECT" {
                        rules.push(rule.line);
                    }
                }
            }
        }
        if allow_direct {
            let option = if android { ",no-resolve" } else { "" };
            for name in self.rule_providers.keys() {
                rules.push(format!("RULE-SET,{name},DIRECT{option}"));
            }
        }
        cfg["dns"] = json!({"enable":true,"ipv6":false,"enhanced-mode":"redir-host",
            "default-nameserver":["223.5.5.5","119.29.29.29"],
            "proxy-server-nameserver":["223.5.5.5","119.29.29.29","https://223.5.5.5/dns-query"],
            "nameserver":["https://1.1.1.1/dns-query#SmartVPN","https://8.8.8.8/dns-query#SmartVPN"]});
        if android {
            cfg["dns"]["enhanced-mode"] = json!("fake-ip");
            cfg["dns"]["fake-ip-range"] = json!("198.18.0.1/16");
            cfg["dns"]["fake-ip-filter"] = json!([
                "localhost",
                "*.lan",
                "*.local",
                "*.localdomain",
                "+.home.arpa",
                "+.msftconnecttest.com",
                "+.msftncsi.com",
                "+.ntp.org",
                "time.*",
                "ntp.*"
            ]);
        }
        if self.domestic_direct {
            for domain in include_str!("../assets/domestic-domains.txt").lines() {
                if android || self.prevent_dns_leaks {
                    cfg["dns"]["nameserver-policy"][format!("+.{domain}")] =
                        json!("https://223.5.5.5/dns-query#DIRECT");
                }
                rules.push(format!("DOMAIN-SUFFIX,{domain},DIRECT"));
            }
        }
        rules.push("MATCH,SmartVPN".into());
        cfg["proxy-groups"] = json!([group]);
        cfg["rules"] = json!(rules);
        Ok(cfg)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn connects_reverse_catalog_in_requested_order() {
        let names = ["入口 [A]", "中继 B", "出口 [C]"].map(String::from);
        let catalog: Vec<_> = names
            .iter()
            .rev()
            .map(|name| json!({"name":name,"smartvpn-source":"source","dialer-proxy":"old"}))
            .collect();
        let chain = chain_nodes(catalog.clone(), &names).unwrap();
        assert_eq!(
            chain,
            vec![
                json!({"name":names[0]}),
                json!({"name":names[1],"dialer-proxy":names[0]}),
                json!({"name":names[2],"dialer-proxy":names[1]})
            ]
        );
        assert!(chain_nodes(catalog, &[names[0].clone(), names[0].clone()]).is_err());
    }
    #[test]
    fn generates_clash_schema() {
        let mut input = json!({"mixedPort":12345,"controllerPort":12346,
            "choices":["exit"],"proxies":[{"name":"exit","server":"localhost","port":9999,"dialerProxy":"entry"}],
            "proxyProviders":{"sub":"nodes.yaml"},"ruleProviders":{"china":"china.txt"},
            "domesticDirect":true,"preventDnsLeaks":true});
        let android: Profile = serde_json::from_value(input.clone()).unwrap();
        let android = android.generate_for("test", true).unwrap();
        let dns = &android["dns"];
        assert_eq!(dns["enhanced-mode"], "fake-ip");
        assert_eq!(dns["fake-ip-range"], "198.18.0.1/16");
        assert_eq!(
            dns["default-nameserver"],
            json!(["223.5.5.5", "119.29.29.29"])
        );
        assert_eq!(
            dns["proxy-server-nameserver"],
            json!(["223.5.5.5", "119.29.29.29", "https://223.5.5.5/dns-query"])
        );
        assert!(android["rules"]
            .as_array()
            .unwrap()
            .contains(&json!("RULE-SET,china,DIRECT,no-resolve")));
        assert_eq!(android["dns"]["ipv6"], false);
        assert!(android["rules"]
            .as_array()
            .unwrap()
            .contains(&json!("AND,((NETWORK,UDP),(DST-PORT,443)),REJECT")));
        let cfg = serde_json::from_value::<Profile>(input.clone())
            .unwrap()
            .generate("secret")
            .unwrap();
        assert_eq!(
            cfg["dns"]["nameserver-policy"]["+.cn"],
            "https://223.5.5.5/dns-query#DIRECT"
        );
        assert_eq!(cfg["proxies"][0]["dialer-proxy"], "entry");
        assert_eq!(cfg["proxy-groups"][0]["use"][0], "sub");
        assert_eq!(cfg["rule-providers"]["china"]["format"], "text");
        let rules = cfg["rules"].as_array().unwrap();
        assert_eq!(rules[0], "DST-PORT,53,REJECT");
        assert!(rules.contains(&json!("RULE-SET,china,DIRECT")));
        assert_eq!(rules.last().unwrap(), "MATCH,SmartVPN");
        input["controllerPort"] = input["mixedPort"].clone();
        assert!(serde_json::from_value::<Profile>(input.clone())
            .unwrap()
            .generate("secret")
            .is_err());
        input["tun"] = json!(true);
        assert!(serde_json::from_value::<Profile>(input).is_err());
    }
}
