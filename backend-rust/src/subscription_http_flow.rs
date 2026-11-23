//! Subscription HTTP, payload analysis and scheduler decisions over opaque codec/IO facts.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Node {
    pub vmess: bool,
    pub network: Vec<u8>,
    pub tls: bool,
    pub ws_host: bool,
    pub ws_path: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Value {
    pub name: Vec<u8>,
    pub count: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Analysis {
    pub present: bool,
    pub format: Vec<u8>,
    pub bytes: i64,
    pub selectable: i64,
    pub valid_vmess: i64,
    pub info_entries: i64,
    pub ws_host_set: i64,
    pub ws_path_set: i64,
    pub uri_counts: Vec<Value>,
    pub networks: Vec<Value>,
    pub tls_modes: Vec<Value>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub clash: bool,
    pub json: bool,
    pub uri: bool,
    pub info: bool,
    pub host: bool,
    pub path: bool,
    pub count: i64,
    pub other: i64,
    pub text: Vec<u8>,
    pub network: Vec<u8>,
    pub tls: Vec<u8>,
    pub names: Vec<Vec<u8>>,
    pub nodes: Vec<Node>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub text: Vec<u8>,
    pub count: i64,
    pub status: i64,
    pub flag: bool,
    pub interval: i64,
    pub names: Vec<Vec<u8>>,
    pub analysis: Analysis,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}
use std::collections::BTreeMap;
const SCHEMES: [&[u8]; 7] = [
    b"vmess",
    b"vless",
    b"ss",
    b"ssr",
    b"trojan",
    b"hysteria2",
    b"tuic",
];
#[derive(Clone)]
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    a: Analysis,
    index: i64,
    total: i64,
    net: BTreeMap<Vec<u8>, i64>,
    tls: BTreeMap<Vec<u8>, i64>,
    child: Option<Box<Flow>>,
    body_open: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 7 {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            a: Analysis::default(),
            index: 0,
            total: 0,
            net: BTreeMap::new(),
            tls: BTreeMap::new(),
            child: None,
            body_open: false,
        };
        match f.q.op {
            0..=2 => f.emit(1, 100),
            3 => f.emit(23, 1),
            4 | 5 => f.emit(34, 200),
            6 => {
                f.emit(38, 210);
                f.e.interval = 600_000_000_000
            }
            7 => f.emit(1, 220),
            _ => unreachable!(),
        }
        Ok(f)
    }
    pub fn next(&self) -> &Effect {
        if let Some(c) = &self.child {
            c.next()
        } else {
            &self.e
        }
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = Effect {
            kind: k,
            ..Default::default()
        };
        self.pc = pc
    }
    fn done(&mut self) {
        self.a.uri_counts.sort_by(|x, y| x.name.cmp(&y.name));
        self.emit(63, 0);
        self.e.analysis = self.a.clone()
    }
    fn count(map: &mut BTreeMap<Vec<u8>, i64>, key: Vec<u8>) {
        let v = map.entry(key).or_default();
        *v = v.wrapping_add(1)
    }
    fn counts(&mut self) {
        self.index = 0;
        self.uri_next()
    }
    fn uri_next(&mut self) {
        if self.index < 7 {
            self.emit(26, 7);
            self.e.text = SCHEMES[self.index as usize].to_vec()
        } else if self.a.format.windows(10).any(|w| w == b"clash-yaml") {
            self.emit(27, 8)
        } else {
            self.emit(28, 9)
        }
    }
    fn vmess_next(&mut self) {
        if self.index < self.total {
            self.emit(29, 10);
            self.e.count = self.index
        } else {
            self.a.networks = self
                .net
                .iter()
                .map(|(n, c)| Value {
                    name: n.clone(),
                    count: *c,
                })
                .collect();
            self.a.tls_modes = self
                .tls
                .iter()
                .map(|(n, c)| Value {
                    name: n.clone(),
                    count: *c,
                })
                .collect();
            self.done()
        }
    }
    fn format(&mut self, r: &Reply, decoded: bool) -> bool {
        let f: &[u8] = if decoded {
            if r.uri {
                b"base64-uri-list".as_slice()
            } else if r.clash {
                b"base64-clash-yaml"
            } else if r.json {
                b"base64-singbox-json"
            } else {
                return false;
            }
        } else if r.clash {
            b"clash-yaml"
        } else if r.json {
            b"singbox-json"
        } else if r.uri {
            b"uri-list"
        } else {
            return false;
        };
        self.a.format = f.to_vec();
        true
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if r.kind != self.next().kind {
            return Err(Error::Invalid);
        }
        if self.next().kind == 64 {
            return Err(Error::Done);
        }
        if self.next().kind == 63 {
            self.emit(64, 0);
            return Ok(());
        }
        if self.child.is_some() {
            return self.child_reply(r);
        }
        match self.pc {
            1 => {
                self.a.bytes = r.count;
                self.a.present = r.count > 0;
                self.a.selectable = r.other;
                if r.count == 0 {
                    self.a.format = b"empty".to_vec();
                    self.done()
                } else {
                    self.a.format = b"unknown".to_vec();
                    self.emit(24, 3)
                }
            }
            2 => {
                self.a.selectable = r.nodes.len() as i64;
                if self.a.bytes == 0 {
                    self.a.format = b"empty".to_vec();
                    self.done()
                } else {
                    self.a.format = b"unknown".to_vec();
                    self.emit(24, 3)
                }
            }
            3 => {
                if self.format(&r, false) {
                    self.counts()
                } else {
                    self.emit(25, 4)
                }
            }
            4 => {
                if r.ok {
                    self.format(&r, true);
                }
                self.counts()
            }
            5 => {
                self.format(&r, true);
                self.counts()
            }
            7 => {
                self.a.uri_counts.push(Value {
                    name: SCHEMES[self.index as usize].to_vec(),
                    count: r.count,
                });
                self.index += 1;
                self.uri_next()
            }
            8 => {
                for n in r.nodes {
                    if n.vmess {
                        self.a.valid_vmess = self.a.valid_vmess.wrapping_add(1)
                    }
                    Self::count(
                        &mut self.net,
                        if n.network.is_empty() {
                            b"tcp/default".to_vec()
                        } else {
                            n.network
                        },
                    );
                    Self::count(
                        &mut self.tls,
                        if n.tls {
                            b"tls".to_vec()
                        } else {
                            b"none".to_vec()
                        },
                    );
                    self.a.ws_host_set += i64::from(n.ws_host);
                    self.a.ws_path_set += i64::from(n.ws_path)
                }
                self.emit(28, 9)
            }
            9 => {
                self.total = r.count;
                self.index = 0;
                self.vmess_next()
            }
            10 => {
                if r.ok {
                    self.emit(30, 12)
                } else {
                    self.emit(29, 11);
                    self.e.count = self.index;
                    self.e.flag = true
                }
            }
            11 => {
                if r.ok {
                    self.emit(30, 12)
                } else {
                    self.index += 1;
                    self.vmess_next()
                }
            }
            12 => {
                if r.ok {
                    self.a.valid_vmess = self.a.valid_vmess.wrapping_add(1);
                    self.a.info_entries += i64::from(crate::subscription::is_informational_node(
                        &String::from_utf8_lossy(&r.text),
                    ));
                    let n = if r.network.is_empty() {
                        b"tcp/default".to_vec()
                    } else {
                        r.network
                    };
                    if n == b"ws" {
                        self.a.ws_host_set += i64::from(r.host);
                        self.a.ws_path_set += i64::from(r.path)
                    }
                    Self::count(&mut self.net, n);
                    Self::count(
                        &mut self.tls,
                        if r.tls.is_empty() {
                            b"none".to_vec()
                        } else {
                            r.tls
                        },
                    )
                }
                self.index += 1;
                self.vmess_next()
            }
            _ => self.parent_reply(r)?,
        }
        Ok(())
    }
}
impl Flow {
    fn response(&mut self, status: i64, text: &[u8], pc: u16) {
        self.emit(32, pc);
        self.e.status = status;
        self.e.text = text.to_vec()
    }
    fn analyze_child(&mut self, pc: u16) {
        self.pc = pc;
        self.child = Some(Box::new(Self::new(Request { op: 3 }).unwrap()))
    }
    fn child_reply(&mut self, r: Reply) -> Result<(), Error> {
        let c = self.child.as_mut().unwrap();
        c.advance(r)?;
        if c.next().kind == 63 {
            let result = c.a.clone();
            self.child = None;
            self.emit(31, if self.pc == 130 { 131 } else { 133 });
            self.e.text = if self.pc == 131 {
                b"fetch".to_vec()
            } else {
                b"cache".to_vec()
            };
            self.e.analysis = result;
        }
        Ok(())
    }
    fn parent_reply(&mut self, r: Reply) -> Result<(), Error> {
        match self.pc {
            100 => {
                if self.q.op == 2 {
                    self.emit(11, 110)
                } else {
                    self.emit(3, 101)
                }
            }
            101 => {
                if self.q.op == 0 && r.flag {
                    self.response(409, b"use-refresh", 150)
                } else if self.q.op == 1 && !r.flag {
                    self.response(409, b"connect", 150)
                } else {
                    self.emit(4, 102)
                }
            }
            102 => {
                if !r.ok {
                    self.response(502, b"fetch", 150)
                } else if self.q.op == 0 {
                    self.emit(5, 103)
                } else {
                    self.emit(6, 104)
                }
            }
            103 => self.response(200, b"fetch", 150),
            104 => {
                if !r.ok {
                    self.response(502, b"provider", 150)
                } else {
                    self.emit(7, 105);
                    self.e.interval = 15_000_000_000
                }
            }
            105 => {
                if !r.ok {
                    self.response(502, b"wait", 150)
                } else {
                    self.emit(8, 106)
                }
            }
            106 => {
                if !r.ok {
                    self.response(502, b"list", 150)
                } else {
                    self.emit(9, 107)
                }
            }
            107 => self.emit(10, 108),
            108 => self.response(
                if r.ok { 200 } else { 500 },
                if r.ok { b"refresh" } else { b"save" },
                150,
            ),
            150 => self.emit(2, 151),
            151 => self.done(),
            110 => {
                self.total = i64::from(r.flag);
                self.emit(2, 111)
            }
            111 => {
                if self.total == 0 {
                    self.response(400, b"no-url", 152)
                } else {
                    self.emit(12, 112)
                }
            }
            112 => {
                if !r.ok {
                    self.response(400, b"url", 152)
                } else {
                    self.emit(13, 113);
                    self.e.names = r.names;
                    self.e.names.sort()
                }
            }
            113 => self.emit(14, 114),
            114 => {
                if !r.ok {
                    self.response(400, b"url", 152)
                } else {
                    self.emit(15, 115)
                }
            }
            115 => {
                if !r.ok {
                    self.emit(19, 131);
                    self.e.text = b"network".to_vec()
                } else {
                    self.body_open = true;
                    self.emit(16, 116)
                }
            }
            116 => {
                self.total = r.count;
                if r.count > 10 * 1024 * 1024 {
                    self.total = 10 * 1024 * 1024;
                    self.emit(17, 117);
                    self.e.count = self.total
                } else if !r.ok {
                    self.emit(19, 119);
                    self.e.text = b"read".to_vec()
                } else {
                    self.emit(18, 120);
                    self.e.count = self.total
                }
            }
            117 => {
                self.emit(19, 119);
                self.e.text = b"large".to_vec()
            }
            119 => {
                self.emit(18, 120);
                self.e.count = self.total
            }
            120 => self.analyze_child(130),
            131 => self.emit(20, 132),
            132 => {
                if r.ok {
                    self.analyze_child(134)
                } else {
                    self.emit(21, 135)
                }
            }
            133 => self.emit(21, 135),
            135 => {
                if r.ok && r.flag {
                    self.emit(22, 136)
                } else {
                    self.response(200, b"diagnostics", 152)
                }
            }
            136 => self.response(200, b"diagnostics", 152),
            152 => {
                if self.body_open {
                    self.emit(33, 153)
                } else {
                    self.done()
                }
            }
            153 => self.done(),
            200 => {
                if self.q.op == 4 {
                    if r.flag {
                        self.done()
                    } else {
                        self.emit(35, 201)
                    }
                } else if r.flag {
                    self.emit(37, 203)
                } else {
                    self.done()
                }
            }
            201 => self.emit(36, 202),
            202 | 203 => self.done(),
            210 => self.emit(39, 211),
            211 => {
                if r.flag {
                    self.emit(41, 212)
                } else {
                    self.emit(40, 213)
                }
            }
            212 => self.emit(39, 211),
            213 => self.done(),
            220 => self.emit(42, 221),
            221 => self.emit(2, 151),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn ok() -> Reply {
        Reply {
            ok: true,
            ..Default::default()
        }
    }
    fn run(q: Request, mut f: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut flow = Flow::new(q).unwrap();
        let mut trace = vec![];
        for _ in 0..1000 {
            let e = flow.next().clone();
            if e.kind == 63 {
                return (e, trace);
            }
            let mut r = f(&e);
            r.kind = e.kind;
            trace.push(e);
            flow.advance(r).unwrap()
        }
        panic!("not done")
    }
    #[test]
    fn empty_payload_does_not_classify() {
        let (r, t) = run(Request { op: 3 }, |_| Reply { other: 9, ..ok() });
        assert_eq!(t.len(), 1);
        assert_eq!(r.analysis.format, b"empty");
        assert_eq!(r.analysis.selectable, 9);
        assert!(r.analysis.uri_counts.is_empty())
    }
    #[test]
    fn format_priority_original_and_decoded() {
        for decoded in [false, true] {
            let (r, t) = run(Request { op: 3 }, |e| match e.kind {
                23 => Reply { count: 10, ..ok() },
                24 => Reply {
                    clash: !decoded,
                    json: !decoded,
                    uri: !decoded,
                    ..ok()
                },
                25 => Reply {
                    clash: true,
                    json: true,
                    uri: true,
                    ..ok()
                },
                _ => ok(),
            });
            assert_eq!(
                r.analysis.format,
                if decoded {
                    b"base64-uri-list".to_vec()
                } else {
                    b"clash-yaml".to_vec()
                }
            );
            assert_eq!(t.iter().filter(|e| e.kind == 24).count(), 1);
            assert_eq!(
                t.iter().filter(|e| e.kind == 27).count(),
                usize::from(!decoded)
            );
        }
    }
    #[test]
    fn decoded_clash_uses_original_nodes_then_vmess_accumulates() {
        let (r, t) = run(Request { op: 3 }, |e| match e.kind {
            23 => Reply {
                count: 10,
                other: 2,
                ..ok()
            },
            25 => Reply {
                clash: true,
                ..ok()
            },
            27 => Reply {
                nodes: vec![Node {
                    vmess: true,
                    ws_host: true,
                    ..Default::default()
                }],
                ..ok()
            },
            28 => Reply { count: 1, ..ok() },
            29 => Reply {
                ok: e.flag,
                ..Default::default()
            },
            30 => Reply {
                text: "剩余流量".as_bytes().to_vec(),
                network: b"ws".to_vec(),
                host: true,
                path: true,
                ..ok()
            },
            _ => ok(),
        });
        let a = r.analysis;
        assert_eq!(a.valid_vmess, 2);
        assert_eq!(a.info_entries, 1);
        assert_eq!(a.ws_host_set, 2);
        assert_eq!(a.ws_path_set, 1);
        assert_eq!(
            a.tls_modes,
            vec![Value {
                name: b"none".to_vec(),
                count: 2
            }]
        );
        assert_eq!(t.iter().filter(|e| e.kind == 29).count(), 2);
        assert_eq!(a.uri_counts[0].name, b"hysteria2");
    }
    #[test]
    fn base64_failure_still_counts_original() {
        let (r, t) = run(Request { op: 3 }, |e| match e.kind {
            23 => Reply { count: 10, ..ok() },
            25 => Reply {
                ok: false,
                ..Default::default()
            },
            26 => Reply { count: 3, ..ok() },
            _ => ok(),
        });
        assert_eq!(r.analysis.format, b"unknown");
        assert_eq!(r.analysis.uri_counts.len(), 7);
        assert!(!t.iter().any(|e| e.kind == 27));
    }
    #[test]
    fn invalid_vmess_json_and_two_decode_failures_skip() {
        let (r, _) = run(Request { op: 3 }, |e| match e.kind {
            23 => Reply { count: 1, ..ok() },
            24 => Reply { uri: true, ..ok() },
            28 => Reply { count: 2, ..ok() },
            29 => Reply {
                ok: e.count == 1,
                ..Default::default()
            },
            30 => Reply {
                ok: false,
                ..Default::default()
            },
            _ => ok(),
        });
        assert_eq!(r.analysis.valid_vmess, 0)
    }
    #[test]
    fn refresh_save_failure_commits_cache_before_response() {
        let (r, t) = run(Request { op: 1 }, |e| match e.kind {
            3 => Reply { flag: true, ..ok() },
            10 => Reply {
                ok: false,
                ..Default::default()
            },
            _ => ok(),
        });
        assert_eq!(r.analysis, Analysis::default());
        let k: Vec<_> = t.iter().map(|e| e.kind).collect();
        assert_eq!(k, [1, 3, 4, 6, 7, 8, 9, 10, 32, 2]);
        assert_eq!(t[8].status, 500);
        assert_eq!(t[4].interval, 15_000_000_000)
    }
    #[test]
    fn fetch_failure_precedence() {
        for running in [true, false] {
            let (_, t) = run(Request { op: 0 }, |e| match e.kind {
                3 => Reply {
                    flag: running,
                    ..ok()
                },
                4 => Reply {
                    ok: false,
                    ..Default::default()
                },
                _ => ok(),
            });
            assert_eq!(t[t.len() - 2].status, if running { 409 } else { 502 });
            assert_eq!(t.last().unwrap().kind, 2)
        }
    }
    #[test]
    fn diagnostics_oversize_beats_readerror_and_closes_after_response() {
        let (r, t) = run(Request { op: 2 }, |e| match e.kind {
            11 => Reply { flag: true, ..ok() },
            12 => Reply {
                names: vec![b"z".to_vec(), b"a".to_vec()],
                ..ok()
            },
            16 => Reply {
                count: 10 * 1024 * 1024 + 1,
                ok: false,
                ..Default::default()
            },
            20 => Reply {
                ok: false,
                ..Default::default()
            },
            _ => ok(),
        });
        let k: Vec<_> = t.iter().map(|e| e.kind).collect();
        assert_eq!(&k[..3], [1, 11, 2]);
        assert_eq!(t.iter().find(|e| e.kind == 19).unwrap().text, b"large");
        assert_eq!(
            t.iter().find(|e| e.kind == 18).unwrap().count,
            10 * 1024 * 1024
        );
        assert_eq!(&k[k.len() - 2..], [32, 33]);
        assert_eq!(
            t.iter().find(|e| e.kind == 13).unwrap().names,
            [b"a".to_vec(), b"z".to_vec()]
        );
        assert_eq!(r.analysis, Analysis::default())
    }
    #[test]
    fn diagnostics_fetch_failure_still_analyzes_cache() {
        let (_, t) = run(Request { op: 2 }, |e| match e.kind {
            11 => Reply { flag: true, ..ok() },
            15 => Reply {
                ok: false,
                ..Default::default()
            },
            21 => Reply { flag: true, ..ok() },
            _ => ok(),
        });
        assert!(!t.iter().any(|e| e.kind == 33));
        assert_eq!(t.iter().find(|e| e.kind == 31).unwrap().text, b"cache");
        assert!(t.iter().any(|e| e.kind == 22))
    }
    #[test]
    fn scheduler_no_initial_tick_no_identity_and_long_loop() {
        let mut f = Flow::new(Request { op: 6 }).unwrap();
        assert_eq!(f.next().interval, 600_000_000_000);
        f.advance(Reply { kind: 38, ..ok() }).unwrap();
        for _ in 0..100001 {
            f.advance(Reply {
                kind: 39,
                flag: true,
                ..ok()
            })
            .unwrap();
            assert_eq!(f.next().kind, 41);
            f.advance(Reply { kind: 41, ..ok() }).unwrap()
        }
        f.advance(Reply { kind: 39, ..ok() }).unwrap();
        assert_eq!(f.next().kind, 40);
        f.advance(Reply { kind: 40, ..ok() }).unwrap();
        assert_eq!(f.next().kind, 63)
    }
    #[test]
    fn start_stop_only_channel_and_tick_borrows_child() {
        let (_, t) = run(Request { op: 4 }, |_| ok());
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), [34, 35, 36]);
        let (_, t) = run(Request { op: 5 }, |_| Reply { flag: true, ..ok() });
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), [34, 37]);
        let (_, t) = run(Request { op: 7 }, |_| ok());
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), [1, 42, 2])
    }
    #[test]
    fn wrong_reply_preserves_effect() {
        let mut f = Flow::new(Request::default()).unwrap();
        let e = f.next().clone();
        assert_eq!(f.advance(Reply { kind: 9, ..ok() }), Err(Error::Invalid));
        assert_eq!(&e, f.next())
    }
}
