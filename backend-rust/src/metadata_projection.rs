//! Metadata decisions; original proxy graphs and codec resources remain in Go.
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
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
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
pub struct Flow {
    op: String,
    pc: u8,
    effect: Effect,
    count: i64,
    profiles: i64,
    index: i64,
    manual_phase: bool,
    saved: String,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        let (k, pc) = match q.op.as_str() {
            "details" | "domains" => ("manual-count", 0),
            "index" => ("decode-profile", 20),
            "current-index" => ("subscription", 0),
            "mode" => ("mode-read", 0),
            _ => return Err(Error::Invalid),
        };
        Ok(Self {
            op: q.op,
            pc,
            effect: Effect {
                kind: k.into(),
                ..Default::default()
            },
            count: 0,
            profiles: 0,
            index: 0,
            manual_phase: false,
            saved: String::new(),
        })
    }
    pub fn next(&self) -> &Effect {
        &self.effect
    }
    fn emit(&mut self, k: &str, t: &str, o: &str, n: i64) {
        self.effect = Effect {
            kind: k.into(),
            text: t.into(),
            other: o.into(),
            number: n,
            result: String::new(),
        }
    }
    fn finish(&mut self, text: &str, error: Option<String>) {
        self.pc = 255;
        self.effect = Effect {
            kind: "result".into(),
            result: format!(
                "{{\"status\":0,\"text\":{},\"data\":null,\"error\":{},\"hasError\":{}}}",
                quote(text),
                quote(error.as_deref().unwrap_or("")),
                error.is_some()
            ),
            ..Default::default()
        }
    }
    fn detail_next(&mut self) {
        if self.index == self.count {
            self.finish("", None)
        } else {
            self.pc = 2;
            self.emit("detail-facts", "", "", self.index)
        }
    }
    fn domain_next(&mut self) {
        if self.index == self.count {
            self.finish("", None)
        } else {
            self.pc = 2;
            self.emit("suffix-child", "", "", self.index)
        }
    }
    fn index_next(&mut self) {
        if !self.manual_phase && self.index == self.profiles {
            self.manual_phase = true;
            self.index = 0
        }
        if self.manual_phase && self.index == self.count {
            self.finish("", None)
        } else {
            self.pc = 23;
            self.emit(
                if self.manual_phase {
                    "manual-name"
                } else {
                    "profile-name"
                },
                "",
                "",
                self.index,
            )
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.pc == 255 {
            return Err(Error::Done);
        }
        match self.op.as_str() {
            "details" => match self.pc {
                0 => {
                    if r.number < 0 {
                        return Err(Error::Invalid);
                    }
                    self.count = r.number;
                    self.pc = 1;
                    self.emit("details-new", "", "", r.number)
                }
                1 => self.detail_next(),
                2 => {
                    if r.names.len() != 4 {
                        return Err(Error::Invalid);
                    }
                    let mut bits = if r.flag { 1 } else { 0 };
                    if r.number & 1 != 0 {
                        if r.number & 2 != 0 {
                            bits |= 2
                        }
                        if !r.names[3].is_empty() {
                            bits |= 4
                        }
                    }
                    self.pc = 3;
                    self.emit("details-put", &r.names[0], &bits.to_string(), self.index)
                }
                3 => {
                    self.index += 1;
                    self.detail_next()
                }
                _ => return Err(Error::Invalid),
            },
            "domains" => match self.pc {
                0 => {
                    if r.number < 0 {
                        return Err(Error::Invalid);
                    }
                    self.count = r.number;
                    self.pc = 1;
                    self.emit("domains-new", "", "", r.number)
                }
                1 => self.domain_next(),
                2 => {
                    if r.flag {
                        self.pc = 3;
                        self.emit("domain-append", &r.text, "", self.index)
                    } else {
                        self.index += 1;
                        self.domain_next()
                    }
                }
                3 => {
                    self.index += 1;
                    self.domain_next()
                }
                _ => return Err(Error::Invalid),
            },
            "index" | "current-index" => match self.pc {
                0 => {
                    if r.text.is_empty() {
                        self.pc = 2;
                        self.emit("manual-snapshot", "", "", 0)
                    } else {
                        self.pc = 1;
                        self.emit("read-profile", "", "", 0)
                    }
                }
                1 => {
                    self.pc = 2;
                    self.emit("manual-snapshot", "", "", 0)
                }
                2 => {
                    self.pc = 20;
                    self.emit("decode-profile", "", "", 0)
                }
                20 => {
                    if r.error.is_some() {
                        self.finish(
                            "",
                            Some(crate::metadata_projection_text::INDEX_ERROR.into()),
                        )
                    } else {
                        if r.number < 0 {
                            return Err(Error::Invalid);
                        }
                        self.profiles = r.number;
                        self.pc = 21;
                        self.emit("manual-count", "", "", 0)
                    }
                }
                21 => {
                    if r.number < 0 {
                        return Err(Error::Invalid);
                    }
                    self.count = r.number;
                    self.pc = 22;
                    self.emit("index-new", "", "", 0)
                }
                22 => self.index_next(),
                23 => {
                    self.pc = 24;
                    self.emit("index-append", &r.text, "", self.index)
                }
                24 => {
                    self.index += 1;
                    self.index_next()
                }
                _ => return Err(Error::Invalid),
            },
            "mode" => match self.pc {
                0 => {
                    self.saved = r.text;
                    self.pc = 1;
                    self.emit("platform", "", "", 0)
                }
                1 => {
                    let s = if r.text == "616e64726f6964" {
                        "vpn"
                    } else if self.saved == "74756e" {
                        "tun"
                    } else {
                        "system-proxy"
                    }
                    .to_owned();
                    self.finish(&s, None)
                }
                _ => return Err(Error::Invalid),
            },
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
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
    fn send(f: &mut Flow, k: &str, r: Reply) {
        assert_eq!(f.next().kind, k);
        f.advance(r).unwrap()
    }
    fn ok(f: &mut Flow, k: &str) {
        send(f, k, Reply::default())
    }
    fn count(f: &mut Flow, k: &str, n: i64) {
        send(
            f,
            k,
            Reply {
                number: n,
                ..Default::default()
            },
        )
    }
    #[test]
    fn details_last_name_writes_in_source_order() {
        let mut f = new("details");
        count(&mut f, "manual-count", 2);
        ok(&mut f, "details-new");
        for i in 0..2 {
            send(
                &mut f,
                "detail-facts",
                Reply {
                    names: vec!["same".into(), "type".into(), "ws".into(), "/".into()],
                    flag: i == 0,
                    number: 3,
                    ..Default::default()
                },
            );
            assert_eq!(f.next().kind, "details-put");
            assert_eq!(f.next().number, i);
            assert_eq!(f.next().text, "same");
            assert_eq!(f.next().other, if i == 0 { "7" } else { "6" });
            ok(&mut f, "details-put")
        }
        assert_eq!(f.next().kind, "result")
    }
    #[test]
    fn invalid_ws_type_ignores_header_and_path() {
        let mut f = new("details");
        count(&mut f, "manual-count", 1);
        ok(&mut f, "details-new");
        send(
            &mut f,
            "detail-facts",
            Reply {
                names: vec!["".into(), "".into(), "".into(), "x".into()],
                number: 2,
                ..Default::default()
            },
        );
        assert_eq!(f.next().other, "0");
        assert_eq!(f.next().text, "")
    }
    #[test]
    fn empty_details_domains_still_allocate() {
        for (op, k) in [("details", "details-new"), ("domains", "domains-new")] {
            let mut f = new(op);
            count(&mut f, "manual-count", 0);
            assert_eq!(f.next().kind, k);
            ok(&mut f, k);
            assert_eq!(f.next().kind, "result")
        }
    }
    #[test]
    fn domains_preserve_duplicates_and_only_append_success() {
        let mut f = new("domains");
        count(&mut f, "manual-count", 3);
        ok(&mut f, "domains-new");
        for i in 0..3 {
            assert_eq!(f.next().number, i);
            send(
                &mut f,
                "suffix-child",
                Reply {
                    text: "+.same".into(),
                    flag: i != 1,
                    ..Default::default()
                },
            );
            if i != 1 {
                assert_eq!(f.next().text, "+.same");
                ok(&mut f, "domain-append")
            }
        }
        assert_eq!(f.next().kind, "result")
    }
    #[test]
    fn index_is_group_append_not_last_wins() {
        let mut f = new("index");
        count(&mut f, "decode-profile", 2);
        count(&mut f, "manual-count", 2);
        ok(&mut f, "index-new");
        for kind in ["profile-name", "profile-name", "manual-name", "manual-name"] {
            send(
                &mut f,
                kind,
                Reply {
                    text: "same".into(),
                    ..Default::default()
                },
            );
            assert_eq!(f.next().text, "same");
            ok(&mut f, "index-append")
        }
        assert_eq!(f.next().kind, "result")
    }
    #[test]
    fn index_decode_error_precedes_manual_count() {
        let mut f = new("index");
        send(
            &mut f,
            "decode-profile",
            Reply {
                error: Some("yaml original".into()),
                ..Default::default()
            },
        );
        assert_eq!(f.next().kind, "result");
        assert!(f
            .next()
            .result
            .contains(&quote(crate::metadata_projection_text::INDEX_ERROR)))
    }
    #[test]
    fn current_read_error_is_ignored_before_snapshot_and_decode() {
        let mut f = new("current-index");
        send(
            &mut f,
            "subscription",
            Reply {
                text: " ".into(),
                ..Default::default()
            },
        );
        send(
            &mut f,
            "read-profile",
            Reply {
                error: Some("partial read".into()),
                ..Default::default()
            },
        );
        ok(&mut f, "manual-snapshot");
        count(&mut f, "decode-profile", 0);
        count(&mut f, "manual-count", 0);
        ok(&mut f, "index-new");
        assert_eq!(f.next().kind, "result")
    }
    #[test]
    fn empty_subscription_skips_read_but_still_decodes() {
        let mut f = new("current-index");
        ok(&mut f, "subscription");
        ok(&mut f, "manual-snapshot");
        assert_eq!(f.next().kind, "decode-profile")
    }
    #[test]
    fn mode_platform_support_does_not_trim() {
        for (platform, mode, want) in [
            ("android", "tun", "vpn"),
            ("android", "vpn", "vpn"),
            ("windows", "tun", "tun"),
            ("windows", "system-proxy", "system-proxy"),
            ("windows", " tun ", "system-proxy"),
            ("windows", "vpn", "system-proxy"),
        ] {
            let mut f = new("mode");
            send(
                &mut f,
                "mode-read",
                Reply {
                    text: mode.as_bytes().iter().map(|b| format!("{b:02x}")).collect(),
                    ..Default::default()
                },
            );
            send(
                &mut f,
                "platform",
                Reply {
                    text: platform
                        .as_bytes()
                        .iter()
                        .map(|b| format!("{b:02x}"))
                        .collect(),
                    ..Default::default()
                },
            );
            assert!(f
                .next()
                .result
                .contains(&format!("\"text\":{}", quote(want))))
        }
    }
    #[test]
    fn malformed_facts_and_counts_do_not_advance() {
        let mut f = new("details");
        let before = f.next().clone();
        assert_eq!(
            f.advance(Reply {
                number: -1,
                ..Default::default()
            }),
            Err(Error::Invalid)
        );
        assert_eq!(f.next(), &before);
        count(&mut f, "manual-count", 1);
        ok(&mut f, "details-new");
        let before = f.next().clone();
        assert_eq!(f.advance(Reply::default()), Err(Error::Invalid));
        assert_eq!(f.next(), &before)
    }
    #[test]
    fn all_manual_nil_profile_and_terminal_are_valid() {
        let mut f = new("index");
        count(&mut f, "decode-profile", 0);
        count(&mut f, "manual-count", 1);
        ok(&mut f, "index-new");
        ok(&mut f, "manual-name");
        assert_eq!(f.next().text, "");
        ok(&mut f, "index-append");
        let old = f.next().clone();
        assert_eq!(f.advance(Reply::default()), Err(Error::Done));
        assert_eq!(f.next(), &old)
    }
}
