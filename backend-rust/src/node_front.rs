//! Node frontend policy; codecs and original resource graphs stay with the caller.
use std::collections::{HashSet, VecDeque};

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    pub op: String,
    pub path: String,
}

#[cfg(test)]
mod tests {
    use super::*;
    fn new(op: &str) -> Flow {
        Flow::new(Request {
            op: op.into(),
            ..Default::default()
        })
        .unwrap()
    }
    fn answer(f: &mut Flow, kind: &str, r: Reply) {
        assert_eq!(f.next().kind, kind);
        f.advance(r).unwrap();
    }
    fn ok(f: &mut Flow, kind: &str) {
        answer(f, kind, Reply::default());
    }
    fn names(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| (*s).into()).collect()
    }
    fn status(f: &Flow, n: i64) {
        assert_eq!(f.next().kind, "result");
        assert!(
            f.next().result.contains(&format!("\"status\":{n},")),
            "{}",
            f.next().result
        );
    }
    fn vmess_facts(f: &mut Flow, r: Reply) {
        answer(
            f,
            "vmess-input",
            Reply {
                flag: true,
                ..Default::default()
            },
        );
        ok(f, "vmess-strip");
        answer(
            f,
            "vmess-cut-facts",
            Reply {
                number: 9,
                ..Default::default()
            },
        );
        assert_eq!(f.next().number, 9);
        ok(f, "vmess-cut");
        ok(f, "vmess-base64");
        ok(f, "vmess-json");
        answer(f, "vmess-facts", r);
    }
    fn vmess_names() -> Vec<String> {
        names(&[
            "server", "", "1", "auto", "AUTO", "tls", "sni", "host", "chrome", "2",
        ])
    }

    #[test]
    fn dispatch_keeps_vmess_before_url_and_protocol_aliases() {
        let mut f = new("dispatch");
        answer(
            &mut f,
            "dispatch-facts",
            Reply {
                text: "vmess".into(),
                flag: true,
                number: 1,
                ..Default::default()
            },
        );
        assert_eq!(f.next().text, "vmess");
        answer(
            &mut f,
            "protocol",
            Reply {
                error: Some("same error".into()),
                ..Default::default()
            },
        );
        assert!(f.next().result.contains("same error"));
        for scheme in [
            "vless",
            "trojan",
            "hysteria",
            "hysteria2",
            "hy2",
            "tuic",
            "ss",
            "ssr",
        ] {
            let mut f = new("dispatch");
            answer(
                &mut f,
                "dispatch-facts",
                Reply {
                    text: scheme.into(),
                    flag: true,
                    number: 1,
                    ..Default::default()
                },
            );
            ok(&mut f, "url-parse");
            assert_eq!(f.next().text, scheme);
            ok(&mut f, "protocol");
            status(&f, 0);
        }
    }
    #[test]
    fn dispatch_failure_priority_and_unknown_scheme_parse() {
        let mut f = new("dispatch");
        answer(&mut f, "dispatch-facts", Reply::default());
        assert!(f
            .next()
            .result
            .contains(&quote(crate::node_front_text::TEXT[0])));
        let mut f = new("dispatch");
        answer(
            &mut f,
            "dispatch-facts",
            Reply {
                flag: true,
                number: 1,
                text: "unknown".into(),
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "url-parse",
            Reply {
                error: Some("bad".into()),
                ..Default::default()
            },
        );
        assert!(f
            .next()
            .result
            .contains(&quote(crate::node_front_text::TEXT[1])));
    }
    #[test]
    fn finish_fallback_and_error_are_ordered() {
        for fallback in [false, true] {
            let mut f = new("finish");
            answer(
                &mut f,
                "finish-facts",
                Reply {
                    flag: fallback,
                    ..Default::default()
                },
            );
            if fallback {
                ok(&mut f, "name-fallback")
            }
            answer(
                &mut f,
                "finish",
                Reply {
                    error: Some("original".into()),
                    ..Default::default()
                },
            );
            assert!(f.next().result.contains("original"));
        }
    }
    #[test]
    fn transport_aliases_masks_and_original_error() {
        for (input, output) in [
            ("ws", "ws"),
            ("websocket", "ws"),
            ("grpc", "grpc"),
            ("h2", "h2"),
            ("http", "h2"),
        ] {
            let mut f = new("transport");
            answer(
                &mut f,
                "transport-facts",
                Reply {
                    text: input.into(),
                    number: 7,
                    ..Default::default()
                },
            );
            assert_eq!(f.next(), &effect("transport-apply", output, "", 7));
            ok(&mut f, "transport-apply");
            status(&f, 0);
        }
        for input in ["", "tcp"] {
            let mut f = new("transport");
            answer(
                &mut f,
                "transport-facts",
                Reply {
                    text: input.into(),
                    ..Default::default()
                },
            );
            status(&f, 0);
        }
        let mut f = new("transport");
        answer(
            &mut f,
            "transport-facts",
            Reply {
                text: "invalid".into(),
                names: names(&["raw\" error"]),
                ..Default::default()
            },
        );
        assert!(f.next().result.contains(&quote("raw\" error")));
    }
    #[test]
    fn vmess_keeps_prefix_cut_and_ordered_fields() {
        let mut f = new("vmess");
        vmess_facts(
            &mut f,
            Reply {
                number: 443,
                names: vmess_names(),
                ..Default::default()
            },
        );
        ok(&mut f, "name-fallback");
        assert_eq!(f.next().text, "auto");
        ok(&mut f, "vmess-base");
        for (field, source) in [
            ("tls", ""),
            ("servername", "sni"),
            ("fingerprint", ""),
            ("alpn", ""),
        ] {
            assert_eq!(f.next(), &effect("vmess-patch", field, source, 0));
            ok(&mut f, "vmess-patch");
        }
        ok(&mut f, "vmess-transport");
        ok(&mut f, "vmess-finish");
        status(&f, 0);
    }
    #[test]
    fn vmess_legacy_handled_stops_without_duplicate_mutations() {
        let mut f = new("vmess");
        vmess_facts(
            &mut f,
            Reply {
                number: 443,
                flag: true,
                names: vmess_names(),
                ..Default::default()
            },
        );
        ok(&mut f, "name-fallback");
        answer(
            &mut f,
            "vmess-legacy",
            Reply {
                flag: true,
                error: Some("legacy error".into()),
                ..Default::default()
            },
        );
        status(&f, 0);
        assert!(f.next().result.contains("legacy error"));
    }
    #[test]
    fn vmess_legacy_unavailable_and_transport_failure() {
        let mut f = new("vmess");
        let mut v = vmess_names();
        v[1] = "named".into();
        v[3] = "aes".into();
        v[4] = "AES".into();
        v[5] = "false".into();
        v[8] = String::new();
        v[9] = "0".into();
        vmess_facts(
            &mut f,
            Reply {
                number: 443,
                flag: true,
                names: v,
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "vmess-legacy",
            Reply {
                error: Some("ignored when not handled".into()),
                ..Default::default()
            },
        );
        assert_eq!(f.next().text, "AES");
        ok(&mut f, "vmess-base");
        answer(
            &mut f,
            "vmess-transport",
            Reply {
                error: Some("unsupported transport".into()),
                ..Default::default()
            },
        );
        status(&f, 0);
        assert!(f.next().result.contains("unsupported transport"));
    }
    #[test]
    fn vmess_case_sensitive_prefix_no_cut_and_missing_fields() {
        let mut f = new("vmess");
        ok(&mut f, "vmess-input");
        answer(
            &mut f,
            "vmess-cut-facts",
            Reply {
                number: -1,
                ..Default::default()
            },
        );
        ok(&mut f, "vmess-base64");
        ok(&mut f, "vmess-json");
        answer(
            &mut f,
            "vmess-facts",
            Reply {
                number: 0,
                names: vmess_names(),
                ..Default::default()
            },
        );
        assert!(f
            .next()
            .result
            .contains(&quote(crate::node_front_text::TEXT[5])));
    }
    #[test]
    fn unique_retains_empty_name_and_first_order() {
        let mut f = new("unique");
        answer(
            &mut f,
            "unique-facts",
            Reply {
                names: names(&["", "a", "", "b", "a"]),
                ..Default::default()
            },
        );
        assert!(f.next().result.contains("\"indices\":[0,1,3]"));
        let mut f = new("unique");
        ok(&mut f, "unique-facts");
        assert!(f.next().result.contains("\"indices\":[]"));
    }
    #[test]
    fn yaml_nil_empty_and_bits_are_distinct() {
        let mut f = new("yaml");
        ok(&mut f, "yaml-facts");
        assert!(f.next().result.contains("\"indices\":null,\"bits\":null"));
        let mut f = new("yaml");
        answer(
            &mut f,
            "yaml-facts",
            Reply {
                number: 1,
                names: names(&["", "ss", "0", "bool", "1", "/", "0"]),
                ..Default::default()
            },
        );
        assert!(f.next().result.contains("\"indices\":[],\"bits\":[]"));
        let mut f = new("yaml");
        answer(
            &mut f,
            "yaml-facts",
            Reply {
                number: 3,
                names: names(&[
                    "one", "ss", "0", "bool", "1", "/", "1", "1", "1", "one", "ss", "0", "string",
                    "TLS", "", "0", "two", "ss", "0", "string", "false", "", "1", "1", "0",
                ]),
                ..Default::default()
            },
        );
        assert!(f.next().result.contains("\"indices\":[0,2],\"bits\":[7,0]"));
    }
    #[test]
    fn malformed_variable_facts_do_not_consume() {
        let mut f = new("yaml");
        let before = f.next().clone();
        assert_eq!(
            f.advance(Reply {
                number: 1,
                names: names(&["truncated"]),
                ..Default::default()
            }),
            Err(Error::Invalid)
        );
        assert_eq!(f.next(), &before);
        ok(&mut f, "yaml-facts");
        let mut f = new("vmess");
        ok(&mut f, "vmess-input");
        answer(
            &mut f,
            "vmess-cut-facts",
            Reply {
                number: -1,
                ..Default::default()
            },
        );
        ok(&mut f, "vmess-base64");
        ok(&mut f, "vmess-json");
        let before = f.next().clone();
        assert_eq!(f.advance(Reply::default()), Err(Error::Invalid));
        assert_eq!(f.next(), &before);
    }
    #[test]
    fn matches_reobserves_subscription_and_preserves_source() {
        let mut f = new("matches");
        ok(&mut f, "match-read");
        answer(
            &mut f,
            "match-nodes",
            Reply {
                number: 2,
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "match-source",
            Reply {
                text: "hash-a".into(),
                ..Default::default()
            },
        );
        ok(&mut f, "subscription");
        answer(
            &mut f,
            "subscription",
            Reply {
                number: 1,
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "hash",
            Reply {
                text: "hash-a".into(),
                ..Default::default()
            },
        );
        status(&f, 1);
        let mut f = new("matches");
        ok(&mut f, "match-read");
        answer(
            &mut f,
            "match-nodes",
            Reply {
                number: 1,
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "match-source",
            Reply {
                text: "__merged__".into(),
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "subscription",
            Reply {
                number: 1,
                ..Default::default()
            },
        );
        status(&f, 1);
    }
    #[test]
    fn matches_read_failure_and_parse_profile_status() {
        let mut f = new("matches");
        answer(
            &mut f,
            "match-read",
            Reply {
                error: Some("missing".into()),
                ..Default::default()
            },
        );
        status(&f, 0);
        for failed in [false, true] {
            let mut f = new("parse-profile");
            answer(
                &mut f,
                "convert-profile",
                Reply {
                    error: failed.then(|| "invalid".into()),
                    ..Default::default()
                },
            );
            status(&f, i64::from(!failed));
            assert!(f.next().result.contains("\"hasError\":false"));
        }
    }
    #[test]
    fn validate_inline_and_remote_order() {
        let mut f = new("validate");
        answer(
            &mut f,
            "validate-facts",
            Reply {
                flag: true,
                ..Default::default()
            },
        );
        ok(&mut f, "inline");
        answer(
            &mut f,
            "categorize",
            Reply {
                number: 2,
                ..Default::default()
            },
        );
        assert_eq!(f.next().number, 0);
        answer(
            &mut f,
            "validate-url",
            Reply {
                text: "https".into(),
                flag: true,
                ..Default::default()
            },
        );
        assert_eq!(f.next().number, 1);
        answer(
            &mut f,
            "validate-url",
            Reply {
                text: "http".into(),
                flag: true,
                ..Default::default()
            },
        );
        status(&f, 0);
        let mut f = new("validate");
        answer(
            &mut f,
            "validate-facts",
            Reply {
                flag: true,
                ..Default::default()
            },
        );
        answer(
            &mut f,
            "inline",
            Reply {
                flag: true,
                ..Default::default()
            },
        );
        status(&f, 0);
    }
    #[test]
    fn validate_rejects_scheme_host_and_empty_without_recovery() {
        for (scheme, host) in [("ftp", true), ("https", false)] {
            let mut f = new("validate");
            answer(
                &mut f,
                "validate-facts",
                Reply {
                    flag: true,
                    ..Default::default()
                },
            );
            ok(&mut f, "inline");
            answer(
                &mut f,
                "categorize",
                Reply {
                    number: 1,
                    ..Default::default()
                },
            );
            answer(
                &mut f,
                "validate-url",
                Reply {
                    text: scheme.into(),
                    flag: host,
                    ..Default::default()
                },
            );
            assert!(f
                .next()
                .result
                .contains(&quote(crate::node_front_text::TEXT[6])));
        }
        let mut f = new("validate");
        ok(&mut f, "validate-facts");
        assert_eq!(f.advance(Reply::default()), Err(Error::Done));
        assert!(matches!(
            Flow::new(Request {
                op: "unknown".into(),
                ..Default::default()
            }),
            Err(Error::Invalid)
        ));
    }
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Effect {
    pub kind: String,
    pub text: String,
    pub other: String,
    pub number: i64,
    pub result: String,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Reply {
    pub text: String,
    pub number: i64,
    pub flag: bool,
    pub names: Vec<String>,
    pub bytes: Vec<u8>,
    pub error: Option<String>,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}

fn quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c < ' ' => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}
fn array(v: &[usize]) -> String {
    format!(
        "[{}]",
        v.iter().map(usize::to_string).collect::<Vec<_>>().join(",")
    )
}
fn effect(k: &str, t: &str, o: &str, n: i64) -> Effect {
    Effect {
        kind: k.into(),
        text: t.into(),
        other: o.into(),
        number: n,
        result: String::new(),
    }
}

pub struct Flow {
    op: String,
    pc: u8,
    effect: Effect,
    remembered: String,
    queue: VecDeque<Effect>,
    limit: i64,
    index: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        let first = match q.op.as_str() {
            "dispatch" => "dispatch-facts",
            "finish" => "finish-facts",
            "transport" => "transport-facts",
            "vmess" => "vmess-input",
            "unique" => "unique-facts",
            "yaml" => "yaml-facts",
            "parse-profile" => "convert-profile",
            "matches" => "match-read",
            "validate" => "validate-facts",
            _ => return Err(Error::Invalid),
        };
        Ok(Self {
            op: q.op,
            pc: 0,
            effect: effect(first, "", "", 0),
            remembered: String::new(),
            queue: VecDeque::new(),
            limit: 0,
            index: 0,
        })
    }
    pub fn next(&self) -> &Effect {
        &self.effect
    }
    fn emit(&mut self, k: &str, t: &str, o: &str, n: i64) {
        self.effect = effect(k, t, o, n)
    }
    fn finish(&mut self, status: i64, data: &str, error: Option<String>) {
        self.pc = 255;
        self.effect=Effect{kind:"result".into(),result:format!("{{\"status\":{status},\"text\":\"\",\"data\":{data},\"error\":{},\"hasError\":{}}}",quote(error.as_deref().unwrap_or("")),error.is_some()),..Default::default()};
    }
    fn fail(&mut self, n: usize) {
        self.finish(0, "null", Some(crate::node_front_text::TEXT[n].into()))
    }
    fn queued(&mut self) {
        if let Some(e) = self.queue.pop_front() {
            self.effect = e
        } else {
            self.finish(0, "null", None)
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.pc == 255 {
            return Err(Error::Done);
        }
        match self.op.as_str() {
            "dispatch" => match self.pc {
                0 => {
                    if !r.flag {
                        self.fail(0)
                    } else if r.number == 0 {
                        self.fail(4)
                    } else if r.text == "vmess" {
                        self.pc = 2;
                        self.emit("protocol", "vmess", "", 0)
                    } else {
                        self.remembered = r.text;
                        self.pc = 1;
                        self.emit("url-parse", "", "", 0)
                    }
                }
                1 => {
                    if r.error.is_some() {
                        self.fail(1)
                    } else if matches!(
                        self.remembered.as_str(),
                        "vless"
                            | "trojan"
                            | "hysteria2"
                            | "hy2"
                            | "hysteria"
                            | "tuic"
                            | "ss"
                            | "ssr"
                    ) {
                        self.pc = 2;
                        let s = self.remembered.clone();
                        self.emit("protocol", &s, "", 0)
                    } else {
                        self.fail(4)
                    }
                }
                2 => self.finish(0, "null", r.error),
                _ => return Err(Error::Invalid),
            },
            "finish" => match self.pc {
                0 => {
                    self.pc = if r.flag { 1 } else { 2 };
                    self.emit(if r.flag { "name-fallback" } else { "finish" }, "", "", 0)
                }
                1 => {
                    self.pc = 2;
                    self.emit("finish", "", "", 0)
                }
                2 => self.finish(0, "null", r.error),
                _ => return Err(Error::Invalid),
            },
            "transport" => match self.pc {
                0 => {
                    let k = match r.text.as_str() {
                        "" | "tcp" => {
                            self.finish(0, "null", None);
                            return Ok(());
                        }
                        "ws" | "websocket" => "ws",
                        "grpc" => "grpc",
                        "h2" | "http" => "h2",
                        _ => {
                            let e = r.names.first().ok_or(Error::Invalid)?.clone();
                            self.finish(0, "null", Some(e));
                            return Ok(());
                        }
                    };
                    self.pc = 1;
                    self.emit("transport-apply", k, "", r.number)
                }
                1 => self.finish(0, "null", r.error),
                _ => return Err(Error::Invalid),
            },
            "vmess" => match self.pc {
                0 => {
                    self.pc = if r.flag { 10 } else { 11 };
                    self.emit(
                        if r.flag {
                            "vmess-strip"
                        } else {
                            "vmess-cut-facts"
                        },
                        "",
                        "",
                        0,
                    )
                }
                10 => {
                    self.pc = 11;
                    self.emit("vmess-cut-facts", "", "", 0)
                }
                11 => {
                    self.pc = if r.number >= 0 { 12 } else { 13 };
                    if r.number >= 0 {
                        self.emit("vmess-cut", "", "", r.number)
                    } else {
                        self.emit("vmess-base64", "", "", 0)
                    }
                }
                12 => {
                    self.pc = 13;
                    self.emit("vmess-base64", "", "", 0)
                }
                13 => {
                    if r.error.is_some() {
                        self.fail(2)
                    } else {
                        self.pc = 1;
                        self.emit("vmess-json", "", "", 0)
                    }
                }
                1 => {
                    if r.error.is_some() {
                        self.fail(3)
                    } else {
                        self.pc = 2;
                        self.emit("vmess-facts", "", "", 0)
                    }
                }
                2 => {
                    if r.names.len() != 10 {
                        return Err(Error::Invalid);
                    }
                    let v = &r.names;
                    if v[0].is_empty() || r.number == 0 || v[2] == "0" {
                        self.fail(5);
                        return Ok(());
                    }
                    let mut actions = VecDeque::new();
                    if v[1].is_empty() {
                        actions.push_back(effect("name-fallback", "", "", 0))
                    }
                    if r.flag {
                        actions.push_back(effect("vmess-legacy", "", "", 0))
                    }
                    let cipher = if v[3].is_empty() || v[3] == "auto" {
                        "auto"
                    } else {
                        &v[4]
                    };
                    actions.push_back(effect("vmess-base", cipher, "", 0));
                    if !v[5].is_empty() && v[5] != "none" && v[5] != "false" {
                        actions.push_back(effect("vmess-patch", "tls", "", 0));
                        if !v[6].is_empty() {
                            actions.push_back(effect("vmess-patch", "servername", "sni", 0))
                        } else if !v[7].is_empty() {
                            actions.push_back(effect("vmess-patch", "servername", "host", 0))
                        }
                    }
                    if !v[8].is_empty() {
                        actions.push_back(effect("vmess-patch", "fingerprint", "", 0))
                    }
                    if v[9] != "0" {
                        actions.push_back(effect("vmess-patch", "alpn", "", 0))
                    }
                    actions.push_back(effect("vmess-transport", "", "", 0));
                    actions.push_back(effect("vmess-finish", "", "", 0));
                    self.queue = actions;
                    self.pc = 3;
                    self.queued();
                }
                3 => {
                    match self.effect.kind.as_str() {
                        "vmess-legacy" if r.flag => {
                            self.finish(0, "null", r.error);
                            return Ok(());
                        }
                        "vmess-transport" if r.error.is_some() => {
                            self.finish(0, "null", r.error);
                            return Ok(());
                        }
                        "vmess-finish" => {
                            self.finish(0, "null", r.error);
                            return Ok(());
                        }
                        _ => {}
                    }
                    self.queued()
                }
                _ => return Err(Error::Invalid),
            },
            "unique" => {
                let mut seen = HashSet::new();
                let indices: Vec<_> = r
                    .names
                    .iter()
                    .enumerate()
                    .filter_map(|(i, n)| seen.insert(n).then_some(i))
                    .collect();
                self.finish(0, &format!("{{\"indices\":{}}}", array(&indices)), None)
            }
            "yaml" => {
                if r.error.is_some() || r.number <= 0 {
                    self.finish(0, "{\"indices\":null,\"bits\":null}", None);
                    return Ok(());
                }
                let mut cur = 0usize;
                let mut indices = Vec::new();
                let mut bits = Vec::new();
                let mut seen = HashSet::new();
                let mut take = || {
                    let s = r.names.get(cur).ok_or(Error::Invalid)?;
                    cur += 1;
                    Ok::<_, Error>(s.as_str())
                };
                for i in 0..r.number {
                    let name = take()?;
                    let kind = take()?;
                    let info = take()?;
                    let tls_kind = take()?;
                    let tls_value = take()?;
                    let path = take()?;
                    let count = take()?.parse::<usize>().map_err(|_| Error::Invalid)?;
                    let mut host = false;
                    for _ in 0..count {
                        let folded = take()?;
                        let nonempty = take()?;
                        host |= folded == "1" && nonempty == "1";
                    }
                    if name.is_empty() || kind.is_empty() || info == "1" || !seen.insert(name) {
                        continue;
                    }
                    let mut bit = 0;
                    if tls_kind == "bool" && tls_value == "1"
                        || tls_kind == "string" && !matches!(tls_value, "" | "none" | "false" | "0")
                    {
                        bit |= 1
                    }
                    if host {
                        bit |= 2
                    }
                    if !path.is_empty() {
                        bit |= 4
                    }
                    indices.push(i as usize);
                    bits.push(bit);
                }
                if cur != r.names.len() {
                    return Err(Error::Invalid);
                }
                self.finish(
                    0,
                    &format!(
                        "{{\"indices\":{},\"bits\":{}}}",
                        array(&indices),
                        array(&bits)
                    ),
                    None,
                )
            }
            "parse-profile" => self.finish(i64::from(r.error.is_none()), "null", None),
            "matches" => match self.pc {
                0 => {
                    if r.error.is_some() {
                        self.finish(0, "null", None)
                    } else {
                        self.pc = 1;
                        self.emit("match-nodes", "", "", 0)
                    }
                }
                1 => {
                    if r.number == 0 {
                        self.finish(0, "null", None)
                    } else {
                        self.pc = 2;
                        self.emit("match-source", "", "", 0)
                    }
                }
                2 => {
                    if r.error.is_some() {
                        self.finish(0, "null", None)
                    } else {
                        self.remembered = r.text;
                        self.pc = 3;
                        self.emit("subscription", "", "", 0)
                    }
                }
                3 => {
                    if r.number == 1 {
                        self.finish(i64::from(self.remembered == "__merged__"), "null", None)
                    } else {
                        self.pc = 4;
                        self.emit("subscription", "", "", 0)
                    }
                }
                4 => {
                    self.pc = 5;
                    self.emit("hash", "", "", 0)
                }
                5 => self.finish(i64::from(self.remembered == r.text), "null", None),
                _ => return Err(Error::Invalid),
            },
            "validate" => match self.pc {
                0 => {
                    if !r.flag {
                        self.fail(6)
                    } else if r.number == 1 {
                        self.finish(0, "null", None)
                    } else {
                        self.pc = 1;
                        self.emit("inline", "", "", 0)
                    }
                }
                1 => {
                    if r.flag {
                        self.finish(0, "null", None)
                    } else {
                        self.pc = 2;
                        self.emit("categorize", "", "", 0)
                    }
                }
                2 => {
                    if r.number == 0 && !r.flag {
                        self.fail(6)
                    } else if r.number <= 0 {
                        self.finish(0, "null", None)
                    } else {
                        self.limit = r.number;
                        self.index = 0;
                        self.pc = 3;
                        self.emit("validate-url", "", "", 0)
                    }
                }
                3 => {
                    if r.error.is_some() || !matches!(r.text.as_str(), "http" | "https") || !r.flag
                    {
                        self.fail(6)
                    } else {
                        self.index += 1;
                        if self.index == self.limit {
                            self.finish(0, "null", None)
                        } else {
                            self.emit("validate-url", "", "", self.index)
                        }
                    }
                }
                _ => return Err(Error::Invalid),
            },
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}
