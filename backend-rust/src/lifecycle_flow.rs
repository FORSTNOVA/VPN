//! Instance lifecycle workflow. Resources, raw settings and credentials remain in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub portable: bool,
    pub android: bool,
    pub chains_nil: bool,
    pub strict: bool,
    pub has_url: bool,
    pub selection_empty: bool,
    pub path_default: bool,
    pub store: bool,
    pub proxy: bool,
    pub hops: i64,
    pub socks_port: i64,
    pub connect_timeout: i64,
    pub failure_threshold: i64,
    pub selected: Vec<u8>,
    pub mode: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Node {
    pub token: i64,
    pub name: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Item {
    pub token: i64,
    pub own: bool,
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
    pub value: i64,
    pub token: i64,
    pub name: Vec<u8>,
    pub items: Vec<Item>,
    pub nodes: Vec<Node>,
    pub snapshot: Option<Snapshot>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub name: Vec<u8>,
    pub indices: Vec<i64>,
    pub flags: Vec<bool>,
}

use std::collections::VecDeque;
fn effect(kind: u8) -> Effect {
    Effect {
        kind,
        ..Effect::default()
    }
}
fn set(name: &[u8], flag: bool, value: i64) -> Effect {
    Effect {
        kind: 6,
        a: value,
        b: flag as i64,
        name: name.to_vec(),
        ..Effect::default()
    }
}
fn log(code: i64, name: &[u8], token: i64) -> Effect {
    Effect {
        kind: 46,
        a: code,
        b: token,
        name: name.to_vec(),
        ..Effect::default()
    }
}
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    s: Snapshot,
    queue: VecDeque<Effect>,
    queue_after: u16,
    load_after: u16,
    cache_after: u16,
    sweep_after: u16,
    bundle_after: u16,
    items: Vec<Item>,
    index: usize,
    own: i64,
    nodes: Vec<Node>,
    locked: bool,
    status: i64,
    message: i64,
    error: i64,
    port: i64,
    listener: i64,
    connected: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 10 {
            return Err("operation");
        };
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            s: Snapshot::default(),
            queue: VecDeque::new(),
            queue_after: 0,
            load_after: 0,
            cache_after: 0,
            sweep_after: 0,
            bundle_after: 0,
            items: vec![],
            index: 0,
            own: 0,
            nodes: vec![],
            locked: false,
            status: 0,
            message: 0,
            error: 0,
            port: 0,
            listener: 0,
            connected: false,
        };
        match f.q.op {
            0 => f.emit(1, 1),
            1 => f.load(999),
            2 => f.emit(27, 200),
            3 => f.emit(2, 300),
            4 => f.emit(36, 400),
            5 => f.sequence(vec![effect(41), effect(42)], 999),
            6 | 7 => f.emit(36, 500),
            8 => f.emit(2, 800),
            9 => f.bundle(999),
            10 => f.sweep(999),
            _ => unreachable!(),
        };
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = effect(k);
        self.pc = pc
    }
    fn action(&mut self, e: Effect, pc: u16) {
        self.e = e;
        self.pc = pc
    }
    fn sequence(&mut self, es: Vec<Effect>, after: u16) {
        self.queue = es.into();
        self.queue_after = after;
        self.next_sequence()
    }
    fn next_sequence(&mut self) {
        if let Some(e) = self.queue.pop_front() {
            self.action(e, 990)
        } else {
            self.process(self.queue_after, Reply::default())
        }
    }
    fn done(&mut self) {
        if self.locked {
            self.emit(37, 998)
        } else {
            self.terminal()
        }
    }
    fn terminal(&mut self) {
        self.emit(63, 1000);
        self.e.a = self.status;
        self.e.b = self.error;
        self.e.indices = vec![self.port, self.message]
    }
    fn load(&mut self, after: u16) {
        self.load_after = after;
        self.emit(2, 10)
    }
    fn loaded_decode(&mut self) {
        let mut es = vec![];
        if self.s.chains_nil && self.s.hops >= 2 {
            es.push(set(b"legacy-chain", false, 0))
        }
        if self.s.strict {
            es.push(set(b"strict", false, 0))
        }
        self.sequence(es, 14)
    }
    fn load_defaults(&mut self) {
        let mut es = vec![];
        if self.s.selection_empty {
            es.push(set(b"selection", false, 0))
        }
        if self.s.path_default {
            es.push(set(b"path-default", false, 0))
        }
        self.sequence(es, 18)
    }
    fn cache(&mut self, after: u16) {
        self.cache_after = after;
        self.emit(7, 30)
    }
    fn sweep(&mut self, after: u16) {
        self.sweep_after = after;
        self.emit(12, 40)
    }
    fn sweep_next(&mut self) {
        while self.index < self.items.len() {
            if self.items[self.index].token != self.own {
                self.emit(14, 42);
                self.e.a = self.index as i64;
                return;
            }
            self.index += 1
        }
        self.items.clear();
        self.process(self.sweep_after, Reply::default())
    }
    fn bundle(&mut self, after: u16) {
        self.bundle_after = after;
        self.emit(2, 50)
    }
    fn bundle_name(&self) -> &'static [u8] {
        if self.index == 0 {
            b"mihomo.exe"
        } else {
            b"wintun.dll"
        }
    }
    fn bundle_emit(&mut self, k: u8, pc: u16) {
        self.emit(k, pc);
        self.e.name = self.bundle_name().to_vec()
    }
    fn bundle_next(&mut self) {
        if self.index < 2 {
            self.bundle_emit(16, 52)
        } else {
            self.process(self.bundle_after, Reply::default())
        }
    }
    fn reapply_response(&mut self) {
        self.emit(45, 999);
        self.e.a = self.status;
        self.e.b = self.message
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("reply kind");
        };
        if (matches!(r.kind, 2 | 4) || (r.kind == 21 && r.ok)) && r.snapshot.is_none() {
            return Err("snapshot required");
        };
        if r.items.len() > 100000 || r.nodes.len() > 100000 {
            return Err("limit");
        };
        self.process(self.pc, r);
        Ok(())
    }
    fn process(&mut self, pc: u16, r: Reply) {
        match pc {
            1 => self.load(100),
            10 => {
                self.s = r.snapshot.unwrap();
                self.emit(3, 11)
            }
            11 => {
                if r.ok {
                    self.emit(4, 12)
                } else {
                    self.process(14, Reply::default())
                }
            }
            12 => {
                self.s = r.snapshot.unwrap();
                self.loaded_decode()
            }
            14 => {
                if self.s.has_url {
                    self.emit(5, 15)
                } else {
                    self.load_defaults()
                }
            }
            15 => {
                let mut es = vec![];
                if !r.ok {
                    es.push(log(0, b"", r.token))
                };
                es.push(set(b"url", !r.ok, 0));
                self.sequence(es, 17)
            }
            17 => self.load_defaults(),
            18 => self.cache(19),
            19 => self.process(self.load_after, Reply::default()),
            30 => {
                if r.ok {
                    self.emit(8, 31)
                } else {
                    self.process(self.cache_after, Reply::default())
                }
            }
            31 => {
                if r.ok {
                    self.nodes = r.nodes;
                    self.emit(9, 32)
                } else {
                    self.emit(10, 34)
                }
            }
            32 => {
                if r.flag {
                    self.emit(11, 34);
                    self.e.indices = self
                        .nodes
                        .iter()
                        .enumerate()
                        .filter(|(_, n)| n.name == self.s.selected)
                        .map(|(i, _)| i as i64)
                        .collect()
                } else {
                    self.emit(10, 34)
                }
            }
            34 => {
                self.nodes.clear();
                self.process(self.cache_after, Reply::default())
            }
            40 => {
                if r.ok {
                    self.items = r.items;
                    self.emit(13, 41)
                } else {
                    self.process(self.sweep_after, Reply::default())
                }
            }
            41 => {
                self.own = r.token;
                self.index = 0;
                self.sweep_next()
            }
            42 => {
                self.index += 1;
                self.sweep_next()
            }
            50 => {
                self.s = r.snapshot.unwrap();
                if self.s.android {
                    self.process(self.bundle_after, Reply::default())
                } else {
                    self.emit(15, 51)
                }
            }
            51 => {
                if r.flag {
                    self.index = 0;
                    self.bundle_next()
                } else {
                    self.process(self.bundle_after, Reply::default())
                }
            }
            52 => {
                if r.ok {
                    self.index += 1;
                    self.bundle_next()
                } else {
                    self.bundle_emit(17, 53)
                }
            }
            53 => {
                if r.ok {
                    self.bundle_emit(18, 54)
                } else {
                    self.index += 1;
                    self.bundle_next()
                }
            }
            54 => {
                if r.ok {
                    self.bundle_emit(19, 55)
                } else {
                    self.action(log(1, self.bundle_name(), r.token), 56)
                }
            }
            55 => self.action(
                log(
                    if r.ok { 3 } else { 2 },
                    self.bundle_name(),
                    if r.ok { 0 } else { r.token },
                ),
                56,
            ),
            56 => {
                self.index += 1;
                self.bundle_next()
            }
            100 => self.action(set(b"locks", false, 0), 101),
            101 => self.sweep(102),
            102 => self.emit(2, 103),
            103 => {
                self.s = r.snapshot.unwrap();
                if self.s.portable {
                    self.emit(20, 105)
                } else {
                    self.bundle(104)
                }
            }
            104 => self.emit(20, 105),
            105 => {
                if r.ok {
                    self.emit(21, 106)
                } else {
                    self.action(log(4, b"", r.token), 999)
                }
            }
            106 => {
                if !r.ok {
                    self.action(log(5, b"", r.token), 109)
                } else {
                    self.s = r.snapshot.unwrap();
                    let mut es = vec![];
                    if self.s.android {
                        if self.s.connect_timeout < 5000 {
                            es.push(set(b"timeout", false, 6000))
                        }
                        if self.s.failure_threshold < 3 {
                            es.push(set(b"threshold", false, 3))
                        }
                    }
                    es.push(set(b"params", false, 0));
                    self.sequence(es, 109)
                }
            }
            109 => self.emit(22, 110),
            110 => {
                if r.ok {
                    self.action(set(b"health", false, 0), 111)
                } else {
                    self.action(log(6, b"", r.token), 111)
                }
            }
            111 => self.emit(23, 112),
            112 => {
                if r.ok {
                    self.action(set(b"regions", false, 0), 113)
                } else {
                    self.action(log(7, b"", r.token), 113)
                }
            }
            113 => self.sequence(vec![effect(24), effect(25), effect(26)], 999),
            200 => {
                if r.flag {
                    self.action(log(8, b"", 0), 202)
                } else {
                    self.emit(28, 201)
                }
            }
            201 => {
                if !r.ok {
                    self.action(log(9, b"", r.token), 202)
                } else {
                    self.process(202, Reply::default())
                }
            }
            202 => self.sequence(vec![effect(29), effect(30), effect(31)], 999),
            300 => {
                self.s = r.snapshot.unwrap();
                self.emit(32, 301)
            }
            301 => {
                if !r.ok {
                    self.error = r.token;
                    self.done()
                } else {
                    self.port = r.value;
                    if self.port == self.s.socks_port {
                        self.emit(32, 302);
                        self.e.a = 1
                    } else {
                        self.listener = 0;
                        self.emit(34, 304)
                    }
                }
            }
            302 => {
                self.port = if r.ok { r.value } else { 0 };
                self.error = if r.ok { 0 } else { r.token };
                self.listener = 1;
                self.emit(33, 303)
            }
            303 => {
                if self.error != 0 {
                    self.done()
                } else {
                    self.emit(34, 304)
                }
            }
            304 => {
                self.emit(35, 305);
                self.e.a = self.listener
            }
            305 => self.action(set(b"api-port", false, self.port), 999),
            400 => {
                self.locked = true;
                self.sequence(vec![effect(38), effect(39)], 401)
            }
            401 => self.emit(2, 402),
            402 => {
                self.s = r.snapshot.unwrap();
                let mut es = vec![];
                if self.s.android {
                    es.push(set(b"proxy", false, 0))
                } else if self.s.proxy {
                    es.push(effect(28));
                    es.push(set(b"proxy", false, 0))
                }
                if self.s.store {
                    es.push(effect(40))
                }
                self.sequence(es, 999)
            }
            500 => {
                self.locked = true;
                self.emit(43, 501)
            }
            501 => {
                self.connected = r.flag;
                if self.q.op == 6 {
                    self.emit(37, 502)
                } else if !r.flag {
                    self.status = 409;
                    self.message = 1;
                    self.reapply_response()
                } else {
                    self.emit(44, 503)
                }
            }
            502 => {
                self.locked = false;
                self.emit(45, 999);
                self.e.name = b"identify".to_vec();
                self.e.flags = vec![self.connected]
            }
            503 => {
                if r.name != b"system-proxy" {
                    self.status = 409;
                    self.message = 2;
                    self.reapply_response()
                } else {
                    self.emit(28, 504);
                    self.e.b = 1
                }
            }
            504 => {
                if !r.ok {
                    self.status = 500;
                    self.message = 3;
                    self.reapply_response()
                } else {
                    self.action(set(b"proxy", true, 0), 505)
                }
            }
            505 => self.reapply_response(),
            800 => {
                self.s = r.snapshot.unwrap();
                self.cache(999)
            }
            990 => self.next_sequence(),
            998 => {
                self.locked = false;
                self.terminal()
            }
            999 => self.done(),
            1000 => self.emit(0, 1001),
            _ => unreachable!(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn request(op: u8) -> Request {
        Request { op }
    }
    fn base(e: &Effect) -> Reply {
        Reply {
            kind: e.kind,
            ok: true,
            snapshot: matches!(e.kind, 2 | 4 | 21).then(Snapshot::default),
            ..Reply::default()
        }
    }
    fn run(op: u8, mut cb: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(request(op)).unwrap();
        let mut t = vec![];
        for _ in 0..1000 {
            let e = f.next();
            if e.kind == 63 {
                return (e, t);
            }
            let mut r = cb(&e);
            r.kind = e.kind;
            t.push(e);
            f.advance(r).unwrap()
        }
        panic!("not terminal")
    }
    fn sets(t: &[Effect]) -> Vec<&[u8]> {
        t.iter()
            .filter(|e| e.kind == 6)
            .map(|e| e.name.as_slice())
            .collect()
    }
    #[test]
    fn partial_decode_error_still_migrates_chain_privacy_and_defaults() {
        let (_, t) = run(1, |e| {
            let mut r = base(e);
            if e.kind == 4 {
                r.ok = false;
                r.snapshot = Some(Snapshot {
                    chains_nil: true,
                    hops: 2,
                    strict: true,
                    selection_empty: true,
                    path_default: true,
                    ..Snapshot::default()
                })
            }
            r
        });
        assert_eq!(
            sets(&t),
            vec![
                b"legacy-chain".as_slice(),
                b"strict",
                b"selection",
                b"path-default"
            ]
        )
    }
    #[test]
    fn read_error_skips_legacy_privacy_but_opens_existing_secret() {
        let (_, t) = run(1, |e| {
            let mut r = base(e);
            if e.kind == 2 {
                r.snapshot = Some(Snapshot {
                    chains_nil: true,
                    hops: 2,
                    strict: true,
                    has_url: true,
                    selection_empty: true,
                    ..Snapshot::default()
                })
            }
            if e.kind == 3 {
                r.ok = false
            }
            if e.kind == 5 {
                r.ok = false;
                r.token = 77
            }
            r
        });
        assert_eq!(sets(&t), vec![b"url".as_slice(), b"selection"]);
        assert_eq!(t.iter().find(|e| e.kind == 46).unwrap().b, 77);
        assert_eq!(
            t.iter()
                .find(|e| e.kind == 6 && e.name == b"url")
                .unwrap()
                .b,
            1
        );
    }
    #[test]
    fn old_chain_presence_and_short_hops_not_migrated() {
        for (nil, hops) in [(false, 2), (true, 1)] {
            let (_, t) = run(1, |e| {
                let mut r = base(e);
                if e.kind == 4 {
                    r.snapshot = Some(Snapshot {
                        chains_nil: nil,
                        hops,
                        ..Snapshot::default()
                    })
                }
                r
            });
            assert!(!sets(&t).contains(&b"legacy-chain".as_slice()))
        }
    }
    #[test]
    fn cache_read_failure_preserves_and_corrupt_or_hash_mismatch_removes() {
        for mode in 0..3 {
            let (_, t) = run(8, |e| {
                let mut r = base(e);
                if (mode == 0 && e.kind == 7) || (mode == 1 && e.kind == 8) {
                    r.ok = false
                }
                r
            });
            assert_eq!(
                t.iter().filter(|e| e.kind == 10).count(),
                usize::from(mode != 0)
            );
            assert!(!t.iter().any(|e| e.kind == 11));
        }
    }
    #[test]
    fn cache_selects_all_duplicate_names_and_preserves_order() {
        let (_, t) = run(8, |e| {
            let mut r = base(e);
            if e.kind == 2 {
                r.snapshot.as_mut().unwrap().selected = b"n".to_vec()
            }
            if e.kind == 8 {
                r.nodes = vec![
                    Node {
                        token: 10,
                        name: b"n".to_vec(),
                    },
                    Node {
                        token: 20,
                        name: b"x".to_vec(),
                    },
                    Node {
                        token: 30,
                        name: b"n".to_vec(),
                    },
                ]
            }
            if e.kind == 9 {
                r.flag = true
            }
            r
        });
        assert_eq!(t.last().unwrap().indices, vec![0, 2]);
    }
    #[test]
    fn sweep_keeps_own_and_removes_each_other_index() {
        let (_, t) = run(10, |e| {
            let mut r = base(e);
            if e.kind == 12 {
                r.items = vec![
                    Item {
                        token: 1,
                        own: false,
                    },
                    Item {
                        token: 2,
                        own: true,
                    },
                    Item {
                        token: 1,
                        own: false,
                    },
                    Item {
                        token: 3,
                        own: false,
                    },
                ]
            }
            if e.kind == 13 {
                r.token = 2
            }
            r
        });
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 14)
                .map(|e| e.a)
                .collect::<Vec<_>>(),
            vec![0, 2, 3]
        );
    }
    #[test]
    fn bundle_nonoverwriting_android_and_missing_source() {
        for mode in 0..3 {
            let (_, t) = run(9, |e| {
                let mut r = base(e);
                if e.kind == 2 {
                    r.snapshot.as_mut().unwrap().android = mode == 0
                }
                if e.kind == 15 {
                    r.flag = mode == 2
                }
                r
            });
            assert!(!t.iter().any(|e| e.kind == 19));
            assert_eq!(
                t.iter().filter(|e| e.kind == 16).count(),
                if mode == 2 { 2 } else { 0 }
            )
        }
    }
    #[test]
    fn bundle_read_write_failure_continue_second_file() {
        for failure in [18, 19] {
            let (_, t) = run(9, |e| {
                let mut r = base(e);
                if e.kind == 15 {
                    r.flag = true
                }
                if e.kind == 16 {
                    r.ok = false
                }
                if e.kind == failure && e.name == b"mihomo.exe" {
                    r.ok = false;
                    r.token = 99
                }
                r
            });
            let logs: Vec<_> = t.iter().filter(|e| e.kind == 46).collect();
            assert_eq!(logs.len(), 2);
            assert_eq!(logs[0].a, if failure == 18 { 1 } else { 2 });
            assert_eq!(logs[1].a, 3);
            assert_eq!(logs[1].name, b"wintun.dll");
        }
    }
    #[test]
    fn new_store_failure_logs_without_children() {
        let (e, t) = run(0, |e| {
            let mut r = base(e);
            if e.kind == 20 {
                r.ok = false;
                r.token = 22
            }
            r
        });
        assert_eq!(e.b, 0);
        assert_eq!(t.last().unwrap().a, 4);
        assert!(!t.iter().any(|e| e.kind == 21 || e.kind == 24));
    }
    #[test]
    fn each_database_load_failure_is_independent() {
        for fail in [21, 22, 23] {
            let (_, t) = run(0, |e| {
                let mut r = base(e);
                if e.kind == fail {
                    r.ok = false;
                    r.token = 33
                }
                r
            });
            assert!(t.iter().any(|e| e.kind == 21));
            assert!(t.iter().any(|e| e.kind == 22));
            assert!(t.iter().any(|e| e.kind == 23));
            assert_eq!(
                t.iter().rev().take(3).map(|e| e.kind).collect::<Vec<_>>(),
                vec![26, 25, 24]
            );
        }
    }
    #[test]
    fn android_params_threshold_boundaries() {
        for (timeout, threshold, want) in [(4999, 2, true), (5000, 3, false)] {
            let (_, t) = run(0, |e| {
                let mut r = base(e);
                if matches!(e.kind, 2 | 21) {
                    r.snapshot = Some(Snapshot {
                        android: true,
                        connect_timeout: timeout,
                        failure_threshold: threshold,
                        ..Snapshot::default()
                    })
                }
                r
            });
            let s = sets(&t);
            assert_eq!(s.contains(&b"timeout".as_slice()), want);
            assert_eq!(s.contains(&b"threshold".as_slice()), want);
            assert!(s.contains(&b"params".as_slice()));
        }
    }
    #[test]
    fn prepare_owner_skips_proxy_otherwise_error_is_log_only() {
        for other in [false, true] {
            let (e, t) = run(2, |e| {
                let mut r = base(e);
                if e.kind == 27 {
                    r.flag = other
                }
                if e.kind == 28 {
                    r.ok = false;
                    r.token = 7
                }
                r
            });
            assert_eq!(e.b, 0);
            assert_eq!(
                t.iter().filter(|e| e.kind == 28).count(),
                usize::from(!other)
            );
            assert_eq!(
                t.iter().rev().take(3).map(|e| e.kind).collect::<Vec<_>>(),
                vec![31, 30, 29]
            );
        }
    }
    #[test]
    fn listener_collision_binds_second_before_close_and_error_preserved() {
        for fail in [false, true] {
            let (e, t) = run(3, |e| {
                let mut r = base(e);
                if e.kind == 2 {
                    r.snapshot.as_mut().unwrap().socks_port = 123
                }
                if e.kind == 32 {
                    r.value = if e.a == 0 { 123 } else { 124 };
                    if e.a == 1 && fail {
                        r.ok = false;
                        r.token = 88
                    }
                }
                r
            });
            assert_eq!(
                t.iter().take(4).map(|e| e.kind).collect::<Vec<_>>(),
                vec![2, 32, 32, 33]
            );
            if fail {
                assert_eq!(e.b, 88);
                assert_eq!(e.indices[0], 0);
                assert!(!t.iter().any(|e| e.kind == 34))
            } else {
                assert_eq!(e.indices[0], 124);
                assert_eq!(t.iter().find(|e| e.kind == 35).unwrap().a, 1)
            }
        }
    }
    #[test]
    fn cleanup_platform_differences_ignore_stop_close_errors() {
        for android in [false, true] {
            for proxy in [false, true] {
                let (e, t) = run(4, |e| {
                    let mut r = base(e);
                    if e.kind == 2 {
                        r.snapshot = Some(Snapshot {
                            android,
                            proxy,
                            store: true,
                            ..Snapshot::default()
                        })
                    }
                    if [28, 39, 40].contains(&e.kind) {
                        r.ok = false;
                        r.token = 99
                    }
                    r
                });
                assert_eq!(e.b, 0);
                assert_eq!(
                    t.iter().filter(|e| e.kind == 28).count(),
                    usize::from(!android && proxy)
                );
                assert_eq!(sets(&t).contains(&b"proxy".as_slice()), android || proxy);
                assert_eq!(t.last().unwrap().kind, 37)
            }
        }
    }
    #[test]
    fn identify_unlocks_before_response_and_shutdown_signals_after_disconnect() {
        let (_, t) = run(6, |e| Reply {
            flag: e.kind == 43,
            ..base(e)
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![36, 43, 37, 45]
        );
        assert_eq!(t.last().unwrap().flags, vec![true]);
        let (_, t) = run(5, base);
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![41, 42]);
    }
    #[test]
    fn reapply_branch_errors_have_no_raw_err_and_response_before_unlock() {
        for mode in 0..4 {
            let (e, t) = run(7, |e| {
                let mut r = base(e);
                if e.kind == 43 {
                    r.flag = mode != 0
                }
                if e.kind == 44 {
                    r.name = if mode == 1 {
                        b"tun".to_vec()
                    } else {
                        b"system-proxy".to_vec()
                    }
                }
                if e.kind == 28 && mode == 2 {
                    assert_eq!(e.b, 1);
                    r.ok = false;
                    r.token = 77
                }
                r
            });
            assert_eq!(e.b, 0);
            assert_eq!(e.indices[1], if mode == 3 { 0 } else { mode + 1 });
            assert_eq!(t[t.len() - 2].kind, 45);
            assert_eq!(t.last().unwrap().kind, 37);
            assert_eq!(sets(&t).contains(&b"proxy".as_slice()), mode == 3);
        }
    }
    #[test]
    fn missing_snapshot_rejected_without_consumption() {
        let mut f = Flow::new(request(1)).unwrap();
        let old = f.next();
        assert!(f
            .advance(Reply {
                kind: 2,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), old)
    }
}
