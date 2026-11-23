//! Subscription conversion planner. Secrets and decoded graph aliases remain in Go.
use crate::converter_names::{rewrite_rule, trim, unique_name};
use std::collections::{HashMap, HashSet};
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Source {
    pub body: i64,
    pub label: Vec<u8>,
    pub prefix: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub body: i64,
    pub sources: Vec<Source>,
    pub name: Vec<u8>,
    pub sources_present: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Field {
    pub key: Vec<u8>,
    pub reference: i64,
    pub trim_ref: i64,
    pub text: Vec<u8>,
    pub trim: Vec<u8>,
    pub empty: bool,
    pub boolean: bool,
    pub integer: i64,
    pub count: i64,
    pub trim_empty: bool,
    pub lower: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Assignment {
    pub key: Vec<u8>,
    pub reference: i64,
    pub text: Vec<u8>,
    pub value: i64,
    pub kind: u8,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Node {
    pub name: Vec<u8>,
    pub kind: Vec<u8>,
    pub network: Vec<u8>,
    pub source_label: Vec<u8>,
    pub tls: bool,
    pub ws_host: bool,
    pub ws_path: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub ok: bool,
    pub token: i64,
    pub count: i64,
    pub text: Vec<u8>,
    pub flag: bool,
    pub tokens: Vec<i64>,
    pub fields: Vec<Field>,
    pub node: Node,
    pub names: Vec<Vec<u8>>,
    pub error_token: i64,
    pub tokens_present: bool,
    pub fields_present: bool,
    pub names_present: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: Vec<u8>,
    pub token: i64,
    pub reference: i64,
    pub index: i64,
    pub key: Vec<u8>,
    pub text: Vec<u8>,
    pub value: i64,
    pub flag: bool,
    pub tokens: Vec<i64>,
    pub fields: Vec<Assignment>,
    pub node: Node,
    pub error: u8,
    pub error_token: i64,
    pub tokens_present: bool,
    pub fields_present: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Done,
}

fn field(fs: &[Field], key: &str) -> Field {
    fs.iter()
        .find(|f| f.key == key.as_bytes())
        .cloned()
        .unwrap_or(Field {
            key: key.as_bytes().to_vec(),
            empty: true,
            trim_empty: true,
            ..Field::default()
        })
}
fn info(n: &[u8]) -> bool {
    crate::subscription::is_informational_node(&String::from_utf8_lossy(n))
}
fn cs(k: &str, v: &[u8]) -> Assignment {
    Assignment {
        key: k.as_bytes().to_vec(),
        kind: 1,
        text: v.to_vec(),
        ..Assignment::default()
    }
}
fn cr(k: &str, v: i64) -> Assignment {
    Assignment {
        key: k.as_bytes().to_vec(),
        reference: v,
        ..Assignment::default()
    }
}
fn ci(k: &str, v: i64) -> Assignment {
    Assignment {
        key: k.as_bytes().to_vec(),
        kind: 2,
        value: v,
        ..Assignment::default()
    }
}
fn cb(k: &str) -> Assignment {
    Assignment {
        key: k.as_bytes().to_vec(),
        kind: 3,
        value: 1,
        ..Assignment::default()
    }
}
fn singbox_plan(
    base: &[Field],
    fs: &[Field],
    kind: &[u8],
) -> Option<(Vec<Assignment>, Node, Option<Vec<i64>>)> {
    let b = |k| field(base, k);
    let f = |k| field(fs, k);
    let typ = if kind == b"shadowsocks" {
        b"ss".as_slice()
    } else {
        kind
    };
    let valid = match typ {
        b"ss" => !f("method").empty && !f("password").empty,
        b"vmess" | b"vless" => !f("uuid").empty,
        b"trojan" | b"hysteria2" => !f("password").empty,
        b"tuic" => !f("uuid").empty || !f("password").empty,
        _ => false,
    };
    if !valid {
        return None;
    }
    let name = b("tag").trim;
    let mut a = vec![
        cs("name", &name),
        cs("type", typ),
        cr("server", b("server").trim_ref),
        ci("port", b("server_port").integer),
    ];
    match typ {
        b"ss" => a.extend([
            cr("cipher", f("method").reference),
            cr("password", f("password").reference),
            cb("udp"),
        ]),
        b"vmess" => {
            a.push(cr("uuid", f("uuid").reference));
            a.push(if f("security").trim_empty {
                cs("cipher", b"auto")
            } else {
                cr("cipher", f("security").trim_ref)
            });
            a.extend([ci("alterId", f("alter_id").integer), cb("udp")]);
        }
        b"vless" => {
            a.extend([cr("uuid", f("uuid").reference), cb("udp")]);
            if !f("flow").empty {
                a.push(cr("flow", f("flow").reference))
            }
        }
        b"trojan" => a.extend([cr("password", f("password").reference), cb("udp")]),
        b"hysteria2" => a.push(cr("password", f("password").reference)),
        b"tuic" => {
            a.extend([
                cr("uuid", f("uuid").reference),
                cr("password", f("password").reference),
                cb("udp"),
            ]);
            if !f("congestion_controller").empty {
                a.push(cr(
                    "congestion-controller",
                    f("congestion_controller").reference,
                ))
            }
        }
        _ => {}
    }
    let mut n = Node {
        name,
        kind: typ.to_vec(),
        network: b"tcp".to_vec(),
        ..Node::default()
    };
    let (tls_fields, sni) = match typ {
        b"vmess" | b"vless" => {
            n.tls = f("tls").count > 0 && f("tls.enabled").boolean;
            if n.tls {
                a.push(cb("tls"))
            }
            (n.tls, "servername")
        }
        b"trojan" | b"hysteria2" | b"tuic" => {
            n.tls = true;
            (f("tls").count > 0, "sni")
        }
        _ => (false, "servername"),
    };
    if tls_fields {
        if !f("tls.server_name").empty {
            a.push(cr(sni, f("tls.server_name").reference))
        }
        if f("tls.insecure").boolean {
            a.push(cb("skip-cert-verify"))
        }
        if f("tls.alpn").count > 0 {
            a.push(cr("alpn", f("tls.alpn").reference))
        }
        if typ == b"vless" && f("tls.reality").count > 0 && f("tls.reality.enabled").boolean {
            a.push(cr(
                "reality-opts.public-key",
                f("tls.reality.public_key").reference,
            ));
            if !f("tls.reality.short_id").empty {
                a.push(cr(
                    "reality-opts.short-id",
                    f("tls.reality.short_id").reference,
                ))
            }
        }
    }
    if typ == b"hysteria2" && f("obfs").count > 0 && !f("obfs.type").empty {
        a.push(cr("obfs", f("obfs.type").reference));
        if !f("obfs.password").empty {
            a.push(cr("obfs-password", f("obfs.password").reference))
        }
    }
    let transport = if matches!(typ, b"vmess" | b"vless" | b"trojan") && f("transport").count > 0 {
        n.network = f("transport.type").text;
        n.ws_host = !f("transport.host").empty;
        n.ws_path = !f("transport.path").empty;
        Some(vec![
            f("transport.path").reference,
            f("transport.host").reference,
            f("transport.path").reference,
        ])
    } else {
        None
    };
    if typ == b"hysteria2" || typ == b"tuic" {
        n.network = b"udp".to_vec()
    }
    Some((a, n, transport))
}

pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    child: Option<Box<Flow>>,
    after: u16,
    items: Vec<i64>,
    pos: usize,
    ps: Vec<i64>,
    ns: Vec<i64>,
    base: Vec<Field>,
    fields: Vec<Field>,
    kind: Vec<u8>,
    node: Node,
    transport: Option<Vec<i64>>,
    proxy: i64,
    ptoken: i64,
    ntoken: i64,
    body: i64,
    non_nil: bool,
    decoded: bool,
    body_uri: bool,
    line: i64,
    suffix: u64,
    m: Merge,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 4 || q.sources.len() > 100_000 {
            return Err(Error::Invalid);
        }
        let mut s = Self {
            body: q.body,
            q,
            e: Effect::default(),
            pc: 0,
            child: None,
            after: 0,
            items: vec![],
            pos: 0,
            ps: vec![],
            ns: vec![],
            base: vec![],
            fields: vec![],
            kind: vec![],
            node: Node::default(),
            transport: None,
            proxy: 0,
            ptoken: 0,
            ntoken: 0,
            non_nil: false,
            decoded: false,
            body_uri: false,
            line: 0,
            suffix: 2,
            m: Merge::default(),
        };
        match s.q.op {
            0 => s.emit("decode-singbox", s.body, 10),
            1 => s.emit("body-facts", s.body, 30),
            2 => s.emit("base64", s.body, 70),
            3 => s.merge_start(),
            4 => {
                s.emit("used-name", 0, 90);
                s.e.text = s.q.name.clone()
            }
            _ => unreachable!(),
        };
        Ok(s)
    }
    pub fn next(&self) -> &Effect {
        &self.e
    }
    fn emit(&mut self, k: &str, t: i64, pc: u16) {
        self.pc = pc;
        self.e = Effect {
            kind: k.as_bytes().to_vec(),
            token: t,
            ..Effect::default()
        };
    }
    fn result(&mut self, body: i64, ps: i64, ns: i64) {
        self.emit("result", body, 254);
        self.e.reference = ps;
        self.e.index = ns;
    }
    fn child(&mut self, op: u8, body: i64, after: u16) -> Result<(), Error> {
        let c = Self::new(Request {
            op,
            body,
            ..Request::default()
        })?;
        self.child = Some(Box::new(c));
        self.after = after;
        self.poll_child()
    }
    fn poll_child(&mut self) -> Result<(), Error> {
        let e = self.child.as_ref().ok_or(Error::Invalid)?.next().clone();
        if e.kind == b"result" {
            self.child = None;
            self.pc = self.after;
            self.child_done(e)
        } else {
            self.e = e;
            Ok(())
        }
    }
    fn next_outbound(&mut self) {
        if self.pos == self.items.len() {
            self.finish_lists(true)
        } else {
            self.emit("decode-base", self.items[self.pos], 11)
        }
    }
    fn skip_outbound(&mut self) {
        self.pos += 1;
        self.next_outbound()
    }
    fn emit_node(&mut self, pc: u16) {
        self.emit("node-new", 0, pc);
        self.e.node = self.node.clone()
    }
    fn finish_lists(&mut self, non_nil: bool) {
        self.non_nil = non_nil;
        self.emit("proxies-build", 0, 16);
        self.e.tokens = self.ps.clone();
        self.e.tokens_present = !self.ps.is_empty();
        self.e.flag = non_nil;
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.e.kind == b"done" {
            return Err(Error::Done);
        }
        if r.tokens.len() > 100_000 || r.fields.len() > 100_000 || r.names.len() > 100_000 {
            return Err(Error::Limit);
        }
        if self.child.is_some() {
            self.child.as_mut().unwrap().advance(r)?;
            return self.poll_child();
        }
        match self.pc {
            10 => {
                if !r.ok || r.tokens.is_empty() {
                    self.result(0, 0, 0)
                } else {
                    self.items = r.tokens;
                    self.next_outbound()
                }
            }
            11 => {
                if !r.ok {
                    self.skip_outbound()
                } else {
                    self.base = r.fields;
                    let name = field(&self.base, "tag").trim;
                    self.kind = field(&self.base, "type").lower;
                    if name.is_empty()
                        || info(&name)
                        || !matches!(
                            self.kind.as_slice(),
                            b"ss"
                                | b"shadowsocks"
                                | b"vmess"
                                | b"vless"
                                | b"trojan"
                                | b"hysteria2"
                                | b"tuic"
                        )
                    {
                        self.skip_outbound()
                    } else {
                        self.emit("decode-outbound", self.items[self.pos], 12);
                        self.e.key = self.kind.clone()
                    }
                }
            }
            12 => {
                if !r.ok {
                    self.skip_outbound()
                } else if let Some((a, n, t)) = singbox_plan(&self.base, &r.fields, &self.kind) {
                    self.node = n;
                    self.transport = t;
                    self.emit("map-new", 0, 13);
                    self.e.fields = a;
                    self.e.fields_present = true;
                } else {
                    self.skip_outbound()
                }
            }
            13 => {
                self.proxy = r.token;
                if let Some(t) = self.transport.take() {
                    self.emit("transport-child", self.proxy, 14);
                    self.e.text = self.node.network.clone();
                    self.e.tokens = t;
                    self.e.tokens_present = true;
                } else {
                    self.emit_node(15)
                }
            }
            14 => self.emit_node(15),
            15 => {
                self.ps.push(self.proxy);
                self.ns.push(r.token);
                self.skip_outbound()
            }
            16 => {
                self.ptoken = r.token;
                self.emit("nodes-build", 0, 17);
                self.e.tokens = self.ns.clone();
                self.e.tokens_present = !self.ns.is_empty();
                self.e.flag = self.non_nil;
            }
            17 => self.emit("nodes-unique-child", r.token, 18),
            18 => self.result(0, self.ptoken, r.token),
            90 => {
                if !r.flag {
                    let text = self.e.text.clone();
                    self.emit("use-name", 0, 91);
                    self.e.text = text;
                } else {
                    let mut n = self.q.name.clone();
                    n.extend(format!(" ({})", self.suffix).as_bytes());
                    self.suffix += 1;
                    self.emit("used-name", 0, 90);
                    self.e.text = n;
                }
            }
            91 => {
                let text = self.e.text.clone();
                self.result(0, 0, 0);
                self.e.text = text
            }
            254 => self.emit("done", 0, 255),
            _ => self.advance_rest(r)?,
        }
        Ok(())
    }
}

impl Flow {
    fn code_error(&mut self, code: u8) {
        self.result(0, 0, 0);
        self.e.error = code;
    }
    fn clash(&mut self) {
        self.ps.clear();
        self.emit("decode-clash", self.body, 35)
    }
    fn clash_failed(&mut self) {
        if !self.decoded && !self.body_uri {
            self.emit("base64", self.body, 39)
        } else {
            self.lines()
        }
    }
    fn next_clash(&mut self) {
        if self.pos == self.items.len() {
            if self.ps.is_empty() {
                self.clash_failed()
            } else {
                self.emit("proxies-build", 0, 37);
                self.e.tokens = self.ps.clone();
                self.e.tokens_present = true;
                self.e.flag = true;
            }
        } else {
            self.emit("proxy-facts", self.items[self.pos], 36)
        }
    }
    fn lines(&mut self) {
        self.ps.clear();
        self.ns.clear();
        self.emit("lines", self.body, 41)
    }
    fn next_line(&mut self) {
        if self.pos == self.items.len() {
            if self.ps.is_empty() {
                self.code_error(2)
            } else {
                self.finish_lists(false)
            }
        } else {
            self.emit("line-facts", self.items[self.pos], 42)
        }
    }
    fn skip_line(&mut self) {
        self.pos += 1;
        self.next_line()
    }
    fn vmess_assignments(&self) -> Vec<Assignment> {
        let f = |k| field(&self.fields, k);
        let mut a = vec![
            cs("name", &self.node.name),
            cs("type", b"vmess"),
            cr("server", f("add").trim_ref),
            ci("port", f("port-number").integer),
        ];
        a.push(if f("id").trim_empty {
            cs("uuid", b"00000000-0000-0000-0000-000000000000")
        } else {
            cr("uuid", f("id").trim_ref)
        });
        let mut cipher = f("scy").trim;
        if cipher.is_empty() || f("scy").lower == b"auto" {
            cipher = b"auto".to_vec()
        }
        a.extend([
            ci("alterId", f("aid-number").integer),
            cs("cipher", &cipher),
            cb("udp"),
        ]);
        if self.node.tls {
            a.push(cb("tls"));
            let mut sni = f("sni");
            if sni.trim_empty {
                sni = f("host")
            }
            if !sni.trim_empty {
                a.push(cr("servername", sni.trim_ref))
            }
        }
        a
    }
    fn child_done(&mut self, e: Effect) -> Result<(), Error> {
        match self.pc {
            31 | 34 => {
                if e.reference != 0 {
                    self.ptoken = e.reference;
                    self.ntoken = e.index;
                    self.emit("proxies-items", e.reference, 32)
                } else {
                    self.clash()
                }
            }
            75 => {
                if e.error != 0 {
                    self.e = e;
                    self.pc = 254;
                } else {
                    self.ptoken = e.reference;
                    self.ntoken = e.index;
                    self.emit("marshal-proxies", e.reference, 76)
                }
            }
            _ => self.merge_child_done(e)?,
        }
        Ok(())
    }
    fn advance_rest(&mut self, r: Reply) -> Result<(), Error> {
        match self.pc {
            30 => {
                if r.flag {
                    self.code_error(2)
                } else {
                    self.body_uri = field(&r.fields, "uri").boolean;
                    if r.text == b"json" {
                        self.child(0, self.body, 31)?
                    } else {
                        self.clash()
                    }
                }
            }
            32 => {
                if !r.tokens.is_empty() {
                    self.result(0, self.ptoken, self.ntoken)
                } else {
                    self.clash()
                }
            }
            35 => {
                if !r.ok || r.count == 0 {
                    self.clash_failed()
                } else {
                    self.items = r.tokens;
                    self.pos = 0;
                    self.next_clash()
                }
            }
            36 => {
                if !r.text.is_empty() && !info(&r.text) {
                    self.ps.push(self.items[self.pos])
                }
                self.pos += 1;
                self.next_clash()
            }
            37 => {
                self.ptoken = r.token;
                self.emit("yaml-nodes-child", self.body, 38)
            }
            38 => self.result(0, self.ptoken, r.token),
            39 => {
                if !r.ok {
                    self.lines()
                } else {
                    self.body = r.token;
                    self.decoded = true;
                    self.emit("body-facts", self.body, 40)
                }
            }
            40 => {
                if r.text == b"json" {
                    self.child(0, self.body, 34)?
                } else {
                    self.clash()
                }
            }
            41 => {
                self.items = r.tokens;
                self.pos = 0;
                self.next_line()
            }
            42 => {
                if field(&r.fields, "empty").boolean
                    || field(&r.fields, "comment").boolean
                    || !r.flag
                {
                    self.skip_line()
                } else {
                    self.line = r.token;
                    self.kind = r.text;
                    if self.kind == b"vmess" {
                        self.emit("vmess-base64", self.line, 43)
                    } else if matches!(
                        self.kind.as_slice(),
                        b"vless" | b"trojan" | b"ss" | b"hysteria2" | b"hy2" | b"anytls"
                    ) {
                        self.emit("decode-uri", self.line, 48)
                    } else {
                        self.emit("uri-child", self.line, 51)
                    }
                }
            }
            43 => {
                if !r.ok {
                    self.skip_line()
                } else {
                    self.emit("decode-vmess", r.token, 44)
                }
            }
            44 => {
                if !r.ok {
                    self.skip_line()
                } else {
                    self.fields = r.fields;
                    let f = |k| field(&self.fields, k);
                    let mut name = f("ps").trim;
                    if name.is_empty() {
                        name = f("fallback").text
                    }
                    if info(&name) {
                        self.skip_line()
                    } else {
                        let tls = f("tls").lower;
                        let mut net = f("net").text;
                        if net.is_empty() {
                            net = b"tcp".to_vec()
                        }
                        self.node = Node {
                            name,
                            kind: b"Vmess".to_vec(),
                            network: net,
                            tls: !tls.is_empty() && tls != b"none" && tls != b"false",
                            ws_host: !f("host").empty,
                            ws_path: !f("path").empty,
                            ..Node::default()
                        };
                        self.emit_node(45)
                    }
                }
            }
            45 => {
                self.ns.push(r.token);
                let a = self.vmess_assignments();
                self.emit("map-new", 0, 46);
                self.e.fields = a;
                self.e.fields_present = true;
            }
            46 => {
                self.proxy = r.token;
                self.emit("transport-child", r.token, 47);
                self.e.text = field(&self.fields, "net").text;
                let path = field(&self.fields, "path").reference;
                self.e.tokens = vec![path, field(&self.fields, "host").reference, path];
                self.e.tokens_present = true;
            }
            47 => {
                self.ps.push(self.proxy);
                self.skip_line()
            }
            48 => {
                if !r.ok {
                    self.skip_line()
                } else {
                    let mut name = r.text;
                    if name.is_empty() {
                        name = r.names.first().cloned().unwrap_or_default()
                    }
                    if name.is_empty() || info(&name) {
                        self.skip_line()
                    } else {
                        let f = |k| field(&r.fields, k).text;
                        let sec = f("security");
                        let t = f("tls");
                        let mut tls =
                            (!sec.is_empty() && sec != b"none" && sec != b"false" && sec != b"0")
                                || t == b"1";
                        if self.kind == b"trojan" && sec.is_empty() && t.is_empty() {
                            tls = true
                        }
                        if sec == b"none" {
                            tls = false
                        }
                        self.node = Node {
                            name,
                            kind: self.kind.clone(),
                            network: f("type"),
                            tls,
                            ws_host: !field(&r.fields, "host").empty,
                            ws_path: !field(&r.fields, "path").empty,
                            ..Node::default()
                        };
                        self.emit_node(49)
                    }
                }
            }
            49 => {
                self.ns.push(r.token);
                self.emit("uri-child", self.line, 50)
            }
            50 => {
                if r.ok {
                    self.ps.push(r.token)
                }
                self.skip_line()
            }
            51 => {
                if !r.ok || info(&r.node.name) {
                    self.skip_line()
                } else {
                    self.ps.push(r.token);
                    self.node = r.node;
                    self.emit("proxy-facts", r.token, 52)
                }
            }
            52 => {
                let name = std::mem::take(&mut self.node.name);
                let kind = std::mem::take(&mut self.node.kind);
                self.node = r.node;
                self.node.name = name;
                self.node.kind = kind;
                self.emit_node(53)
            }
            53 => {
                self.ns.push(r.token);
                self.skip_line()
            }
            70 => {
                if !r.ok {
                    self.emit("body-facts", self.body, 73)
                } else {
                    self.line = r.token;
                    self.emit("yaml-nodes-child", r.token, 71)
                }
            }
            71 => {
                if r.count > 0 {
                    self.emit("yaml-nodes-child", self.line, 72)
                } else {
                    self.emit("body-facts", self.body, 73)
                }
            }
            72 => self.result(self.line, 0, r.token),
            73 => {
                if r.count > 0 && r.text != b"json" {
                    self.emit("yaml-nodes-child", self.body, 74)
                } else {
                    self.child(1, self.body, 75)?
                }
            }
            74 => {
                if r.count > 0 {
                    self.result(self.body, 0, r.token)
                } else {
                    self.child(1, self.body, 75)?
                }
            }
            76 => {
                if !r.ok {
                    self.code_error(3)
                } else {
                    self.result(r.token, self.ptoken, self.ntoken)
                }
            }
            _ => self.advance_merge(r)?,
        }
        Ok(())
    }
}

#[derive(Default)]
struct Merge {
    token: i64,
    source: usize,
    proxies: i64,
    nodes: i64,
    ps: Vec<i64>,
    ns: Vec<i64>,
    index: usize,
    used: HashSet<Vec<u8>>,
    used_labels: HashSet<Vec<u8>>,
    dns_keys: HashSet<Vec<u8>>,
    names: HashMap<Vec<u8>, Vec<u8>>,
    label: Vec<u8>,
    prefix: Vec<u8>,
    name: Vec<u8>,
    dns_names: Vec<Vec<u8>>,
    dns_values: Vec<i64>,
    dns_index: usize,
    policy: i64,
    groups: Vec<i64>,
    group: usize,
    members: Vec<Vec<u8>>,
    member: usize,
    rules: Vec<Vec<u8>>,
    rule: usize,
    proxy_count: i64,
    group_count: i64,
    rule_count: i64,
}
impl Flow {
    fn merge_start(&mut self) {
        self.emit("merge-new", 0, 100)
    }
    fn source_body(&self) -> i64 {
        self.q.sources[self.m.source].body
    }
    fn merge_source(&mut self) -> Result<(), Error> {
        if self.m.source == self.q.sources.len() {
            if self.m.proxy_count == 0 {
                self.code_error(2)
            } else {
                self.emit("merge-marshal", self.m.token, 122);
                self.e.flag = !self.m.dns_keys.is_empty();
                self.e.index = self.m.group_count;
                self.e.value = self.m.rule_count;
            };
            Ok(())
        } else {
            self.child(1, self.source_body(), 101)
        }
    }
    fn merge_next_source(&mut self) -> Result<(), Error> {
        self.m.source += 1;
        self.merge_source()
    }
    fn merge_child_done(&mut self, e: Effect) -> Result<(), Error> {
        if self.pc != 101 {
            return Err(Error::Invalid);
        }
        if e.error != 0 || e.reference == 0 {
            self.merge_next_source()?
        } else {
            self.m.proxies = e.reference;
            self.m.nodes = e.index;
            self.emit("proxies-items", e.reference, 102)
        }
        Ok(())
    }
    fn merge_dns(&mut self) {
        while self.m.dns_index < self.m.dns_names.len() {
            let i = self.m.dns_index;
            let name = self.m.dns_names[i].clone();
            self.m.dns_index += 1;
            if self.m.dns_keys.insert(name.clone()) {
                self.emit("merge-dns", self.m.token, 104);
                self.e.reference = self.m.dns_values[i];
                self.e.text = name;
                return;
            }
        }
        let source = &self.q.sources[self.m.source];
        let mut label = trim(&source.label).to_vec();
        if label.is_empty() {
            label = default_label(self.m.source + 1)
        }
        self.m.label = unique_name(&label, &mut self.m.used_labels);
        self.m.prefix = trim(&source.prefix).to_vec();
        self.m.names.clear();
        self.emit("proxies-items", self.m.proxies, 105)
    }
    fn merge_proxy_next(&mut self) {
        if self.m.index == self.m.ps.len() {
            self.emit("policy-decode", self.source_body(), 111)
        } else {
            self.emit("proxy-facts", self.m.ps[self.m.index], 107)
        }
    }
    fn merge_proxy_append(&mut self) {
        self.emit("merge-proxy", self.m.token, 110);
        self.e.reference = self.m.ps[self.m.index];
        self.e.text = self.m.name.clone();
        self.e.key = self.m.label.clone()
    }
    fn group_name_next(&mut self) {
        if self.m.group == self.m.groups.len() {
            self.m.group = 0;
            self.group_next()
        } else {
            self.emit("group-facts", self.m.groups[self.m.group], 114)
        }
    }
    fn group_next(&mut self) {
        if self.m.group == self.m.groups.len() {
            self.emit("policy-rules", self.m.policy, 119)
        } else {
            self.emit("group-facts", self.m.groups[self.m.group], 116)
        }
    }
    fn member_next(&mut self) {
        while self.m.member < self.m.members.len() {
            let j = self.m.member;
            self.m.member += 1;
            let name = &self.m.members[j];
            if name == b"DIRECT" || name == b"REJECT" {
                continue;
            }
            if let Some(alias) = self.m.names.get(name).cloned() {
                self.emit("group-member", self.m.groups[self.m.group], 117);
                self.e.index = j as i64;
                self.e.text = alias;
                return;
            }
        }
        self.emit("merge-group", self.m.token, 118);
        self.e.reference = self.m.groups[self.m.group]
    }
    fn rule_next(&mut self) {
        if self.m.rule == self.m.rules.len() {
            self.merge_nodes()
        } else {
            let rule = rewrite_rule(&self.m.rules[self.m.rule], &self.m.names);
            self.emit("merge-rule", self.m.token, 120);
            self.e.text = rule
        }
    }
    fn merge_nodes(&mut self) {
        self.emit("merge-nodes", self.m.token, 121);
        self.e.reference = self.m.nodes;
    }
    fn advance_merge(&mut self, r: Reply) -> Result<(), Error> {
        match self.pc {
            100 => {
                self.m.token = r.token;
                self.merge_source()?
            }
            102 => {
                if r.tokens.is_empty() {
                    self.merge_next_source()?
                } else {
                    self.emit("dns-child", self.source_body(), 103)
                }
            }
            103 => {
                if r.names.len() != r.tokens.len() {
                    return Err(Error::Invalid);
                }
                self.m.dns_names = r.names;
                self.m.dns_values = r.tokens;
                self.m.dns_index = 0;
                self.merge_dns()
            }
            104 => self.merge_dns(),
            105 => {
                self.m.ps = r.tokens;
                self.emit("nodes-items", self.m.nodes, 106)
            }
            106 => {
                self.m.ns = r.tokens;
                self.m.index = 0;
                self.merge_proxy_next()
            }
            107 => {
                let old = r.text;
                let mut name = old.clone();
                if name.is_empty() {
                    name = r.names.first().cloned().ok_or(Error::Invalid)?
                }
                if !self.m.prefix.is_empty() && !name.starts_with(&self.m.prefix) {
                    let mut pref = self.m.prefix.clone();
                    pref.push(b' ');
                    pref.extend(name);
                    name = pref
                }
                name = unique_name(&name, &mut self.m.used);
                self.m.names.insert(old, name.clone());
                self.m.name = name.clone();
                self.emit("map-assign", self.m.ps[self.m.index], 108);
                self.e.fields = vec![cs("name", &name)];
                self.e.fields_present = true;
            }
            108 => {
                if self.m.index < self.m.ns.len() {
                    self.emit("nodes-rename", self.m.nodes, 109);
                    self.e.index = self.m.index as i64;
                    self.e.text = self.m.name.clone();
                    self.e.key = self.m.label.clone()
                } else {
                    self.merge_proxy_append()
                }
            }
            109 => self.merge_proxy_append(),
            110 => {
                self.m.proxy_count += 1;
                self.m.index += 1;
                self.merge_proxy_next()
            }
            111 => {
                if !r.ok {
                    self.merge_nodes()
                } else {
                    self.m.policy = r.token;
                    self.emit("section-append", self.m.token, 112);
                    self.e.reference = r.token;
                    self.e.text = self.m.label.clone()
                }
            }
            112 => self.emit("policy-clone-groups", self.m.policy, 113),
            113 => {
                self.m.groups = r.tokens;
                self.m.group = 0;
                if self.q.sources.len() > 1 {
                    self.group_name_next()
                } else {
                    self.group_next()
                }
            }
            114 => {
                let mut alias = self.m.label.clone();
                alias.extend_from_slice(b" / ");
                alias.extend_from_slice(&r.text);
                self.m.names.insert(r.text, alias.clone());
                self.emit("group-name", self.m.groups[self.m.group], 115);
                self.e.text = alias;
            }
            115 => {
                self.m.group += 1;
                self.group_name_next()
            }
            116 => {
                self.m.members = r.names;
                self.m.member = 0;
                self.member_next()
            }
            117 => self.member_next(),
            118 => {
                self.m.group_count += 1;
                self.m.group += 1;
                self.group_next()
            }
            119 => {
                self.m.rules = r.names;
                self.m.rule = 0;
                self.rule_next()
            }
            120 => {
                self.m.rule_count += 1;
                self.m.rule += 1;
                self.rule_next()
            }
            121 => self.merge_next_source()?,
            122 => {
                if !r.ok {
                    self.code_error(4)
                } else {
                    self.body = r.token;
                    self.emit("merge-node-list", self.m.token, 123)
                }
            }
            123 => self.emit("nodes-unique-child", r.token, 124),
            124 => self.result(self.body, 0, r.token),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}
fn default_label(index: usize) -> Vec<u8> {
    format!("订阅{}", index).into_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;
    fn f(k: &str, t: &str, r: i64) -> Field {
        Field {
            key: k.as_bytes().to_vec(),
            text: t.as_bytes().to_vec(),
            trim: trim(t.as_bytes()).to_vec(),
            lower: trim(t.as_bytes()).to_ascii_lowercase(),
            reference: r,
            trim_ref: r + 100,
            empty: t.is_empty(),
            trim_empty: trim(t.as_bytes()).is_empty(),
            count: t.len() as i64,
            ..Field::default()
        }
    }
    fn flag(k: &str, b: bool) -> Field {
        Field {
            key: k.as_bytes().to_vec(),
            boolean: b,
            ..Field::default()
        }
    }
    fn count(k: &str, n: i64) -> Field {
        Field {
            key: k.as_bytes().to_vec(),
            count: n,
            ..Field::default()
        }
    }
    fn base() -> Vec<Field> {
        vec![
            f("tag", " Name ", 1),
            f("server", " host ", 2),
            Field {
                key: b"server_port".to_vec(),
                integer: -1,
                ..Field::default()
            },
        ]
    }
    fn has(a: &[Assignment], k: &str) -> bool {
        a.iter().any(|v| v.key == k.as_bytes())
    }
    fn get<'a>(a: &'a [Assignment], k: &str) -> &'a Assignment {
        a.iter().find(|v| v.key == k.as_bytes()).unwrap()
    }
    fn run(q: Request, mut fx: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut flow = Flow::new(q).unwrap();
        let mut trace = vec![];
        for _ in 0..10000 {
            let e = flow.next().clone();
            if e.kind == b"result" {
                return (e, trace);
            }
            let r = fx(&e);
            trace.push(e);
            flow.advance(r).unwrap()
        }
        panic!("unfinished")
    }
    fn ok() -> Reply {
        Reply {
            ok: true,
            ..Reply::default()
        }
    }
    #[test]
    fn singbox_protocol_eligibility_and_raw_credentials() {
        let b = base();
        assert!(singbox_plan(&b, &[], b"vmess").is_none());
        assert!(singbox_plan(&b, &[], b"tuic").is_none());
        for typ in [
            b"ss".as_slice(),
            b"shadowsocks",
            b"vmess",
            b"vless",
            b"trojan",
            b"hysteria2",
            b"tuic",
        ] {
            let fs = vec![
                f("method", "cipher", 3),
                f("password", " ", 4),
                f("uuid", " ", 5),
            ];
            let (a, n, _) = singbox_plan(&b, &fs, typ).unwrap();
            assert_eq!(n.name, b"Name");
            assert_eq!(get(&a, "server").reference, 102);
            assert_eq!(get(&a, "port").value, -1);
            assert_eq!(has(&a, "udp"), typ != b"hysteria2");
            assert_eq!(
                n.network,
                if typ == b"tuic" || typ == b"hysteria2" {
                    b"udp".as_slice()
                } else {
                    b"tcp"
                }
            );
        }
    }
    #[test]
    fn singbox_vmess_defaults_tls_and_transport_dialect() {
        let fs = vec![
            f("uuid", "opaque", 8),
            f("security", " ", 9),
            count("tls", 1),
            flag("tls.enabled", true),
            f("tls.server_name", "sni", 10),
            flag("tls.insecure", true),
            count("tls.alpn", 2),
            count("transport", 1),
            f("transport.type", "UNKNOWN", 11),
            f("transport.host", "h", 12),
            f("transport.path", "p", 13),
        ];
        let (a, n, t) = singbox_plan(&base(), &fs, b"vmess").unwrap();
        assert_eq!(get(&a, "cipher").text, b"auto");
        assert!(has(&a, "tls") && has(&a, "skip-cert-verify") && has(&a, "alpn"));
        assert!(n.tls && n.ws_host && n.ws_path);
        assert_eq!(n.network, b"UNKNOWN");
        assert_eq!(t, Some(vec![13, 12, 13]));
    }
    #[test]
    fn reality_only_under_enabled_tls_and_trojan_tls_metadata_only() {
        let mut fs = vec![
            f("uuid", "u", 1),
            f("password", "p", 2),
            count("tls", 1),
            flag("tls.enabled", false),
            count("tls.reality", 1),
            flag("tls.reality.enabled", true),
            f("tls.reality.public_key", "", 3),
            f("tls.reality.short_id", "id", 4),
        ];
        let (a, n, _) = singbox_plan(&base(), &fs, b"vless").unwrap();
        assert!(!has(&a, "reality-opts.public-key") && !n.tls);
        fs[3].boolean = true;
        let (a, _, _) = singbox_plan(&base(), &fs, b"vless").unwrap();
        assert_eq!(get(&a, "reality-opts.public-key").reference, 3);
        let (a, n, _) = singbox_plan(&base(), &fs, b"trojan").unwrap();
        assert!(n.tls);
        assert!(!has(&a, "tls") && !has(&a, "reality-opts.public-key"));
    }
    #[test]
    fn hysteria_obfs_and_tuic_one_credential_are_independent() {
        let fs = vec![
            f("password", "p", 4),
            f("uuid", "", 5),
            count("obfs", 1),
            f("obfs.type", "x", 6),
            f("obfs.password", "p", 7),
            f("congestion_controller", "bbr", 8),
        ];
        let (a, n, _) = singbox_plan(&base(), &fs, b"hysteria2").unwrap();
        assert!(!has(&a, "udp"));
        assert_eq!(get(&a, "obfs-password").reference, 7);
        assert!(n.tls);
        let (a, _, _) = singbox_plan(&base(), &fs, b"tuic").unwrap();
        assert_eq!(get(&a, "uuid").reference, 5);
        assert!(has(&a, "congestion-controller"));
    }
    #[test]
    fn empty_singbox_distinct_from_all_skipped_shape() {
        let (r, t) = run(Request::default(), |_| ok());
        assert_eq!(r.reference, 0);
        assert_eq!(t.len(), 1);
        let (r, t) = run(Request::default(), |e| match e.kind.as_slice() {
            b"decode-singbox" => Reply {
                tokens: vec![1],
                ..ok()
            },
            b"decode-base" => Reply {
                fields: vec![f("tag", "", 1)],
                ..ok()
            },
            b"proxies-build" => Reply { token: 2, ..ok() },
            b"nodes-build" => Reply { token: 3, ..ok() },
            b"nodes-unique-child" => Reply { token: 4, ..ok() },
            _ => ok(),
        });
        assert_eq!((r.reference, r.index), (2, 4));
        let p = t.iter().find(|e| e.kind == b"proxies-build").unwrap();
        assert!(p.flag);
        assert!(!p.tokens_present);
    }
    #[test]
    fn informational_filtered_before_protocol_decode() {
        let name = "剩余流量";
        let (_, trace) = run(Request::default(), |e| {
            if e.kind == b"decode-singbox" {
                Reply {
                    tokens: vec![1],
                    ..ok()
                }
            } else if e.kind == b"decode-base" {
                Reply {
                    fields: vec![f("tag", name, 1), f("type", "vmess", 2)],
                    ..ok()
                }
            } else {
                ok()
            }
        });
        assert!(!trace.iter().any(|e| e.kind == b"decode-outbound"));
    }
    #[test]
    fn direct_clash_priority_and_original_proxy_tokens() {
        let (r, t) = run(
            Request {
                op: 1,
                body: 1,
                ..Request::default()
            },
            |e| match e.kind.as_slice() {
                b"decode-clash" => Reply {
                    count: 2,
                    tokens: vec![4, 5],
                    ..ok()
                },
                b"proxy-facts" => Reply {
                    text: if e.token == 4 { b"A".to_vec() } else { vec![] },
                    ..ok()
                },
                b"proxies-build" => {
                    assert_eq!(e.tokens, vec![4]);
                    Reply { token: 10, ..ok() }
                }
                b"yaml-nodes-child" => Reply { token: 11, ..ok() },
                _ => ok(),
            },
        );
        assert_eq!((r.reference, r.index), (10, 11));
        assert!(!t.iter().any(|e| e.kind == b"base64"));
    }
    #[test]
    fn decoded_clash_and_ensure_preserve_body_tokens_and_double_yaml_call() {
        let mut yaml = 0;
        let (r, t) = run(
            Request {
                op: 2,
                body: 1,
                ..Request::default()
            },
            |e| match e.kind.as_slice() {
                b"base64" => Reply { token: 99, ..ok() },
                b"yaml-nodes-child" => {
                    yaml += 1;
                    Reply {
                        token: 100 + yaml,
                        count: 1,
                        ..ok()
                    }
                }
                _ => ok(),
            },
        );
        assert_eq!((r.token, r.reference, r.index), (99, 0, 102));
        assert_eq!(t.len(), 3);
    }
    #[test]
    fn ensure_original_yaml_does_not_reencode() {
        let (r, t) = run(
            Request {
                op: 2,
                body: 1,
                ..Request::default()
            },
            |e| match e.kind.as_slice() {
                b"base64" => Reply::default(),
                b"body-facts" => Reply { count: 1, ..ok() },
                b"yaml-nodes-child" => Reply {
                    count: 1,
                    token: 9,
                    ..ok()
                },
                _ => ok(),
            },
        );
        assert_eq!((r.token, r.index), (1, 9));
        assert!(!t.iter().any(|e| e.kind == b"marshal-proxies"));
    }
    fn uri_prefix(e: &Effect) -> Option<Reply> {
        Some(match e.kind.as_slice() {
            b"body-facts" => Reply {
                fields: vec![flag("uri", true)],
                ..ok()
            },
            b"decode-clash" => ok(),
            b"lines" => Reply {
                tokens: vec![8],
                ..ok()
            },
            b"line-facts" => Reply {
                token: 9,
                flag: true,
                text: b"vmess".to_vec(),
                ..ok()
            },
            b"vmess-base64" => Reply { token: 10, ..ok() },
            _ => return None,
        })
    }
    #[test]
    fn vmess_uri_placeholder_and_node_before_proxy_and_ignored_transport_error() {
        let (r, t) = run(
            Request {
                op: 1,
                body: 1,
                ..Request::default()
            },
            |e| {
                if let Some(r) = uri_prefix(e) {
                    return r;
                }
                match e.kind.as_slice() {
                    b"decode-vmess" => Reply {
                        fields: vec![
                            f("ps", "", 1),
                            f("fallback", "host:<nil>", 2),
                            f("tls", " false ", 3),
                            f("net", "", 4),
                            f("id", " ", 5),
                        ],
                        ..ok()
                    },
                    b"node-new" => {
                        assert_eq!(e.node.kind, b"Vmess");
                        assert_eq!(e.node.network, b"tcp");
                        assert!(!e.node.tls);
                        Reply { token: 20, ..ok() }
                    }
                    b"map-new" => {
                        assert_eq!(
                            get(&e.fields, "uuid").text,
                            b"00000000-0000-0000-0000-000000000000"
                        );
                        Reply { token: 21, ..ok() }
                    }
                    b"transport-child" => {
                        assert!(e.text.is_empty());
                        Reply::default()
                    }
                    b"proxies-build" => Reply { token: 30, ..ok() },
                    b"nodes-build" => Reply { token: 31, ..ok() },
                    b"nodes-unique-child" => Reply { token: 32, ..ok() },
                    _ => ok(),
                }
            },
        );
        assert_eq!(r.error, 0);
        let n = t.iter().position(|e| e.kind == b"node-new").unwrap();
        let p = t.iter().position(|e| e.kind == b"map-new").unwrap();
        assert!(n < p);
    }
    #[test]
    fn url_node_without_proxy_is_not_zipped_or_rolled_back() {
        let (r, t) = run(
            Request {
                op: 1,
                body: 1,
                ..Request::default()
            },
            |e| {
                if e.kind == b"line-facts" {
                    return Reply {
                        token: 9,
                        flag: true,
                        text: b"trojan".to_vec(),
                        ..ok()
                    };
                }
                if let Some(r) = uri_prefix(e) {
                    return r;
                }
                match e.kind.as_slice() {
                    b"decode-uri" => Reply {
                        text: b"name".to_vec(),
                        fields: vec![
                            f("security", "", 1),
                            f("tls", "", 2),
                            f("host", "", 3),
                            f("path", "", 4),
                        ],
                        ..ok()
                    },
                    b"node-new" => {
                        assert!(e.node.tls);
                        Reply { token: 22, ..ok() }
                    }
                    b"uri-child" => Reply::default(),
                    _ => ok(),
                }
            },
        );
        assert_eq!(r.error, 2);
        assert!(t.iter().any(|e| e.kind == b"node-new"));
        assert!(!t.iter().any(|e| e.kind == b"nodes-build"));
    }
    #[test]
    fn unique_public_used_map_calls_skip_suffix_collisions() {
        let mut used = HashSet::from([b"N".to_vec(), b"N (2)".to_vec()]);
        let (r, t) = run(
            Request {
                op: 4,
                name: b"N".to_vec(),
                ..Request::default()
            },
            |e| match e.kind.as_slice() {
                b"used-name" => Reply {
                    flag: used.contains(&e.text),
                    ..ok()
                },
                b"use-name" => {
                    used.insert(e.text.clone());
                    ok()
                }
                _ => ok(),
            },
        );
        assert_eq!(r.text, b"N (3)");
        assert_eq!(t.len(), 4);
    }
    #[test]
    fn empty_merge_has_no_marshal() {
        let (r, t) = run(
            Request {
                op: 3,
                ..Request::default()
            },
            |_| Reply { token: 1, ..ok() },
        );
        assert_eq!(r.error, 2);
        assert_eq!(t.len(), 1);
    }
    #[test]
    fn limits_rejected_without_step_mutation() {
        let mut f = Flow::new(Request::default()).unwrap();
        let before = f.next().clone();
        assert_eq!(
            f.advance(Reply {
                tokens: vec![1; 100001],
                ..ok()
            }),
            Err(Error::Limit)
        );
        assert_eq!(f.next(), &before);
    }
    #[test]
    fn merge_order_dns_first_wins_duplicate_last_reference_and_alias_plan() {
        let mut list_id = 1000i64;
        let mut lists = HashMap::<i64, Vec<i64>>::new();
        let mut names = HashMap::from([
            (11i64, b"N".to_vec()),
            (12, b"N".to_vec()),
            (21, b"N (2)".to_vec()),
        ]);
        let mut group_names = HashMap::from([(301i64, b"G".to_vec()), (302, b"G".to_vec())]);
        let mut applied_dns = vec![];
        let mut rules = vec![];
        let mut renames = vec![];
        let (r, trace) = run(
            Request {
                op: 3,
                sources: vec![
                    Source {
                        body: 1,
                        label: b"L".to_vec(),
                        ..Source::default()
                    },
                    Source {
                        body: 2,
                        label: b"L".to_vec(),
                        ..Source::default()
                    },
                ],
                sources_present: true,
                ..Request::default()
            },
            |e| match e.kind.as_slice() {
                b"merge-new" => Reply { token: 99, ..ok() },
                b"decode-clash" => Reply {
                    count: 1,
                    tokens: if e.token == 1 { vec![11, 12] } else { vec![21] },
                    ..ok()
                },
                b"proxy-facts" => Reply {
                    text: names[&e.token].clone(),
                    ..ok()
                },
                b"proxies-build" => {
                    list_id += 1;
                    lists.insert(list_id, e.tokens.clone());
                    Reply {
                        token: list_id,
                        ..ok()
                    }
                }
                b"proxies-items" => Reply {
                    tokens: lists[&e.token].clone(),
                    ..ok()
                },
                b"yaml-nodes-child" => Reply {
                    token: 100 + e.token,
                    ..ok()
                },
                b"nodes-items" => Reply {
                    tokens: vec![e.token + 100],
                    ..ok()
                },
                b"dns-child" => Reply {
                    names: vec![b"same".to_vec()],
                    tokens: vec![e.token + 500],
                    ..ok()
                },
                b"merge-dns" => {
                    applied_dns.push(e.reference);
                    ok()
                }
                b"map-assign" => {
                    names.insert(e.token, e.fields[0].text.clone());
                    ok()
                }
                b"nodes-rename" => {
                    renames.push((e.token, e.index, e.text.clone(), e.key.clone()));
                    ok()
                }
                b"policy-decode" => Reply {
                    token: 200 + e.token,
                    ..ok()
                },
                b"policy-clone-groups" => Reply {
                    tokens: vec![e.token + 100],
                    ..ok()
                },
                b"group-facts" => Reply {
                    text: group_names[&e.token].clone(),
                    names: vec![b"N".to_vec(), b"G".to_vec(), b"DIRECT".to_vec()],
                    ..ok()
                },
                b"group-name" => {
                    group_names.insert(e.token, e.text.clone());
                    ok()
                }
                b"policy-rules" => Reply {
                    names: vec![b"DOMAIN,x, N ,no-resolve".to_vec(), b"MATCH,G".to_vec()],
                    ..ok()
                },
                b"merge-rule" => {
                    rules.push(e.text.clone());
                    ok()
                }
                b"merge-marshal" => {
                    assert!(e.flag);
                    assert_eq!((e.index, e.value), (2, 4));
                    Reply { token: 800, ..ok() }
                }
                b"merge-node-list" => Reply { token: 801, ..ok() },
                b"nodes-unique-child" => Reply { token: 802, ..ok() },
                _ => ok(),
            },
        );
        assert_eq!((r.token, r.index, r.error), (800, 802, 0));
        assert_eq!(applied_dns, vec![501]);
        assert_eq!(names[&11], b"N");
        assert_eq!(names[&12], b"N (2)");
        assert_eq!(names[&21], b"N (2) (2)");
        assert_eq!(rules[0], b"DOMAIN,x,N (2),no-resolve");
        assert_eq!(rules[1], b"MATCH,L / G");
        assert_eq!(rules[3], b"MATCH,L (2) / G");
        assert_eq!(renames.len(), 2); // Deliberately fewer UI nodes than proxy maps.
        let section = trace
            .iter()
            .position(|e| e.kind == b"section-append")
            .unwrap();
        let clone = trace
            .iter()
            .position(|e| e.kind == b"policy-clone-groups")
            .unwrap();
        assert!(section < clone);
        assert!(!trace
            .iter()
            .any(|e| e.kind == b"group-member" && e.index == 2));
    }
    #[test]
    fn exact_go_unicode_lower_fact_selects_singbox_without_full_case_expansion() {
        for (raw, normalized) in [("hysterİa2", "hysteria2"), ("tuİc", "tuic")] {
            let (result, trace) = run(Request::default(), |e| match e.kind.as_slice() {
                b"decode-singbox" => Reply {
                    tokens: vec![1],
                    ..ok()
                },
                b"decode-base" => {
                    let mut fields = base();
                    let mut kind = f("type", raw, 20);
                    kind.lower = normalized.as_bytes().to_vec();
                    fields.push(kind);
                    Reply { fields, ..ok() }
                }
                b"decode-outbound" => {
                    assert_eq!(e.key, normalized.as_bytes());
                    Reply {
                        fields: vec![f("password", "opaque", 21)],
                        ..ok()
                    }
                }
                b"map-new" => Reply { token: 30, ..ok() },
                b"node-new" => {
                    assert_eq!(e.node.kind, normalized.as_bytes());
                    Reply { token: 31, ..ok() }
                }
                b"proxies-build" => Reply { token: 32, ..ok() },
                b"nodes-build" => Reply { token: 33, ..ok() },
                b"nodes-unique-child" => Reply { token: 34, ..ok() },
                _ => ok(),
            });
            assert_eq!((result.reference, result.index, result.error), (32, 34, 0));
            assert!(trace.iter().any(|e| e.kind == b"decode-outbound"));
        }
    }
}
