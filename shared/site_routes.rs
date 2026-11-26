use serde::{Deserialize, Serialize};
use std::{collections::HashSet, net::IpAddr};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SiteRoute {
    host: String,
    action: Action,
    include_subdomains: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum Action {
    Direct,
    Proxy,
}

pub fn normalize(mut rules: Vec<SiteRoute>) -> Result<Vec<SiteRoute>, String> {
    let mut hosts = HashSet::new();
    for rule in &mut rules {
        let host = rule.host.trim_end_matches('.').to_ascii_lowercase();
        rule.host = if let Ok(ip) = host.parse::<IpAddr>() {
            rule.include_subdomains = false;
            ip.to_string()
        } else {
            if !host.contains('.')
                || host.len() > 253
                || host.bytes().all(|b| b == b'.' || b.is_ascii_digit())
                || !host.split('.').all(|label| {
                    !label.is_empty()
                        && label.len() <= 63
                        && !label.starts_with('-')
                        && !label.ends_with('-')
                        && label
                            .bytes()
                            .all(|b| b.is_ascii_alphanumeric() || b == b'-')
                })
            {
                return Err(format!("无效域名：{host}"));
            }
            host
        };
        if !hosts.insert(rule.host.clone()) {
            return Err(format!("域名规则重复：{}", rule.host));
        }
    }
    // First match wins in both engines. A child can override its parent.
    rules.sort_by_key(|r| std::cmp::Reverse(r.host.len()));
    Ok(rules)
}

pub fn deserialize<'de, D: serde::Deserializer<'de>>(d: D) -> Result<Vec<SiteRoute>, D::Error> {
    normalize(Vec::deserialize(d)?).map_err(serde::de::Error::custom)
}

impl SiteRoute {
    pub fn clash_rule(&self) -> String {
        let target = match self.action {
            Action::Direct => "DIRECT",
            Action::Proxy => "SmartVPN",
        };
        match self.host.parse::<IpAddr>() {
            Ok(IpAddr::V4(ip)) => format!("IP-CIDR,{ip}/32,{target}"),
            Ok(IpAddr::V6(ip)) => format!("IP-CIDR6,{ip}/128,{target}"),
            Err(_) => {
                let kind = if self.include_subdomains {
                    "DOMAIN-SUFFIX"
                } else {
                    "DOMAIN"
                };
                format!("{kind},{},{target}", self.host)
            }
        }
    }
}
