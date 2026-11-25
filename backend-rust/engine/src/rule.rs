use std::{io, net::IpAddr};

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Rule {
    Domain(String),
    DomainSuffix(String),
    DomainKeyword(String),
    IpCidr { addr: IpAddr, prefix: u8 },
    ProcessName(String),
    China,
    Match,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RuleAction {
    Direct,
    Reject,
    Proxy(String),
}

#[derive(Default)]
pub struct RuleContext<'a> {
    pub domain: Option<&'a str>,
    pub ip: Option<IpAddr>,
    pub process_name: Option<&'a str>,
}

#[derive(Clone)]
pub struct RuleSet {
    rules: Vec<(Rule, RuleAction)>,
    pub(crate) china: std::sync::Arc<crate::domestic::Ranges>,
}

impl RuleSet {
    pub fn from_lines(lines: &[&str]) -> io::Result<Self> {
        Self::parse(lines, false, Default::default())
    }

    pub(crate) fn from_clash_lines(
        lines: &[&str],
        china: crate::domestic::Ranges,
    ) -> io::Result<Self> {
        Self::parse(lines, true, china)
    }

    fn parse(lines: &[&str], clash: bool, china: crate::domestic::Ranges) -> io::Result<Self> {
        let mut rules = Vec::new();
        for (i, line) in lines.iter().enumerate() {
            let invalid = || {
                io::Error::new(
                    io::ErrorKind::InvalidInput,
                    format!("invalid rule on line {}: {line}", i + 1),
                )
            };
            let parts: Vec<_> = line.split(',').map(str::trim).collect();
            if clash {
                match parts.as_slice() {
                    ["GEOIP", "LAN", tag] | ["GEOIP", "LAN", tag, "no-resolve"]
                        if !tag.is_empty() =>
                    {
                        for cidr in [
                            "10.0.0.0/8",
                            "172.16.0.0/12",
                            "192.168.0.0/16",
                            "127.0.0.0/8",
                            "169.254.0.0/16",
                            "::1/128",
                            "fc00::/7",
                            "fe80::/10",
                        ] {
                            rules.extend(
                                Self::from_lines(&[&format!("IP-CIDR,{cidr},{tag}")])?.rules,
                            );
                        }
                        continue;
                    }
                    ["GEOIP", "CN", _] if !china.is_empty() => {}
                    ["GEOIP", "LAN", ..] => return Err(invalid()),
                    ["MATCH" | "DOMAIN" | "DOMAIN-SUFFIX" | "DOMAIN-KEYWORD" | "PROCESS-NAME"
                    | "IP-CIDR" | "IP-CIDR6", ..] => {}
                    _ => continue,
                }
            }
            if parts.contains(&"") {
                return Err(invalid());
            }
            let (rule, tag) = match parts.as_slice() {
                ["GEOIP", "CN", tag] => (Rule::China, tag),
                ["MATCH", tag] => (Rule::Match, tag),
                ["DOMAIN", v, tag] => (Rule::Domain(v.to_ascii_lowercase()), tag),
                ["DOMAIN-SUFFIX", v, tag] => (Rule::DomainSuffix(v.to_ascii_lowercase()), tag),
                ["DOMAIN-KEYWORD", v, tag] => (Rule::DomainKeyword(v.to_ascii_lowercase()), tag),
                ["PROCESS-NAME", v, tag] => (Rule::ProcessName(v.to_string()), tag),
                ["IP-CIDR" | "IP-CIDR6", v, tag]
                | ["IP-CIDR" | "IP-CIDR6", v, tag, "no-resolve"] => {
                    let (ip, bits) = v.split_once('/').ok_or_else(invalid)?;
                    let addr: IpAddr = ip.parse().map_err(|_| invalid())?;
                    let prefix = bits.parse().map_err(|_| invalid())?;
                    if prefix > if addr.is_ipv4() { 32 } else { 128 }
                        || (parts[0] == "IP-CIDR6" && addr.is_ipv4())
                    {
                        return Err(invalid());
                    }
                    (Rule::IpCidr { addr, prefix }, tag)
                }
                _ => return Err(invalid()),
            };
            rules.push((
                rule,
                match *tag {
                    "DIRECT" => RuleAction::Direct,
                    "REJECT" => RuleAction::Reject,
                    name => RuleAction::Proxy(name.into()),
                },
            ));
        }
        Ok(Self {
            rules,
            china: std::sync::Arc::new(china),
        })
    }

    pub(crate) fn needs_process_name(&self) -> bool {
        self.rules
            .iter()
            .any(|(rule, _)| matches!(rule, Rule::ProcessName(_)))
    }

    pub(crate) fn resolve_actions(
        &mut self,
        mut resolve: impl FnMut(&str) -> io::Result<RuleAction>,
    ) -> io::Result<()> {
        for (_, action) in &mut self.rules {
            if let RuleAction::Proxy(tag) = action {
                *action = resolve(tag)?;
            }
        }
        Ok(())
    }

    pub fn evaluate(&self, ctx: &RuleContext<'_>) -> Option<&RuleAction> {
        self.match_rule(ctx, ctx.ip.is_some_and(|ip| self.china.contains(ip)))
            .map(|(_, action)| action)
    }

    pub(crate) fn match_rule(
        &self,
        ctx: &RuleContext<'_>,
        china_matches: bool,
    ) -> Option<&(Rule, RuleAction)> {
        let domain = ctx.domain.map(str::to_ascii_lowercase);
        self.rules.iter().find(|(rule, _)| match rule {
            Rule::Domain(v) => domain.as_ref() == Some(v),
            Rule::DomainSuffix(v) => domain.as_ref().is_some_and(|d| {
                d.strip_suffix(v)
                    .is_some_and(|p| p.is_empty() || p.ends_with('.'))
            }),
            Rule::DomainKeyword(v) => domain.as_ref().is_some_and(|d| d.contains(v)),
            Rule::IpCidr { addr, prefix } => match (*addr, ctx.ip) {
                (IpAddr::V4(a), Some(IpAddr::V4(b))) => {
                    (u32::from(a) ^ u32::from(b)).leading_zeros() >= u32::from(*prefix)
                }
                (IpAddr::V6(a), Some(IpAddr::V6(b))) => {
                    (u128::from(a) ^ u128::from(b)).leading_zeros() >= u32::from(*prefix)
                }
                _ => false,
            },
            Rule::ProcessName(v) => ctx.process_name.is_some_and(|p| process_matches(p, v)),
            Rule::China => china_matches,
            Rule::Match => true,
        })
    }
}

fn process_matches(actual: &str, expected: &str) -> bool {
    if cfg!(windows) {
        let a = actual.to_ascii_lowercase();
        let b = expected.to_ascii_lowercase();
        a.strip_suffix(".exe").unwrap_or(&a) == b.strip_suffix(".exe").unwrap_or(&b)
    } else {
        actual == expected
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn ordered_rules_and_cidr_boundaries() {
        let set = RuleSet::from_lines(&[
            "DOMAIN-SUFFIX,Example.COM,S",
            "DOMAIN-KEYWORD,Needle,K",
            "IP-CIDR,2001:db8::3/127,V6",
            "IP-CIDR,128.0.0.1/1,V4",
            "PROCESS-NAME,Chrome,P",
            "MATCH,REJECT",
        ])
        .unwrap();
        for (domain, ip, tag) in [
            (Some("x.eXample.com"), None, Some("S")),
            (Some("notexample.com"), None, None),
            (Some("aNEEDLEb"), None, Some("K")),
            (None, Some("2001:db8::2"), Some("V6")),
            (None, Some("2001:db8::1"), None),
            (None, Some("255.255.255.255"), Some("V4")),
        ] {
            let ctx = RuleContext {
                domain,
                ip: ip.map(|s| s.parse().unwrap()),
                process_name: None,
            };
            let action = tag.map_or(RuleAction::Reject, |s| RuleAction::Proxy(s.into()));
            assert_eq!(set.evaluate(&ctx), Some(&action));
        }
        assert_eq!(process_matches("chrome.EXE", "Chrome"), cfg!(windows));
        for bad in [
            "MATCH",
            "IP-CIDR,::1/129,DIRECT",
            "IP-CIDR,0.0.0.0/33,DIRECT",
            "DOMAIN,,DIRECT",
            "DOMAIN,a,DIRECT,no-resolve",
        ] {
            assert!(RuleSet::from_lines(&[bad]).is_err());
        }
    }
}
