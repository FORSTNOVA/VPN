//! Node query orchestration. Raw node fields, credentials, HTTP and clock values stay in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Node {
    pub token: i64,
    pub name: Vec<u8>,
    pub network: bool,
    pub label: Vec<u8>,
    pub manual: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Source {
    pub matches: bool,
    pub label: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Provider {
    pub token: i64,
    pub name: Vec<u8>,
    pub valid: bool,
    pub informational: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Projection {
    pub node: i64,
    pub detail: i64,
    pub detail_network: bool,
    pub selected: bool,
    pub label: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub cached: Vec<Node>,
    pub manual: Vec<Node>,
    pub sources: Vec<Source>,
    pub subscription: u8,
    pub selected: Vec<u8>,
    pub proxy: bool,
    pub timeout: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub borrowed: bool,
    pub timeout: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub value: i64,
    pub token: i64,
    pub name: Vec<u8>,
    pub nodes: Vec<Node>,
    pub providers: Vec<Provider>,
    pub snapshot: Option<Snapshot>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub name: Vec<u8>,
    pub nodes: Vec<Projection>,
    pub nil_nodes: bool,
    pub proxy: bool,
    pub error: u8,
    pub token: i64,
    pub latency: i64,
}

use crate::node_query_logic;
use std::collections::{BTreeMap, BTreeSet};
const LIMIT: usize = 100_000;
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    locked: bool,
    owns: bool,
    capture_kind: u8,
    capture_name: Vec<u8>,
    capture_after: u16,
    capture_reply: Reply,
    s: Snapshot,
    selected: Vec<u8>,
    proxy: bool,
    running: bool,
    details: BTreeMap<Vec<u8>, Node>,
    seen: BTreeSet<Vec<u8>>,
    out: Vec<Projection>,
    nil_nodes: bool,
    status: i64,
    message: i64,
    error: u8,
    error_token: i64,
    latency: i64,
    duration: i64,
    deadline: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 5 {
            return Err("operation");
        }
        let mut f = Self {
            locked: q.borrowed,
            q,
            e: Effect::default(),
            pc: 0,
            owns: false,
            capture_kind: 0,
            capture_name: vec![],
            capture_after: 0,
            capture_reply: Reply::default(),
            s: Snapshot::default(),
            selected: vec![],
            proxy: false,
            running: false,
            details: BTreeMap::new(),
            seen: BTreeSet::new(),
            out: vec![],
            nil_nodes: true,
            status: 0,
            message: 0,
            error: 0,
            error_token: 0,
            latency: 0,
            duration: 0,
            deadline: 0,
        };
        match f.q.op {
            0 | 2 | 3 => f.emit(1, 10),
            1 => f.list(),
            4 => {
                f.emit(13, 400);
                f.e.a = f.q.timeout
            }
            5 => {
                f.duration = f.q.timeout;
                f.delay()
            }
            _ => unreachable!(),
        };
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = Effect {
            kind: k,
            ..Effect::default()
        };
        self.pc = pc
    }
    fn named(&mut self, k: u8, name: &[u8], pc: u16) {
        self.emit(k, pc);
        self.e.name = name.to_vec()
    }
    fn capture(&mut self, k: u8, name: &[u8], pc: u16) {
        if self.locked {
            self.named(k, name, pc)
        } else {
            self.capture_kind = k;
            self.capture_name = name.to_vec();
            self.capture_after = pc;
            self.emit(1, 900)
        }
    }
    fn output(&mut self) {
        self.e.a = self.status;
        self.e.b = self.message;
        self.e.error = self.error;
        self.e.token = self.error_token;
        self.e.latency = self.latency;
        self.e.name = self.selected.clone();
        self.e.nodes = self.out.clone();
        self.e.nil_nodes = self.nil_nodes;
        self.e.proxy = self.proxy
    }
    fn terminal(&mut self) {
        self.emit(63, 9999);
        self.output()
    }
    fn done(&mut self) {
        if self.owns {
            self.emit(2, 9998)
        } else {
            self.terminal()
        }
    }
    fn respond(&mut self) {
        self.emit(16, 9997);
        self.output()
    }
    fn fail(&mut self, status: i64, message: i64) {
        self.status = status;
        self.message = message;
        self.respond()
    }
    fn list(&mut self) {
        self.error = 0;
        self.error_token = 0;
        self.nil_nodes = true;
        self.out.clear();
        self.details.clear();
        self.seen.clear();
        self.capture(4, b"", 100)
    }
    fn list_done(&mut self) {
        match self.q.op {
            0 => {
                if self.error != 0 {
                    self.proxy = false;
                    self.fail(502, 1)
                } else {
                    self.respond()
                }
            }
            1 => self.done(),
            4 => {
                if self.error == 0 && !self.out.is_empty() {
                    self.out.clear();
                    self.nil_nodes = true;
                    self.done()
                } else {
                    self.error = 0;
                    self.error_token = 0;
                    self.out.clear();
                    self.nil_nodes = true;
                    self.emit(15, 402);
                    self.e.a = 50_000_000
                }
            }
            _ => unreachable!(),
        }
    }
    fn delay(&mut self) {
        self.capture(4, b"", 200)
    }
    fn delay_done(&mut self) {
        match self.q.op {
            5 => self.done(),
            2 => {
                if self.error != 0 {
                    self.fail(502, 3)
                } else {
                    self.capture(4, b"", 309)
                }
            }
            3 => {
                if self.error != 0 {
                    self.fail(502, 7)
                } else {
                    self.respond()
                }
            }
            _ => unreachable!(),
        }
    }
    fn active_done(&mut self) {
        if self.selected.is_empty() {
            self.fail(502, 4)
        } else {
            let n = self.selected.clone();
            self.named(17, &n, 312)
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("reply kind");
        }
        if r.nodes.len() > LIMIT || r.providers.len() > LIMIT {
            return Err("limit");
        }
        if r.kind == 5 && r.snapshot.is_none() {
            return Err("metadata required");
        }
        if let Some(s) = &r.snapshot {
            if s.cached.len() > LIMIT
                || s.manual.len() > LIMIT
                || s.sources.len() > LIMIT
                || s.subscription > 2
            {
                return Err("snapshot");
            }
        }
        self.process(self.pc, r);
        Ok(())
    }
    fn process(&mut self, pc: u16, r: Reply) {
        match pc {
            900 => {
                self.locked = true;
                self.owns = true;
                let n = self.capture_name.clone();
                self.named(self.capture_kind, &n, 901)
            }
            901 => {
                self.capture_reply = r;
                self.emit(2, 902)
            }
            902 => {
                self.locked = false;
                self.owns = false;
                let reply = std::mem::take(&mut self.capture_reply);
                self.process(self.capture_after, reply)
            }
            10 => {
                self.locked = true;
                self.owns = true;
                self.emit(3, 11)
            }
            11 => {
                self.running = r.flag;
                match self.q.op {
                    0 => self.named(5, b"nodes", 12),
                    2 => self.emit(2, 20),
                    3 => {
                        if !self.running {
                            self.fail(409, 6)
                        } else {
                            self.named(5, b"timeout", 30)
                        }
                    }
                    _ => unreachable!(),
                }
            }
            12 => {
                self.s = r.snapshot.unwrap();
                self.proxy = self.s.proxy;
                self.emit(2, 13)
            }
            13 => {
                self.locked = false;
                self.owns = false;
                if self.running {
                    self.list()
                } else {
                    self.proxy = false;
                    self.out = node_query_logic::offline(&self.s);
                    self.nil_nodes = self.out.is_empty();
                    self.respond()
                }
            }
            20 => {
                self.locked = false;
                self.owns = false;
                if self.running {
                    self.duration = 8_000_000_000;
                    self.delay()
                } else {
                    self.fail(409, 2)
                }
            }
            30 => {
                self.duration = r.snapshot.unwrap().timeout;
                self.delay()
            }
            100 => self.named(6, b"SmartVPN", 101),
            101 => {
                if !r.ok {
                    self.error = 1;
                    self.error_token = r.token;
                    self.list_done()
                } else {
                    self.selected = r.name;
                    self.capture(5, b"list", 102)
                }
            }
            102 => {
                self.s = r.snapshot.unwrap();
                self.emit(7, 103)
            }
            103 => {
                if r.ok {
                    self.emit(8, 104)
                } else {
                    self.details = node_query_logic::details(&self.s, vec![]);
                    self.named(9, b"SmartVPNSubscription", 105)
                }
            }
            104 => {
                self.details = node_query_logic::details(&self.s, r.nodes);
                self.named(9, b"SmartVPNSubscription", 105)
            }
            105 => {
                if r.ok {
                    node_query_logic::append_provider(
                        &self.details,
                        &self.selected,
                        &mut self.seen,
                        r.providers,
                        &mut self.out,
                    )
                };
                self.named(9, b"SmartVPNManual", 106)
            }
            106 => {
                if r.ok {
                    node_query_logic::append_provider(
                        &self.details,
                        &self.selected,
                        &mut self.seen,
                        r.providers,
                        &mut self.out,
                    )
                };
                self.nil_nodes = false;
                self.selected.clear();
                self.list_done()
            }
            200 => {
                self.emit(10, 201);
                self.e.a = self.duration;
                self.e.b = (self.q.op != 5) as i64
            }
            201 => {
                if !r.ok {
                    self.error = 1;
                    self.error_token = r.token;
                    self.delay_done()
                } else if r.value != 200 {
                    self.error = 2;
                    self.error_token = r.value;
                    self.emit(12, 203)
                } else {
                    self.emit(11, 202)
                }
            }
            202 => {
                if !r.ok {
                    self.error = 1;
                    self.error_token = r.token
                };
                self.emit(12, 203)
            }
            203 => self.delay_done(),
            309 => self.named(6, b"SmartVPN", 310),
            310 => {
                if !r.ok {
                    self.selected.clear();
                    self.active_done()
                } else {
                    self.selected = r.name;
                    if self.selected == b"SmartVPNAuto" || self.selected == b"SmartVPNFallback" {
                        let n = self.selected.clone();
                        self.named(6, &n, 311)
                    } else {
                        self.active_done()
                    }
                }
            }
            311 => {
                if r.ok {
                    self.selected = r.name
                };
                self.active_done()
            }
            312 => {
                self.latency = r.value;
                if self.latency <= 0 {
                    self.fail(502, 5)
                } else {
                    self.respond()
                }
            }
            400 => {
                self.deadline = r.token;
                self.emit(14, 401);
                self.e.a = self.deadline
            }
            401 => {
                if r.flag {
                    self.list()
                } else {
                    self.error = 3;
                    self.error_token = 0;
                    self.done()
                }
            }
            402 => {
                self.emit(14, 401);
                self.e.a = self.deadline
            }
            9997 => self.done(),
            9998 => {
                self.locked = false;
                self.owns = false;
                self.terminal()
            }
            9999 => self.emit(0, 10000),
            _ => unreachable!(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn run(q: Request, mut reply: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(q).unwrap();
        let mut trace = vec![];
        for _ in 0..1000 {
            let e = f.next();
            if e.kind == 63 {
                return (e, trace);
            };
            let mut r = reply(&e);
            r.kind = e.kind;
            trace.push(e);
            f.advance(r).unwrap();
        }
        panic!("no terminal")
    }
    fn base(e: &Effect) -> Reply {
        Reply {
            kind: e.kind,
            ok: true,
            snapshot: (e.kind == 5).then(Snapshot::default),
            ..Reply::default()
        }
    }
    fn request(op: u8) -> Request {
        Request {
            op,
            ..Request::default()
        }
    }
    fn node(token: i64, name: &str) -> Node {
        Node {
            token,
            name: name.as_bytes().to_vec(),
            ..Node::default()
        }
    }
    fn provider(token: i64, name: &str) -> Provider {
        Provider {
            token,
            name: name.as_bytes().to_vec(),
            valid: true,
            ..Provider::default()
        }
    }
    #[test]
    fn offline_nil_and_duplicate_projection() {
        let (e, t) = run(request(0), |e| {
            let mut r = base(e);
            if e.kind == 5 {
                r.snapshot = Some(Snapshot {
                    cached: vec![node(1, "a"), node(2, "a")],
                    manual: vec![node(3, "a"), node(4, "b")],
                    selected: b"a".to_vec(),
                    proxy: true,
                    ..Snapshot::default()
                })
            }
            r
        });
        assert_eq!(
            e.nodes.iter().map(|n| n.node).collect::<Vec<_>>(),
            vec![1, 2, 4]
        );
        assert!(e.nodes[0].selected);
        assert!(!e.proxy);
        assert!(!e.nil_nodes);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 3, 5, 2, 16]
        );
        let (e, _) = run(request(0), base);
        assert!(e.nil_nodes);
    }
    #[test]
    fn list_group_fail_fast_raw() {
        let (e, t) = run(request(1), |e| {
            let mut r = base(e);
            if e.kind == 6 {
                r.ok = false;
                r.token = 9
            }
            r
        });
        assert_eq!((e.error, e.token), (1, 9));
        assert!(e.nil_nodes);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 4, 2, 6]
        );
    }
    #[test]
    fn list_provider_failure_ignored_and_detail_manual_override() {
        let (e, t) = run(request(1), |e| {
            let mut r = base(e);
            match e.kind {
                6 => r.name = b"b".to_vec(),
                5 => {
                    r.snapshot = Some(Snapshot {
                        manual: vec![Node {
                            network: true,
                            label: b"manual".to_vec(),
                            ..node(4, "b")
                        }],
                        ..Snapshot::default()
                    })
                }
                8 => r.nodes = vec![node(2, "b")],
                9 => {
                    if e.name == b"SmartVPNSubscription" {
                        r.ok = false
                    } else {
                        r.providers = vec![provider(5, "b")]
                    }
                }
                _ => {}
            }
            r
        });
        assert_eq!(e.nodes.len(), 1);
        assert_eq!((e.nodes[0].node, e.nodes[0].detail), (5, 4));
        assert!(e.nodes[0].detail_network && e.nodes[0].selected);
        assert_eq!(e.nodes[0].label, b"manual");
        assert!(t.iter().position(|e| e.kind == 6) < t.iter().position(|e| e.kind == 7));
    }
    #[test]
    fn list_read_failure_still_manual_provider() {
        let (e, t) = run(request(1), |e| {
            let mut r = base(e);
            if e.kind == 7 {
                r.ok = false
            }
            r
        });
        assert!(!e.nil_nodes);
        assert!(!t.iter().any(|e| e.kind == 8));
        assert_eq!(t.iter().filter(|e| e.kind == 9).count(), 2);
    }
    #[test]
    fn borrowed_list_never_takes_or_releases_lock() {
        let mut q = request(1);
        q.borrowed = true;
        let (_, t) = run(q, base);
        assert!(!t.iter().any(|e| e.kind == 1 || e.kind == 2));
    }
    #[test]
    fn running_nodes_keeps_initial_proxy_not_list_metadata() {
        let (e, _) = run(request(0), |e| {
            let mut r = base(e);
            if e.kind == 3 {
                r.flag = true
            }
            if e.kind == 5 && e.name == b"nodes" {
                r.snapshot.as_mut().unwrap().proxy = true
            }
            r
        });
        assert!(e.proxy);
        assert!(!e.nil_nodes);
    }
    #[test]
    fn delay_status_decode_error_and_close_once() {
        for mode in 0..4 {
            let mut q = request(5);
            q.timeout = -9;
            let (e, t) = run(q, |e| {
                let mut r = base(e);
                if e.kind == 10 {
                    assert_eq!(e.a, -9);
                    assert_eq!(e.b, 0);
                    r.value = if mode == 1 { 204 } else { 200 };
                    if mode == 0 {
                        r.ok = false;
                        r.token = 8
                    }
                }
                if e.kind == 11 && mode == 2 {
                    r.ok = false;
                    r.token = 7
                }
                r
            });
            assert_eq!(
                t.iter().filter(|e| e.kind == 12).count(),
                usize::from(mode != 0)
            );
            assert_eq!(
                e.error,
                match mode {
                    0 | 2 => 1,
                    1 => 2,
                    _ => 0,
                }
            );
            if mode == 1 {
                assert_eq!(e.token, 204)
            };
        }
    }
    #[test]
    fn latency_checks_after_probe_and_nested_failure_preserves_group() {
        let (e, t) = run(request(2), |e| {
            let mut r = base(e);
            match e.kind {
                3 => r.flag = true,
                10 => {
                    assert_eq!(e.a, 8_000_000_000);
                    r.value = 200
                }
                6 => {
                    if e.name == b"SmartVPN" {
                        r.name = b"SmartVPNAuto".to_vec()
                    } else {
                        r.ok = false
                    }
                }
                17 => {
                    assert_eq!(e.name, b"SmartVPNAuto");
                    r.value = 42
                }
                _ => {}
            }
            r
        });
        assert_eq!((e.a, e.latency), (0, 42));
        assert_eq!(e.name, b"SmartVPNAuto");
        assert!(t.iter().position(|e| e.kind == 12) < t.iter().position(|e| e.kind == 6));
    }
    #[test]
    fn latency_empty_nested_and_nonpositive() {
        for mode in 0..3 {
            let (e, _) = run(request(2), |e| {
                let mut r = base(e);
                match e.kind {
                    3 => r.flag = true,
                    10 => r.value = 200,
                    6 => {
                        if mode == 0 {
                            r.ok = false
                        } else if mode == 1 && e.name == b"SmartVPN" {
                            r.name = b"SmartVPNFallback".to_vec()
                        } else if mode == 2 {
                            r.name = b"REJECT".to_vec()
                        }
                    }
                    17 => r.value = -1,
                    _ => {}
                }
                r
            });
            assert_eq!(e.a, 502);
            assert_eq!(e.b, if mode == 2 { 5 } else { 4 });
        }
    }
    #[test]
    fn stopped_http_does_not_query() {
        for op in [2, 3] {
            let (e, t) = run(request(op), base);
            assert_eq!(e.a, 409);
            assert!(!t.iter().any(|e| e.kind == 10));
            assert_eq!(t.iter().filter(|e| e.kind == 2).count(), 1);
            if op == 3 {
                assert_eq!(t[t.len() - 2].kind, 16);
                assert_eq!(t.last().unwrap().kind, 2)
            }
        }
    }
    #[test]
    fn node_latency_health_timeout_response_before_unlock() {
        let (e, t) = run(request(3), |e| {
            let mut r = base(e);
            match e.kind {
                3 => r.flag = true,
                5 => r.snapshot.as_mut().unwrap().timeout = 123_000_000,
                10 => {
                    assert_eq!(e.a, 123_000_000);
                    r.value = 200
                }
                _ => {}
            }
            r
        });
        assert_eq!(e.a, 0);
        assert_eq!(t.iter().filter(|e| e.kind == 1).count(), 1);
        assert_eq!(t[t.len() - 2].kind, 16);
        assert_eq!(t.last().unwrap().kind, 2);
    }
    #[test]
    fn wait_nonpositive_no_list_and_failed_iteration_sleeps_even_overshoot() {
        let (e, t) = run(request(4), base);
        assert_eq!(e.error, 3);
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![13, 14]);
        let mut before = 0;
        let (e, t) = run(request(4), |e| {
            let mut r = base(e);
            if e.kind == 14 {
                before += 1;
                r.flag = before == 1
            }
            if e.kind == 6 {
                r.ok = false;
                r.token = 99
            }
            r
        });
        assert_eq!(e.error, 3);
        assert_eq!(e.token, 0);
        assert!(e.nil_nodes);
        assert_eq!(t.iter().filter(|e| e.kind == 15).count(), 1);
        assert_eq!(t.iter().find(|e| e.kind == 15).unwrap().a, 50_000_000);
    }
    #[test]
    fn wait_usable_result_no_sleep_no_nodes_leak() {
        let (e, t) = run(request(4), |e| {
            let mut r = base(e);
            if e.kind == 14 {
                r.flag = true
            }
            if e.kind == 9 {
                r.providers = vec![provider(1, "valid")]
            }
            r
        });
        assert_eq!(e.error, 0);
        assert!(e.nodes.is_empty() && e.nil_nodes);
        assert!(!t.iter().any(|e| e.kind == 15));
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = Flow::new(request(0)).unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 55,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(e, f.next());
        f.advance(Reply {
            kind: 1,
            ..Reply::default()
        })
        .unwrap();
        f.advance(Reply {
            kind: 3,
            ..Reply::default()
        })
        .unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 5,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(e, f.next());
    }
}

#[cfg(test)]
mod error_projection_tests {
    use super::*;
    #[test]
    fn running_nodes_group_error_does_not_publish_captured_proxy() {
        let mut f = Flow::new(Request {
            op: 0,
            ..Request::default()
        })
        .unwrap();
        loop {
            let e = f.next();
            if e.kind == 16 || e.kind == 63 {
                assert_eq!(e.a, 502);
                assert_eq!(e.b, 1);
                assert!(!e.proxy);
                assert!(e.nil_nodes);
                assert_eq!((e.error, e.token), (1, 44));
                if e.kind == 63 {
                    break;
                }
            }
            let r = Reply {
                kind: e.kind,
                ok: e.kind != 6,
                flag: e.kind == 3,
                token: 44,
                snapshot: (e.kind == 5).then(|| Snapshot {
                    proxy: true,
                    ..Snapshot::default()
                }),
                ..Reply::default()
            };
            f.advance(r).unwrap();
        }
    }
}
