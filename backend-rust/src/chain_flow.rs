//! Chain configuration and compilation decisions. Proxy maps and profile bytes
//! stay in Go; only strict-string presence facts and opaque handles cross FFI.
use std::collections::{BTreeMap, BTreeSet};
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Chain {
    pub enabled: bool,
    pub name: Vec<u8>,
    pub hops: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Saved {
    pub name: Vec<u8>,
    pub hops: Vec<Vec<u8>>,
    pub is_nil: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub subscription: bool,
    pub locked_region: Vec<u8>,
    pub current: Chain,
    pub saved: Vec<Saved>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Proxy {
    pub token: i64,
    pub dialer: bool,
    pub server: bool,
    pub proxy_type: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Group {
    pub name: Vec<u8>,
    pub proxies: Vec<Proxy>,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub next: Chain,
    pub name: Vec<u8>,
    pub original: Vec<u8>,
    pub name_runes: i64,
    pub hops: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub c: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
    pub indices: Vec<i64>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub value: i64,
    pub ttl: i64,
    pub token: i64,
    pub name: Vec<u8>,
    pub error: Vec<u8>,
    pub groups: Vec<Group>,
    pub snapshot: Option<Snapshot>,
}
// ops0update1saveprofile2build3chain4prefix5compile.
// 1Lock,2SnapshotBorrowed,3Running,4JobRunning,5ReadProfile,
// 6Index(a nilProfile override),7InputIndex(compile-only),
// 8CloneProxy(a token,b setDialer,data alias,text previous),
// 9LookupRegion(data exit),10RegionAge(data exit),11Marshal(indices clone tokens),
// 12CommitChain(a0q.Next/1renamedEnabled),13CommitProfiles(data name,text original),
// 14SaveSettings,15Restore(a0chain/1profiles; save rollback emits both in order),
// 63Result(aHTTP,bcode,c errorIndex/opaqueError,data alias,text err,indices tokens),0Done.
// codes0updated1saved2compiled3built4disabled;10invalidupdate11namelen12busyupdate13stale,
// 14originalmissing15nameexists16busysave20chainlen21prefixlen22duplicate23missing24dialer25fields26alias27region28decode30updatesave31profilesave40raw.
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    stage: u8,
    hops: Vec<Vec<u8>>,
    index: BTreeMap<Vec<u8>, Vec<Proxy>>,
    seen: BTreeSet<Vec<u8>>,
    at: usize,
    compiled: Vec<i64>,
    previous: Vec<u8>,
    after: u8,
    active_rename: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 5 {
            return Err("unknown chain op");
        }
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            stage: 0,
            hops: vec![],
            index: BTreeMap::new(),
            seen: BTreeSet::new(),
            at: 0,
            compiled: vec![],
            previous: vec![],
            after: 0,
            active_rename: false,
        };
        match f.q.op {
            0 => {
                if f.q.next.hops.len() > 8 {
                    f.result(400, 10);
                } else {
                    f.emit(1, 1);
                }
            }
            1 => {
                if f.q.name.is_empty() || f.q.name_runes > 80 {
                    f.result(400, 11);
                } else {
                    f.emit(1, 1);
                }
            }
            2 => {
                if !f.q.next.enabled {
                    f.result(0, 4);
                } else {
                    f.emit(2, 1);
                }
            }
            3..=5 => f.emit(2, 1),
            _ => unreachable!(),
        }
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, kind: u8, stage: u8) {
        self.e = Effect {
            kind,
            ..Effect::default()
        };
        self.stage = stage;
    }
    fn result(&mut self, status: i64, code: i64) {
        self.emit(63, 99);
        self.e.a = if self.q.op >= 2 { 0 } else { status };
        self.e.b = code;
    }
    fn raw(&mut self, r: &Reply) {
        self.result(400, 40);
        self.e.c = r.token;
        self.e.text = r.error.clone();
    }
    fn lookup_saved(&self, name: &[u8]) -> Option<&Saved> {
        self.s.saved.iter().find(|s| s.name == name)
    }
    fn begin_compile(&mut self, hops: Vec<Vec<u8>>, after: u8, chain: bool) {
        self.after = after;
        self.hops = hops;
        if chain && !(2..=8).contains(&self.hops.len()) {
            self.result(400, 20);
            return;
        }
        self.emit(6, 20);
        self.e.a = if matches!(after, 1..=4) {
            (!self.s.subscription) as i64
        } else {
            0
        };
    }
    fn next_hop(&mut self) {
        if self.at == self.hops.len() {
            self.compiled_done();
            return;
        }
        let name = self.hops[self.at].clone();
        if !self.seen.insert(name.clone()) {
            self.hop_error(22);
            return;
        }
        let Some(entries) = self.index.get(&name) else {
            self.hop_error(23);
            return;
        };
        if name.is_empty() || entries.len() != 1 {
            self.hop_error(23);
            return;
        }
        let p = entries[0].clone();
        if p.dialer {
            self.hop_error(24);
            return;
        }
        if !p.server || !p.proxy_type {
            self.hop_error(25);
            return;
        }
        let alias = if self.at + 1 == self.hops.len() {
            b"SmartVPNChain".to_vec()
        } else {
            format!("SmartVPNChainHop{}", self.at + 1).into_bytes()
        };
        if self.index.get(&alias).is_some_and(|v| !v.is_empty()) {
            self.result(400, 26);
            self.e.data = alias;
            return;
        }
        self.emit(8, 21);
        self.e.a = p.token;
        self.e.b = (self.at > 0) as i64;
        self.e.data = alias;
        self.e.text = self.previous.clone();
    }
    fn hop_error(&mut self, code: i64) {
        self.result(400, code);
        self.e.c = self.at as i64 + 1;
    }
    fn compiled_done(&mut self) {
        match self.after {
            1 | 3 | 4 => {
                if self.s.locked_region.is_empty() {
                    self.marshal();
                } else {
                    self.emit(9, 22);
                    self.e.data = self.hops.last().unwrap().clone();
                }
            }
            2 => {
                self.active_rename = self.s.current.enabled
                    && !self.q.original.is_empty()
                    && self.s.current.name == self.q.original;
                if self.active_rename {
                    self.emit(3, 10);
                } else {
                    self.commit_profiles();
                }
            }
            5 => {
                self.result(0, 2);
                self.e.indices = self.compiled.clone();
            }
            _ => unreachable!(),
        }
    }
    fn marshal(&mut self) {
        self.emit(11, 24);
        self.e.indices = self.compiled.clone();
    }
    fn commit_profiles(&mut self) {
        self.emit(13, 40);
        self.e.data = self.q.name.clone();
        self.e.text = self.q.original.clone();
    }
    fn after_update_running(&mut self) {
        if self.q.next.enabled {
            if !self.q.next.name.is_empty() {
                let expected = self
                    .lookup_saved(&self.q.next.name)
                    .map(|s| s.hops.as_slice())
                    .unwrap_or(&[]);
                if self.q.next.hops != expected {
                    self.result(409, 13);
                    return;
                }
            }
            self.emit(5, 5);
        } else {
            self.emit(12, 30);
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected chain reply");
        }
        if matches!(self.e.kind, 1 | 2) && r.snapshot.is_none() {
            return Err("missing chain snapshot");
        }
        if let Some(s) = r.snapshot.clone() {
            self.s = s;
        }
        match self.stage {
            1 => match self.q.op {
                0 => self.emit(3, 2),
                1 => {
                    if !self.q.original.is_empty()
                        && self.lookup_saved(&self.q.original).is_none_or(|v| v.is_nil)
                    {
                        self.result(409, 14);
                    } else if self.q.name != self.q.original
                        && self.lookup_saved(&self.q.name).is_some_and(|v| !v.is_nil)
                    {
                        self.result(409, 15);
                    } else {
                        self.emit(5, 6);
                    }
                }
                2 => self.begin_compile(self.q.next.hops.clone(), 4, true),
                3 => self.begin_compile(self.q.hops.clone(), 5, true),
                4 => self.begin_compile(self.q.hops.clone(), 5, false),
                5 => {
                    self.hops = self.q.hops.clone();
                    self.after = 5;
                    if !(1..=8).contains(&self.hops.len()) {
                        self.result(0, 21);
                    } else {
                        self.emit(7, 20);
                    }
                }
                _ => unreachable!(),
            },
            2 => {
                if r.flag {
                    self.result(409, 12);
                } else {
                    self.emit(4, 3);
                }
            }
            3 => {
                if r.flag {
                    self.result(409, 12);
                } else {
                    self.after_update_running();
                }
            }
            5 => self.begin_compile(self.q.next.hops.clone(), 1, true),
            6 => self.begin_compile(self.q.hops.clone(), 2, true),
            10 => {
                if r.flag {
                    self.result(409, 16);
                } else {
                    self.emit(4, 11);
                }
            }
            11 => {
                if r.flag {
                    self.result(409, 16);
                } else {
                    self.begin_compile(self.q.hops.clone(), 3, true);
                }
            }
            20 => {
                if self.e.kind == 6 && !r.ok {
                    self.result(400, 28);
                } else if !(1..=8).contains(&self.hops.len()) {
                    self.result(400, 21);
                } else {
                    self.index = r.groups.into_iter().map(|g| (g.name, g.proxies)).collect();
                    self.at = 0;
                    self.seen.clear();
                    self.compiled.clear();
                    self.previous.clear();
                    self.next_hop();
                }
            }
            21 => {
                self.compiled.push(r.token);
                self.previous = self.e.data.clone();
                self.at += 1;
                self.next_hop();
            }
            22 => {
                if !r.flag || r.name != self.s.locked_region {
                    self.result(400, 27);
                } else {
                    self.emit(10, 23);
                    self.e.data = self.hops.last().unwrap().clone();
                }
            }
            23 => {
                if r.value > r.ttl {
                    self.result(400, 27);
                } else {
                    self.marshal();
                }
            }
            24 => {
                if !r.ok {
                    self.raw(&r);
                } else {
                    match self.after {
                        1 => self.emit(12, 30),
                        3 => {
                            self.emit(12, 31);
                            self.e.a = 1;
                        }
                        4 => {
                            self.result(0, 3);
                            self.e.indices = vec![r.token];
                        }
                        _ => unreachable!(),
                    }
                }
            }
            30 => self.emit(14, 32),
            31 => self.commit_profiles(),
            32 => {
                if !r.ok {
                    self.emit(15, 33);
                } else {
                    self.result(200, 0);
                }
            }
            33 => self.result(500, 30),
            40 => self.emit(14, 41),
            41 => {
                if !r.ok {
                    self.emit(15, 42);
                    self.e.a = 1;
                } else {
                    self.result(200, 1);
                }
            }
            42 => self.emit(15, 43),
            43 => self.result(500, 31),
            99 => self.emit(0, 100),
            _ => return Err("invalid chain stage"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn groups() -> Vec<Group> {
        vec![
            Group {
                name: b"a".to_vec(),
                proxies: vec![Proxy {
                    token: 1,
                    server: true,
                    proxy_type: true,
                    ..Proxy::default()
                }],
            },
            Group {
                name: b"b".to_vec(),
                proxies: vec![Proxy {
                    token: 2,
                    server: true,
                    proxy_type: true,
                    ..Proxy::default()
                }],
            },
        ]
    }
    fn chain() -> Chain {
        Chain {
            enabled: true,
            hops: vec![b"a".to_vec(), b"b".to_vec()],
            ..Chain::default()
        }
    }
    fn req(op: u8) -> Request {
        Request {
            op,
            next: chain(),
            name: b"saved".to_vec(),
            name_runes: 5,
            hops: chain().hops,
            ..Request::default()
        }
    }
    fn snapshot() -> Snapshot {
        Snapshot {
            subscription: true,
            ..Snapshot::default()
        }
    }
    fn run(q: Request, mut change: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(q).unwrap();
        let mut out = vec![];
        for _ in 0..100 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                token: 100 + e.a,
                value: 10,
                ttl: 10,
                groups: groups(),
                snapshot: if matches!(e.kind, 1 | 2) {
                    Some(snapshot())
                } else {
                    None
                },
                ..Reply::default()
            };
            change(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop")
    }
    #[test]
    fn update_checks_running_and_job_before_read() {
        let t = run(req(0), |e, r| {
            if e.kind == 3 {
                r.flag = true
            }
        });
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 3, 63]);
        assert_eq!(t.last().unwrap().b, 12);
        let t = run(req(0), |e, r| {
            if e.kind == 4 {
                r.flag = true
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 3, 4, 63]
        );
    }
    #[test]
    fn disabled_update_does_not_compile_but_saves() {
        let mut q = req(0);
        q.next.enabled = false;
        q.next.hops = vec![b"bogus".to_vec()];
        let t = run(q, |_, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 3, 4, 12, 14, 63]
        );
    }
    #[test]
    fn chain_prefix_differ_in_decode_error_priority() {
        for op in [3, 4] {
            let mut q = req(op);
            q.hops.clear();
            let t = run(q, |e, r| {
                if e.kind == 6 {
                    r.ok = false;
                    r.token = 77;
                }
            });
            assert_eq!(t.last().unwrap().b, if op == 3 { 20 } else { 28 });
            assert_eq!(t.iter().any(|e| e.kind == 6), op == 4);
        }
    }
    #[test]
    fn duplicate_and_strict_fields_error_priority() {
        let mut q = req(5);
        q.hops = vec![b"a".to_vec(), b"a".to_vec()];
        let t = run(q, |_, _| {});
        assert_eq!((t.last().unwrap().b, t.last().unwrap().c), (22, 2));
        for (dialer, server, typ, code) in [
            (true, false, false, 24),
            (false, false, true, 25),
            (false, true, false, 25),
        ] {
            let t = run(req(5), |e, r| {
                if e.kind == 7 {
                    r.groups[0].proxies[0].dialer = dialer;
                    r.groups[0].proxies[0].server = server;
                    r.groups[0].proxies[0].proxy_type = typ;
                }
            });
            assert_eq!((t.last().unwrap().b, t.last().unwrap().c), (code, 1));
        }
    }
    #[test]
    fn aliases_plan_preserves_first_dialer_and_links_later() {
        let t = run(req(3), |_, _| {});
        let c: Vec<_> = t.iter().filter(|e| e.kind == 8).collect();
        assert_eq!(c[0].data, b"SmartVPNChainHop1");
        assert_eq!(c[0].b, 0);
        assert_eq!(c[1].data, b"SmartVPNChain");
        assert_eq!(c[1].b, 1);
        assert_eq!(c[1].text, b"SmartVPNChainHop1");
        let mut q = req(4);
        q.hops.truncate(1);
        let t = run(q, |_, _| {});
        assert_eq!(
            t.iter().find(|e| e.kind == 8).unwrap().data,
            b"SmartVPNChain"
        );
    }
    #[test]
    fn alias_collision_in_unselected_index_and_ambiguous_name() {
        let t = run(req(5), |e, r| {
            if e.kind == 7 {
                r.groups.push(Group {
                    name: b"SmartVPNChainHop1".to_vec(),
                    proxies: vec![Proxy::default()],
                });
            }
        });
        assert_eq!(t.last().unwrap().b, 26);
        let t = run(req(5), |e, r| {
            if e.kind == 7 {
                r.groups[0].proxies.push(Proxy::default());
            }
        });
        assert_eq!(t.last().unwrap().b, 23);
    }
    #[test]
    fn build_disabled_does_not_even_snapshot() {
        let mut q = req(2);
        q.next.enabled = false;
        let t = run(q, |_, _| {});
        assert_eq!(t.len(), 1);
        assert_eq!(t[0].b, 4);
    }
    #[test]
    fn ttl_equal_allowed_only_verified_matched_reads_clock() {
        for (verified, country, age, clock, code) in [
            (false, b"US".as_slice(), 10, false, 27),
            (true, b"JP", 10, false, 27),
            (true, b"US", 11, true, 27),
            (true, b"US", 10, true, 3),
        ] {
            let t = run(req(2), |e, r| {
                if e.kind == 2 {
                    r.snapshot.as_mut().unwrap().locked_region = b"US".to_vec();
                }
                if e.kind == 9 {
                    r.flag = verified;
                    r.name = country.to_vec();
                }
                if e.kind == 10 {
                    r.value = age;
                }
            });
            assert_eq!(t.iter().any(|e| e.kind == 10), clock);
            assert_eq!(t.last().unwrap().b, code);
        }
    }
    #[test]
    fn save_inactive_compiles_without_running_or_region() {
        let t = run(req(1), |_, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 5, 6, 8, 8, 13, 14, 63]
        );
    }
    #[test]
    fn save_active_rename_compiles_twice_and_only_then_commits() {
        let mut q = req(1);
        q.original = b"old".to_vec();
        let t = run(q, |e, r| {
            if e.kind == 1 {
                let s = r.snapshot.as_mut().unwrap();
                s.current = Chain {
                    name: b"old".to_vec(),
                    ..chain()
                };
                s.saved.push(Saved {
                    name: b"old".to_vec(),
                    hops: vec![],
                    is_nil: false,
                });
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 5, 6, 8, 8, 3, 4, 6, 8, 8, 11, 12, 13, 14, 63]
        );
        assert_eq!(t.iter().find(|e| e.kind == 12).unwrap().a, 1);
    }
    #[test]
    fn old_nil_profile_means_missing_original_but_empty_slice_is_present() {
        for nil in [false, true] {
            let mut q = req(1);
            q.original = b"old".to_vec();
            let t = run(q, |e, r| {
                if e.kind == 1 {
                    r.snapshot.as_mut().unwrap().saved.push(Saved {
                        name: b"old".to_vec(),
                        is_nil: nil,
                        ..Saved::default()
                    });
                }
            });
            assert_eq!(t.last().unwrap().b, if nil { 14 } else { 1 });
        }
    }
    #[test]
    fn save_failures_restore_exact_scope() {
        for (op, scope, code) in [(0, 0, 30), (1, 1, 31)] {
            let t = run(req(op), |e, r| {
                if e.kind == 14 {
                    r.ok = false
                }
            });
            assert_eq!(t.iter().find(|e| e.kind == 15).unwrap().a, scope);
            assert_eq!(t.last().unwrap().b, code);
        }
    }
    #[test]
    fn name_rune_limit_original_is_not_trimmed_and_named_update_stale() {
        let mut q = req(1);
        q.name = vec![255; 80];
        q.name_runes = 80;
        let t = run(q.clone(), |_, _| {});
        assert_eq!(t.last().unwrap().b, 1);
        q.name_runes = 81;
        assert_eq!(Flow::new(q).unwrap().next().b, 11);
        let mut q = req(1);
        q.original = b" old ".to_vec();
        let t = run(q, |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().saved.push(Saved {
                    name: b"old".to_vec(),
                    is_nil: false,
                    ..Saved::default()
                });
            }
        });
        assert_eq!(t.last().unwrap().b, 14);
        let mut q = req(0);
        q.next.name = b"missing".to_vec();
        let t = run(q, |_, _| {});
        assert_eq!(t.last().unwrap().b, 13);
    }
    #[test]
    fn subscription_absence_uses_nil_profile_after_read() {
        let t = run(req(0), |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().subscription = false
            }
        });
        assert!(t.iter().any(|e| e.kind == 5));
        assert_eq!(t.iter().find(|e| e.kind == 6).unwrap().a, 1);
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = Flow::new(req(0)).unwrap();
        let before = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), before);
        assert!(f
            .advance(Reply {
                kind: 2,
                snapshot: Some(snapshot()),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), before);
    }
}
