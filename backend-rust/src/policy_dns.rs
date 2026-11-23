//! Policy resolution and DNS filtering over narrow, codec-only facts.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    pub op: String,
    pub path: String,
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

use crate::policy_dns_text::*;
#[derive(Clone, Debug)]
enum J {
    Null,
    Bool(bool),
    Num(i64),
    Arr(Vec<J>),
    Str(String),
    Obj(Vec<(String, J)>),
}
fn quote(s: &str) -> String {
    let mut o = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => o.push_str("\\\""),
            '\\' => o.push_str("\\\\"),
            '\n' => o.push_str("\\n"),
            '\r' => o.push_str("\\r"),
            '\t' => o.push_str("\\t"),
            c if c < ' ' => o.push_str(&format!("\\u{:04x}", c as u32)),
            c => o.push(c),
        }
    }
    o.push('"');
    o
}
impl J {
    fn json(&self) -> String {
        match self {
            Self::Null => "null".into(),
            Self::Bool(v) => v.to_string(),
            Self::Num(v) => v.to_string(),
            Self::Str(v) => quote(v),
            Self::Arr(v) => format!(
                "[{}]",
                v.iter().map(Self::json).collect::<Vec<_>>().join(",")
            ),
            Self::Obj(v) => format!(
                "{{{}}}",
                v.iter()
                    .map(|(k, v)| format!("{}:{}", quote(k), v.json()))
                    .collect::<Vec<_>>()
                    .join(",")
            ),
        }
    }
}
fn st(v: impl Into<String>) -> J {
    J::Str(v.into())
}
fn obj(v: Vec<(&str, J)>) -> J {
    J::Obj(v.into_iter().map(|(k, v)| (k.into(), v)).collect())
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Done,
}
#[derive(Default)]
struct Outcome {
    status: i64,
    text: String,
    data: Option<J>,
    error: Option<String>,
}
impl Outcome {
    fn fail(status: i64, e: String) -> Self {
        Self {
            status,
            error: Some(e),
            ..Default::default()
        }
    }
    fn json(self) -> String {
        obj(vec![
            ("status", J::Num(self.status)),
            ("text", st(self.text)),
            ("data", self.data.unwrap_or(J::Null)),
            ("hasError", J::Bool(self.error.is_some())),
            ("error", st(self.error.unwrap_or_default())),
        ])
        .json()
    }
}
use std::collections::{BTreeMap, BTreeSet};
struct Facts<'a> {
    v: &'a [String],
    i: usize,
}
impl Facts<'_> {
    fn text(&mut self) -> Result<String, Error> {
        let v = self.v.get(self.i).ok_or(Error::Invalid)?.clone();
        self.i += 1;
        Ok(v)
    }
    fn num(&mut self) -> Result<usize, Error> {
        let v = self.text()?.parse::<usize>().map_err(|_| Error::Invalid)?;
        if v > 100000 {
            return Err(Error::Limit);
        }
        Ok(v)
    }
    fn list(&mut self) -> Result<Vec<String>, Error> {
        let n = self.num()?;
        (0..n).map(|_| self.text()).collect()
    }
}
fn suffix(h: &str, ip: &str) -> Option<String> {
    if h.is_empty() || h.len() > 253 || ip == "1" {
        return None;
    }
    let p: Vec<_> = h.split('.').collect();
    for s in &p {
        if s.is_empty()
            || s.len() > 63
            || s.starts_with('-')
            || s.ends_with('-')
            || !s
                .bytes()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-')
        {
            return None;
        }
    }
    if p.len() < 2 {
        return None;
    }
    Some(format!("+.{}", p[p.len() - 2..].join(".")))
}
#[derive(Clone)]
struct Group {
    used: bool,
    members: Vec<String>,
}
fn resolve(name: &str, targets: &mut BTreeMap<String, String>, groups: &BTreeMap<String, Group>) {
    struct Frame {
        name: String,
        next: usize,
        result: Option<String>,
    }
    let mut stack = vec![Frame {
        name: name.into(),
        next: 0,
        result: None,
    }];
    let mut visiting = BTreeSet::new();
    let mut returned: Option<String> = None;
    while let Some(f) = stack.last_mut() {
        if let Some(v) = returned.take() {
            if f.result.is_none() {
                f.result = Some(v)
            } else if f.result.as_ref() != Some(&v) {
                f.result = Some(String::new())
            }
        }
        if f.next == 0 && f.result.is_none() {
            if let Some(v) = targets.get(&f.name) {
                returned = Some(v.clone());
                stack.pop();
                continue;
            }
            let g = groups.get(&f.name);
            if visiting.contains(&f.name) || g.is_none_or(|g| g.used || g.members.is_empty()) {
                returned = Some(String::new());
                stack.pop();
                continue;
            }
            visiting.insert(f.name.clone());
        }
        let g = groups.get(&f.name).unwrap();
        if f.next == g.members.len() || f.result.as_ref().is_some_and(String::is_empty) {
            let v = f.result.clone().unwrap_or_default();
            targets.insert(f.name.clone(), v.clone());
            visiting.remove(&f.name);
            stack.pop();
            returned = Some(v);
            continue;
        }
        let child = g.members[f.next].clone();
        f.next += 1;
        stack.push(Frame {
            name: child,
            next: 0,
            result: None,
        });
    }
}
fn targets(s: &mut Facts) -> Result<BTreeMap<String, String>, Error> {
    let mut t = BTreeMap::from([
        ("DIRECT".into(), "DIRECT".into()),
        ("REJECT".into(), "REJECT".into()),
        ("SmartVPN".into(), "SmartVPN".into()),
    ]);
    for _ in 0..s.num()? {
        t.insert(s.text()?, "SmartVPN".into());
    }
    let mut groups = BTreeMap::new();
    for _ in 0..s.num()? {
        let name = s.text()?;
        let used = s.num()? > 0;
        let all = s.text()? == "1";
        let members = s.list()?;
        groups.insert(
            name,
            Group {
                used: used || all,
                members,
            },
        );
    }
    for name in s.list()? {
        resolve(&name, &mut t, &groups)
    }
    Ok(t)
}
struct Rule {
    raw: String,
    parts: Vec<String>,
    family: String,
    upper: String,
}
fn rules(s: &mut Facts) -> Result<Vec<Rule>, Error> {
    (0..s.num()?)
        .map(|_| {
            Ok(Rule {
                raw: s.text()?,
                parts: s.list()?,
                family: s.text()?,
                upper: s.text()?,
            })
        })
        .collect()
}
fn strings(v: Vec<String>) -> J {
    J::Arr(v.into_iter().map(st).collect())
}
fn supported(s: &mut Facts) -> Result<(J, J), Error> {
    let t = targets(s)?;
    let mut preview = vec![];
    let mut active = vec![];
    for r in rules(s)? {
        let mut p = r.parts;
        let mut effective = String::new();
        if p.len() == 3 || (p.len() == 4 && p[3] == "no-resolve") {
            let valid = match p[0].as_str() {
                "DOMAIN" | "DOMAIN-SUFFIX" | "DOMAIN-KEYWORD" => {
                    p.len() == 3 && !p[1].is_empty() && !p[1].contains([' ', '\r', '\n', '\t'])
                }
                "IP-CIDR" => r.family == "4",
                "IP-CIDR6" => r.family == "6",
                _ => false,
            };
            if let Some(v) = t.get(&p[2]) {
                if valid && !v.is_empty() {
                    p[2] = v.clone();
                    effective = p.join(",")
                }
            }
        }
        if !effective.is_empty() {
            active.push(effective.clone())
        }
        let mut row = vec![("source", st(r.raw))];
        if !effective.is_empty() {
            row.push(("applied", st(effective)))
        }
        preview.push(obj(row));
    }
    Ok((
        if preview.is_empty() {
            J::Null
        } else {
            J::Arr(preview)
        },
        if active.is_empty() {
            J::Null
        } else {
            strings(active)
        },
    ))
}
fn analyze(op: &str, r: &Reply) -> Result<Outcome, Error> {
    let mut s = Facts { v: &r.names, i: 0 };
    let mut out = Outcome::default();
    let data = match op {
        "targets" => obj(vec![(
            "targets",
            J::Obj(
                targets(&mut s)?
                    .into_iter()
                    .map(|(k, v)| (k, st(v)))
                    .collect(),
            ),
        )]),
        "rules" => {
            let (p, a) = supported(&mut s)?;
            obj(vec![("preview", p), ("active", a)])
        }
        "subrules" => {
            let mut result = vec![];
            if r.error.is_none() {
                let list = rules(&mut s)?;
                let reject = s.list()?;
                let domestic = s.list()?;
                for r in list {
                    let p = r.parts;
                    if p.len() < 3
                        || !matches!(
                            p[0].as_str(),
                            "DOMAIN" | "DOMAIN-SUFFIX" | "DOMAIN-KEYWORD" | "IP-CIDR" | "IP-CIDR6"
                        )
                        || p[1].is_empty()
                        || p[1].contains(['\r', '\n'])
                        || r.upper == "PASS"
                    {
                        continue;
                    }
                    let target = if reject.iter().any(|h| r.upper.contains(h)) {
                        "REJECT"
                    } else if domestic.iter().any(|h| r.upper.contains(h)) {
                        "DIRECT"
                    } else {
                        "SmartVPN"
                    };
                    let mut clean = format!("{},{},{}", p[0], p[1], target);
                    if p.len() > 3 && p[3] == "no-resolve" {
                        clean.push_str(",no-resolve")
                    }
                    result.push(clean);
                    if result.len() == 3000 {
                        break;
                    }
                }
            }
            obj(vec![(
                "rules",
                if r.error.is_some() {
                    J::Null
                } else {
                    strings(result)
                },
            )])
        }
        "suffix" => {
            let h = s.text()?;
            let ip = s.text()?;
            let value = suffix(&h, &ip);
            out.text = value.clone().unwrap_or_default();
            obj(vec![("ok", J::Bool(value.is_some()))])
        }
        "merge" => {
            let mut set = BTreeSet::new();
            for _ in 0..s.num()? {
                for e in s.list()? {
                    if !e.is_empty() {
                        set.insert(e);
                    }
                }
            }
            obj(vec![("domains", strings(set.into_iter().collect()))])
        }
        "domains" => {
            let mut set = BTreeSet::new();
            for _ in 0..s.num()? {
                let line = s.text()?;
                let h = s.text()?;
                let ip = s.text()?;
                if line.starts_with("server:") {
                    if let Some(e) = suffix(&h, &ip) {
                        set.insert(e);
                        if set.len() >= 256 {
                            break;
                        }
                    }
                }
            }
            obj(vec![("domains", strings(set.into_iter().collect()))])
        }
        "hostname" => {
            if r.error.is_none() {
                for _ in 0..s.num()? {
                    let host = s.text()?;
                    let h = s.text()?;
                    let ip = s.text()?;
                    let original = s.text()?;
                    if !host.is_empty() && original != "1" && suffix(&h, &ip).is_some() {
                        out.text = host;
                        break;
                    }
                }
            }
            obj(vec![])
        }
        "entrydns" => {
            let mut map: BTreeMap<String, Vec<String>> = BTreeMap::new();
            let mut exists = false;
            if r.error.is_none() {
                let count = s.num()?;
                exists = count > 0;
                for _ in 0..s.num()? {
                    let host = s.text()?;
                    let h = s.text()?;
                    let ip = s.text()?;
                    let valid = suffix(&h, &ip).is_some();
                    let duplicate = map.contains_key(&host);
                    for _ in 0..s.num()? {
                        let isstring = s.text()?;
                        let raw = s.text()?;
                        let scheme = s.text()?;
                        let hostname = s.text()?;
                        let user = s.text()?;
                        let fragment = s.text()?;
                        if count > 0
                            && valid
                            && !duplicate
                            && isstring == "1"
                            && scheme == "https"
                            && !hostname.is_empty()
                            && user == "0"
                            && fragment.is_empty()
                        {
                            map.entry(host.clone()).or_default().push(raw);
                        }
                    }
                }
            }
            obj(vec![(
                "policy",
                if exists {
                    J::Obj(map.into_iter().map(|(k, v)| (k, strings(v))).collect())
                } else {
                    J::Null
                },
            )])
        }
        _ => return Err(Error::Invalid),
    };
    out.data = Some(data);
    Ok(out)
}
#[derive(Clone)]
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u8,
    out: Option<J>,
    source_hash: String,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if !matches!(
            q.op.as_str(),
            "http"
                | "targets"
                | "rules"
                | "subrules"
                | "suffix"
                | "merge"
                | "domains"
                | "hostname"
                | "entrydns"
        ) {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            out: None,
            source_hash: String::new(),
        };
        if f.q.op == "http" {
            f.emit("lock", 1)
        } else {
            f.source()
        }
        Ok(f)
    }
    fn emit(&mut self, k: &str, pc: u8) {
        self.e = Effect {
            kind: k.into(),
            ..Default::default()
        };
        self.pc = pc
    }
    fn source(&mut self) {
        self.emit("source", 5);
        self.e.text = if self.q.op == "http" {
            "rules".into()
        } else {
            self.q.op.clone()
        }
    }
    fn finish(&mut self, o: Outcome) {
        self.emit("result", 0);
        self.e.result = o.json()
    }
    pub fn next(&self) -> &Effect {
        &self.e
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.e.kind == "done" {
            return Err(Error::Done);
        }
        if self.e.kind == "result" {
            self.emit("done", 0);
            return Ok(());
        }
        match self.pc {
            1 => self.emit("subscription", 2),
            2 => {
                if !r.flag {
                    self.source()
                } else {
                    self.emit("match-read", 10)
                }
            }
            10 => {
                if r.error.is_some() {
                    self.source()
                } else {
                    self.emit("match-nodes", 11)
                }
            }
            11 => {
                if r.number == 0 {
                    self.source()
                } else {
                    self.emit("match-source", 12)
                }
            }
            12 => {
                if r.error.is_some() {
                    self.source()
                } else {
                    self.source_hash = r.text;
                    self.emit("subscription", 13)
                }
            }
            13 => {
                if r.number == 1 {
                    if self.source_hash == "__merged__" {
                        self.emit("read-policy", 4)
                    } else {
                        self.source()
                    }
                } else {
                    self.emit("subscription", 14)
                }
            }
            14 => self.emit("hash", 15),
            15 => {
                if self.source_hash == r.text {
                    self.emit("read-policy", 4)
                } else {
                    self.source()
                }
            }
            4 => {
                if r.error.is_some() {
                    self.finish(Outcome::fail(400, READ_ERROR.into()))
                } else {
                    self.source()
                }
            }
            5 => {
                let mut o = analyze(
                    if self.q.op == "http" {
                        "rules"
                    } else {
                        &self.q.op
                    },
                    &r,
                )?;
                if self.q.op == "http" {
                    if let Some(J::Obj(v)) = o.data.take() {
                        self.out = Some(v.into_iter().find(|(k, _)| k == "preview").unwrap().1);
                    }
                    self.emit("enabled", 6)
                } else {
                    self.finish(o)
                }
            }
            6 => {
                self.e.number = i64::from(r.flag);
                self.emit("running", if r.flag { 8 } else { 7 })
            }
            7 | 8 => {
                let p = self.out.take().unwrap();
                self.finish(Outcome {
                    data: Some(obj(vec![
                        ("rules", p),
                        ("enabled", J::Bool(self.pc == 8)),
                        ("running", J::Bool(r.flag)),
                    ])),
                    ..Default::default()
                })
            }
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn facts(v: &[&str]) -> Reply {
        Reply {
            names: v.iter().map(|s| s.to_string()).collect(),
            ..Default::default()
        }
    }
    fn result(op: &str, v: &[&str]) -> String {
        analyze(op, &facts(v)).unwrap().json()
    }
    #[test]
    fn suffix_validation_and_last_two_labels() {
        assert_eq!(suffix("a.b.co.uk", "0"), Some("+.co.uk".into()));
        for h in [
            "",
            "localhost",
            "-a.com",
            "a-.com",
            "a..com",
            "A.com",
            "é.com",
        ] {
            assert!(suffix(h, "0").is_none())
        }
        assert!(suffix("1.2.3.4", "1").is_none());
        assert!(suffix(&format!("{}.com", "a".repeat(64)), "0").is_none());
    }
    #[test]
    fn builtin_proxy_override_and_heterogeneous_groups() {
        let r = result(
            "targets",
            &[
                "1", "DIRECT", "3", "all", "0", "0", "2", "DIRECT", "SmartVPN", "mix", "0", "0",
                "2", "REJECT", "SmartVPN", "skip", "1", "0", "1", "DIRECT", "3", "all", "mix",
                "skip",
            ],
        );
        assert!(r.contains("\"DIRECT\":\"SmartVPN\""));
        assert!(r.contains("\"all\":\"SmartVPN\""));
        assert!(r.contains("\"mix\":\"\""));
        assert!(!r.contains("\"skip\":"));
    }
    #[test]
    fn cycles_and_order_cache_empty() {
        let r = result(
            "targets",
            &[
                "0", "2", "a", "0", "0", "1", "b", "b", "0", "0", "1", "a", "2", "a", "b",
            ],
        );
        assert!(r.contains("\"a\":\"\""));
        assert!(r.contains("\"b\":\"\""));
    }
    #[test]
    fn deep_graph_explicit_stack() {
        let mut v = vec!["0".into(), "10000".into()];
        for i in 0..10000 {
            v.extend([
                format!("g{i}"),
                "0".into(),
                "0".into(),
                "1".into(),
                if i == 9999 {
                    "DIRECT".into()
                } else {
                    format!("g{}", i + 1)
                },
            ])
        }
        v.extend(["1".into(), "g0".into()]);
        let o = analyze(
            "targets",
            &Reply {
                names: v,
                ..Default::default()
            },
        )
        .unwrap()
        .json();
        assert!(o.contains("\"g0\":\"DIRECT\""));
    }
    #[test]
    fn supported_noresolve_and_empty_shape() {
        let r = result(
            "rules",
            &[
                "0",
                "0",
                "0",
                "2",
                "original1",
                "3",
                "DOMAIN",
                "example.com",
                "DIRECT",
                "",
                "",
                "original2",
                "4",
                "DOMAIN",
                "example.com",
                "DIRECT",
                "no-resolve",
                "",
                "",
            ],
        );
        assert!(r.contains("\"applied\":\"DOMAIN,example.com,DIRECT\""));
        assert!(r.contains("{\"source\":\"original2\"}"));
        let r = result("rules", &["0", "0", "0", "0"]);
        assert!(r.contains("\"preview\":null"));
        assert!(r.contains("\"active\":null"));
    }
    #[test]
    fn cidr_family_required_but_subrules_accept_legacy() {
        let r = result(
            "rules",
            &[
                "0", "0", "0", "1", "raw", "3", "IP-CIDR", "bad", "DIRECT", "", "",
            ],
        );
        assert!(!r.contains("applied"));
        let r = result(
            "subrules",
            &[
                "1", "raw", "3", "IP-CIDR", "bad", "anything", "", "DIRECT", "0", "1", "DIRECT",
            ],
        );
        assert!(r.contains("IP-CIDR,bad,DIRECT"));
    }
    #[test]
    fn subrules_reject_priority_pass_and_error_nil() {
        let r = result(
            "subrules",
            &[
                "2",
                "one",
                "3",
                "DOMAIN",
                "x",
                "direct-reject",
                "",
                "DIRECT-REJECT",
                "two",
                "3",
                "DOMAIN",
                "y",
                "pass",
                "",
                "PASS",
                "1",
                "REJECT",
                "1",
                "DIRECT",
            ],
        );
        assert!(r.contains("DOMAIN,x,REJECT"));
        assert!(!r.contains("DOMAIN,y"));
        let r = analyze(
            "subrules",
            &Reply {
                error: Some("bad".into()),
                ..Default::default()
            },
        )
        .unwrap()
        .json();
        assert!(r.contains("\"rules\":null"));
    }
    #[test]
    fn merge_and_domains_order_dedup() {
        let r = result("merge", &["2", "3", "b", "a", "", "2", "b", "c"]);
        assert!(r.contains("[\"a\",\"b\",\"c\"]"));
        let r = result(
            "domains",
            &[
                "3",
                "server:x",
                "x.example.com",
                "0",
                "not-server:",
                "other.com",
                "0",
                "server:z",
                "z.example.com",
                "0",
            ],
        );
        assert!(r.contains("[\"+.example.com\"]"));
    }
    #[test]
    fn hostname_returns_original_case() {
        let r = result(
            "hostname",
            &[
                "2",
                "1.2.3.4",
                "1.2.3.4",
                "1",
                "1",
                "Example.COM",
                "example.com",
                "0",
                "0",
            ],
        );
        assert!(r.contains("\"text\":\"Example.COM\""));
    }
    #[test]
    fn entrydns_duplicate_host_keeps_first_successful_group() {
        let r = result(
            "entrydns",
            &[
                "1",
                "2",
                "example.com",
                "example.com",
                "0",
                "2",
                "1",
                "https://a/",
                "https",
                "a",
                "0",
                "",
                "1",
                "https://b/",
                "https",
                "b",
                "0",
                "",
                "example.com",
                "example.com",
                "0",
                "1",
                "1",
                "https://c/",
                "https",
                "c",
                "0",
                "",
            ],
        );
        assert!(r.contains("https://a/"));
        assert!(r.contains("https://b/"));
        assert!(!r.contains("https://c/"));
        assert!(result("entrydns", &["0", "0"]).contains("\"policy\":null"));
        assert!(result("entrydns", &["1", "0"]).contains("\"policy\":{}"));
    }
    #[test]
    fn http_locks_and_preserves_nonselected_graph_boundary() {
        let mut f = Flow::new(Request {
            op: "http".into(),
            ..Default::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, "lock");
        f.advance(Reply::default()).unwrap();
        f.advance(Reply::default()).unwrap();
        assert_eq!(f.next().kind, "source");
        f.advance(facts(&["0", "0", "0", "0"])).unwrap();
        assert_eq!(f.next().kind, "enabled");
        f.advance(Reply {
            flag: true,
            ..Default::default()
        })
        .unwrap();
        f.advance(Reply {
            flag: false,
            ..Default::default()
        })
        .unwrap();
        assert!(f.next().result.contains("\"enabled\":true"));
        assert!(!f.next().result.contains("sections"));
    }
    #[test]
    fn malformed_source_rejected_without_consuming() {
        let mut f = Flow::new(Request {
            op: "rules".into(),
            ..Default::default()
        })
        .unwrap();
        let e = f.next().clone();
        assert_eq!(f.advance(Reply::default()), Err(Error::Invalid));
        assert_eq!(f.next(), &e);
        f.advance(facts(&["0", "0", "0", "0"])).unwrap();
        assert_eq!(f.next().kind, "result")
    }
    #[test]
    fn http_match_narrow_reads_preserve_priority_and_repeated_subscription() {
        for branch in 0..7 {
            let mut f = Flow::new(Request {
                op: "http".into(),
                ..Default::default()
            })
            .unwrap();
            let mut trace = vec![];
            for _ in 0..30 {
                let k = f.next().kind.clone();
                if k == "result" {
                    break;
                }
                trace.push(k.clone());
                let r = match k.as_str() {
                    "subscription" => Reply {
                        flag: true,
                        number: i64::from(branch == 3),
                        ..Default::default()
                    },
                    "match-read" => Reply {
                        error: (branch == 0).then(|| "x".into()),
                        ..Default::default()
                    },
                    "match-nodes" => Reply {
                        number: i64::from(branch != 1),
                        ..Default::default()
                    },
                    "match-source" => Reply {
                        text: if branch == 3 { "__merged__" } else { "digest" }.into(),
                        error: (branch == 2).then(|| "x".into()),
                        ..Default::default()
                    },
                    "hash" => Reply {
                        text: if branch == 4 { "wrong" } else { "digest" }.into(),
                        ..Default::default()
                    },
                    "read-policy" => Reply {
                        error: (branch == 6).then(|| "x".into()),
                        ..Default::default()
                    },
                    "source" => facts(&["0", "0", "0", "0"]),
                    _ => Reply::default(),
                };
                f.advance(r).unwrap()
            }
            assert!(!trace.iter().any(|s| s == "matches"));
            assert_eq!(
                trace
                    .iter()
                    .filter(|s| s.as_str() == "subscription")
                    .count(),
                if branch < 3 {
                    1
                } else if branch == 3 {
                    2
                } else {
                    3
                }
            );
            assert_eq!(
                trace.iter().any(|s| s == "read-policy"),
                branch == 3 || branch >= 5
            );
            if branch == 6 {
                assert!(f.next().result.contains("\"status\":400"));
            }
        }
    }
    #[test]
    fn domain_cap_is_before_sort_and_subrule_cap_after_filter() {
        let mut v = vec!["257".into()];
        for n in 0..257 {
            v.extend(["server:x".into(), format!("h{n}.com"), "0".into()])
        }
        let r = analyze(
            "domains",
            &Reply {
                names: v,
                ..Default::default()
            },
        )
        .unwrap()
        .json();
        assert!(!r.contains("+.h256.com"));
        let mut v = vec!["3001".into()];
        for _ in 0..3001 {
            v.extend([
                "raw".into(),
                "3".into(),
                "DOMAIN".into(),
                "x".into(),
                "DIRECT".into(),
                "".into(),
                "DIRECT".into(),
            ])
        }
        v.extend(["0".into(), "0".into()]);
        let r = analyze(
            "subrules",
            &Reply {
                names: v,
                ..Default::default()
            },
        )
        .unwrap()
        .json();
        assert_eq!(r.matches("DOMAIN,x,SmartVPN").count(), 3000);
    }
}
