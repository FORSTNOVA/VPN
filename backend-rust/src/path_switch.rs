//! Same-region switch and legacy path-round state machines. No actual effects run here.
use crate::path_flow::{Effect, Reply, Request, Row, Snapshot, Target};
use std::collections::{BTreeSet, VecDeque};
fn effect(kind: u8) -> Effect {
    Effect {
        kind,
        ..Effect::default()
    }
}
fn set(name: &[u8], text: Vec<u8>, flag: bool) -> Effect {
    Effect {
        kind: 9,
        name: name.to_vec(),
        text,
        a: flag as i64,
        ..Effect::default()
    }
}
fn message(code: i64, args: Vec<Vec<u8>>) -> Effect {
    Effect {
        kind: 9,
        name: b"note".to_vec(),
        b: code + 1,
        strings: args,
        ..Effect::default()
    }
}
fn get(name: Vec<u8>) -> Effect {
    Effect {
        kind: 4,
        name,
        ..Effect::default()
    }
}
fn named(kind: u8, name: Vec<u8>) -> Effect {
    Effect {
        kind,
        name,
        ..Effect::default()
    }
}
fn terminal(code: i64, token: i64, text: Vec<u8>) -> Effect {
    Effect {
        kind: 63,
        a: code,
        b: token,
        text,
        ..Effect::default()
    }
}
fn auto(n: &[u8]) -> bool {
    matches!(n, b"SmartVPNAuto" | b"SmartVPNFallback")
}
#[derive(Clone, Debug)]
pub struct SwitchFlow {
    q: Request,
    e: Effect,
    pc: u16,
    active: Vec<u8>,
    from: Vec<u8>,
    code: i64,
    token: i64,
    error: Vec<u8>,
    evidence_code: i64,
}
impl SwitchFlow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        Ok(Self {
            q,
            e: effect(1),
            pc: 1,
            active: vec![],
            from: vec![],
            code: 0,
            token: 0,
            error: vec![],
            evidence_code: 0,
        })
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    pub fn set_evidence_code(&mut self, code: i64) {
        self.evidence_code = code;
    }
    fn emit(&mut self, e: Effect, pc: u16) {
        self.e = e;
        self.pc = pc
    }
    fn finish(&mut self, code: i64) {
        self.code = code;
        self.emit(effect(2), 90)
    }
    fn active(&mut self, second: bool) {
        self.emit(get(b"SmartVPN".to_vec()), if second { 20 } else { 10 })
    }
    fn got_active(&mut self, second: bool) {
        if second {
            self.from = self.active.clone();
            self.emit(named(12, self.q.target.clone()), 30)
        } else if self.active == self.q.target {
            self.finish(0)
        } else {
            self.emit(named(5, self.q.target.clone()), 15)
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected path-switch reply");
        }
        if self.e.kind == 1 && r.snapshot.is_none() {
            return Err("missing locked path snapshot");
        }
        match self.pc {
            1 => self.emit(effect(3), 2),
            2 => {
                if r.flag {
                    self.active(false)
                } else {
                    self.finish(2)
                }
            }
            10 | 20 => {
                let second = self.pc == 20;
                self.active = if r.ok { r.name } else { vec![] };
                if auto(&self.active) {
                    self.emit(get(self.active.clone()), if second { 21 } else { 11 })
                } else {
                    self.got_active(second)
                }
            }
            11 | 21 => {
                let second = self.pc == 21;
                if r.ok {
                    self.active = r.name
                }
                self.got_active(second)
            }
            15 => {
                if !r.found || !r.verified || r.name != self.q.region {
                    self.finish(3)
                } else {
                    self.emit(named(13, self.q.target.clone()), 16)
                }
            }
            16 => {
                if r.flag {
                    self.finish(3)
                } else {
                    self.active(true)
                }
            }
            30 => {
                if !r.ok {
                    self.token = r.token;
                    self.error = r.error;
                    self.finish(1)
                } else {
                    self.emit(set(b"pin", self.q.target.clone(), false), 31)
                }
            }
            31 => self.emit(effect(10), 32),
            32 => self.emit(effect(6), 33),
            33 => self.emit(set(b"last-switch", vec![], false), 34),
            34 => self.emit(set(b"append-switch", self.q.target.clone(), false), 35),
            35 => {
                let mut e = effect(11);
                e.b = self.evidence_code;
                e.strings = vec![
                    self.from.clone(),
                    self.q.target.clone(),
                    self.q.trigger.clone(),
                    self.q.evidence.clone(),
                ];
                self.emit(e, 36)
            }
            36 => self.finish(0),
            90 => self.emit(terminal(self.code, self.token, self.error.clone()), 91),
            91 => self.emit(effect(0), 92),
            _ => return Err("invalid switch state"),
        }
        Ok(())
    }
}
#[derive(Clone, Debug)]
pub struct LegacyFlow {
    e: Effect,
    pc: u16,
    s: Snapshot,
    queue: VecDeque<Effect>,
    after: u16,
    active: Vec<u8>,
    active_after: u16,
    record_after: u16,
    check_after: u16,
    targets: Vec<Target>,
    rows: Vec<Row>,
    check_index: usize,
    busy: bool,
    region: Vec<u8>,
    target: Vec<u8>,
    from: Vec<u8>,
    tried: BTreeSet<Vec<u8>>,
    failures: Vec<Vec<u8>>,
}
impl LegacyFlow {
    pub fn new(_q: Request) -> Result<Self, &'static str> {
        Ok(Self {
            e: effect(1),
            pc: 1,
            s: Snapshot::default(),
            queue: VecDeque::new(),
            after: 0,
            active: vec![],
            active_after: 0,
            record_after: 0,
            check_after: 0,
            targets: vec![],
            rows: vec![],
            check_index: 0,
            busy: false,
            region: vec![],
            target: vec![],
            from: vec![],
            tried: BTreeSet::new(),
            failures: vec![],
        })
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, e: Effect, pc: u16) {
        self.e = e;
        self.pc = pc
    }
    fn seq(&mut self, es: Vec<Effect>, after: u16) {
        self.queue = es.into();
        self.after = after;
        self.seq_next()
    }
    fn seq_next(&mut self) {
        if let Some(e) = self.queue.pop_front() {
            self.emit(e, 900)
        } else {
            let n = self.after;
            self.step(n)
        }
    }
    fn active(&mut self, after: u16) {
        self.active_after = after;
        self.emit(get(b"SmartVPN".to_vec()), 800)
    }
    fn record(&mut self, after: u16) {
        self.record_after = after;
        self.failures = self
            .rows
            .iter()
            .filter(|r| !r.passed)
            .map(|r| r.name.clone())
            .collect();
        let mut results = set(b"results", vec![], false);
        results.indices = self.rows.iter().map(|r| r.token).collect();
        let mut fails = set(b"failures", vec![], false);
        fails.strings = self.failures.clone();
        self.seq(
            vec![effect(6), set(b"at", vec![], false), results, fails],
            810,
        )
    }
    fn check(&mut self, after: u16) {
        self.check_after = after;
        self.rows.clear();
        self.check_index = 0;
        self.check_next()
    }
    fn check_next(&mut self) {
        if self.check_index < self.targets.len() {
            let mut e = effect(8);
            e.a = self.targets[self.check_index].token;
            self.emit(e, 820)
        } else {
            let n = self.check_after;
            self.step(n)
        }
    }
    fn done(&mut self) {
        if self.busy {
            self.emit(effect(1), 990)
        } else {
            self.emit(terminal(0, 0, vec![]), 999)
        }
    }
    fn leave(&mut self) {
        self.emit(effect(2), 989)
    }
    fn step(&mut self, n: u16) {
        match n {
            10 => {
                if self.active.is_empty() || self.active.eq_ignore_ascii_case(b"REJECT") {
                    self.leave()
                } else {
                    self.targets = self
                        .s
                        .targets
                        .iter()
                        .filter(|t| t.enabled)
                        .cloned()
                        .collect();
                    if self.targets.is_empty() {
                        self.leave()
                    } else {
                        self.busy = true;
                        self.seq(vec![set(b"busy", vec![], true), effect(14), effect(2)], 11)
                    }
                }
            }
            11 => self.check(12),
            12 => self.emit(effect(1), 13),
            14 => self.seq(
                vec![set(b"note", vec![], false), set(b"switched", vec![], false)],
                15,
            ),
            15 => {
                if self.failures.is_empty() {
                    self.leave()
                } else if self.s.chain {
                    self.seq(vec![message(4, vec![])], 989)
                } else if !self.s.locked_region.is_empty() {
                    self.region = self.s.locked_region.clone();
                    self.emit(effect(2), 18)
                } else {
                    self.active(16)
                }
            }
            16 => self.emit(named(5, self.active.clone()), 17),
            18 => {
                if self.region.is_empty() {
                    self.emit(effect(1), 19)
                } else {
                    self.emit(effect(1), 30)
                }
            }
            20 => self.leave(),
            32 => self.emit(effect(7), 33),
            34 => {
                self.from = self.active.clone();
                self.emit(named(12, self.target.clone()), 35)
            }
            40 => self.check(41),
            41 => self.emit(effect(1), 42),
            43 => self.active(44),
            44 => self.seq(
                vec![set(b"active", self.active.clone(), false), effect(2)],
                45,
            ),
            45 => {
                if self.failures.is_empty() {
                    self.done()
                } else {
                    self.emit(effect(1), 30)
                }
            }
            810 => self.active(811),
            811 => {
                let after = self.record_after;
                self.seq(
                    vec![
                        set(b"active", self.active.clone(), false),
                        set(b"running", vec![], self.s.busy),
                    ],
                    after,
                )
            }
            989 => self.leave(),
            991 => self.emit(terminal(0, 0, vec![]), 999),
            _ => panic!("invalid legacy continuation {n}"),
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected legacy path reply");
        }
        if self.e.kind == 1 && r.snapshot.is_none() {
            return Err("missing locked path snapshot");
        }
        if let Some(s) = r.snapshot {
            self.s = s
        }
        match self.pc {
            1 => self.emit(effect(3), 2),
            2 => {
                if !r.flag || self.s.busy {
                    self.leave()
                } else {
                    self.active(10)
                }
            }
            13 => self.record(14),
            17 => {
                self.region = if r.found { r.name } else { vec![] };
                self.emit(effect(2), 18)
            }
            18 => self.step(18),
            19 => self.seq(vec![message(5, vec![])], 20),
            30 => self.emit(effect(3), 31),
            31 => {
                if !r.flag {
                    self.leave()
                } else {
                    self.active(32)
                }
            }
            33 => {
                let mut names: Vec<_> = r
                    .candidates
                    .into_iter()
                    .filter(|c| {
                        c.verified
                            && c.country == self.region
                            && c.age <= r.ttl
                            && !c.cooldown
                            && c.name != self.active
                            && !self.tried.contains(&c.name)
                    })
                    .map(|c| c.name)
                    .collect();
                names.sort();
                if let Some(n) = names.first().filter(|n| !n.is_empty()) {
                    self.target = n.clone();
                    self.active(34)
                } else {
                    let mut args = vec![self.region.clone()];
                    args.extend(self.failures.clone());
                    self.seq(vec![message(6, args)], 989)
                }
            }
            35 => {
                if !r.ok {
                    let mut e = message(8, vec![]);
                    e.text = r.token.to_string().into_bytes();
                    self.seq(vec![e], 989)
                } else {
                    self.tried.insert(self.target.clone());
                    let mut ev = effect(11);
                    ev.b = 10;
                    ev.strings = vec![
                        self.from.clone(),
                        self.target.clone(),
                        b"path_check".to_vec(),
                    ];
                    ev.strings.extend(self.failures.clone());
                    self.seq(
                        vec![
                            set(b"pin", self.target.clone(), false),
                            effect(10),
                            effect(6),
                            set(b"last-switch", vec![], false),
                            set(b"append-switch", self.target.clone(), false),
                            ev,
                            effect(2),
                        ],
                        40,
                    )
                }
            }
            42 => self.record(43),
            800 => {
                self.active = if r.ok { r.name } else { vec![] };
                if auto(&self.active) {
                    self.emit(get(self.active.clone()), 801)
                } else {
                    let n = self.active_after;
                    self.step(n)
                }
            }
            801 => {
                if r.ok {
                    self.active = r.name
                }
                let n = self.active_after;
                self.step(n)
            }
            820 => {
                self.rows.push(r.row);
                self.check_index += 1;
                self.check_next()
            }
            900 => self.seq_next(),
            989 => self.done(),
            990 => self.seq(vec![set(b"busy", vec![], false), effect(2)], 991),
            999 => self.emit(effect(0), 1000),
            _ => return Err("invalid legacy state"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::path_flow::{Candidate, Target};
    fn q() -> Request {
        Request {
            target: b"new".to_vec(),
            region: b"XX".to_vec(),
            trigger: b"access-path".to_vec(),
            evidence: b"evidence".to_vec(),
            ..Request::default()
        }
    }
    fn switch_run(mut alter: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = SwitchFlow::new(q()).unwrap();
        let mut out = vec![];
        for _ in 0..80 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: e.kind != 13,
                found: true,
                verified: true,
                name: if e.kind == 5 {
                    b"XX".to_vec()
                } else {
                    b"old".to_vec()
                },
                snapshot: if e.kind == 1 {
                    Some(Snapshot::default())
                } else {
                    None
                },
                ..Reply::default()
            };
            alter(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap()
        }
        panic!("loop")
    }
    fn kinds(t: &[Effect]) -> Vec<u8> {
        t.iter().map(|e| e.kind).collect()
    }
    #[test]
    fn switch_order_save_error_ignored() {
        let t = switch_run(|e, r| {
            if e.kind == 10 {
                r.ok = false
            }
        });
        assert_eq!(
            kinds(&t),
            [1, 3, 4, 5, 13, 4, 12, 9, 10, 6, 9, 9, 11, 2, 63]
        );
        assert_eq!(t.last().unwrap().a, 0);
        assert_eq!(
            t.iter().find(|e| e.kind == 11).unwrap().strings,
            [
                b"old".to_vec(),
                b"new".to_vec(),
                b"access-path".to_vec(),
                b"evidence".to_vec()
            ]
        );
    }
    #[test]
    fn same_active_bypasses_region_and_time() {
        let t = switch_run(|e, r| {
            if e.kind == 4 {
                r.name = b"new".to_vec()
            }
        });
        assert_eq!(kinds(&t), [1, 3, 4, 2, 63]);
    }
    #[test]
    fn disconnected_does_not_get_or_put() {
        let t = switch_run(|e, r| {
            if e.kind == 3 {
                r.flag = false
            }
        });
        assert_eq!(kinds(&t), [1, 3, 2, 63]);
        assert_eq!(t.last().unwrap().a, 2);
    }
    #[test]
    fn region_checks_before_age() {
        for which in 0..3 {
            let t = switch_run(|e, r| {
                if e.kind == 5 {
                    match which {
                        0 => r.found = false,
                        1 => r.verified = false,
                        _ => r.name = b"YY".to_vec(),
                    }
                }
            });
            assert_eq!(kinds(&t), [1, 3, 4, 5, 2, 63]);
            assert_eq!(t.last().unwrap().a, 3)
        }
        let t = switch_run(|e, r| {
            if e.kind == 13 {
                r.flag = true
            }
        });
        assert!(!kinds(&t).contains(&12));
        assert_eq!(t.last().unwrap().a, 3);
    }
    #[test]
    fn put_failure_has_no_commit() {
        let t = switch_run(|e, r| {
            if e.kind == 12 {
                r.ok = false;
                r.token = 77;
                r.error = b"failure".to_vec()
            }
        });
        assert!(!kinds(&t).contains(&9));
        assert_eq!((t.last().unwrap().a, t.last().unwrap().b), (1, 77));
    }
    #[test]
    fn nested_active_get_failure_retains_group() {
        let t = switch_run(|e, r| {
            if e.kind == 4 {
                if e.name == b"SmartVPN" {
                    r.name = b"SmartVPNAuto".to_vec()
                } else {
                    r.ok = false
                }
            }
        });
        let ev = t.iter().find(|e| e.kind == 11).unwrap();
        assert_eq!(ev.strings[0], b"SmartVPNAuto");
    }
    fn legacy_run(mut alter: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = LegacyFlow::new(Request::default()).unwrap();
        let mut out = vec![];
        let mut busy = false;
        let mut active = b"old".to_vec();
        for token in 1..=300 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            if e.kind == 9 && e.name == b"busy" {
                busy = e.a != 0
            }
            if e.kind == 12 {
                active = e.name.clone()
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: true,
                found: true,
                verified: true,
                name: if e.kind == 5 {
                    b"XX".to_vec()
                } else {
                    active.clone()
                },
                ttl: 100,
                row: Row {
                    token,
                    name: b"target".to_vec(),
                    passed: true,
                },
                candidates: vec![Candidate {
                    name: b"new".to_vec(),
                    country: b"XX".to_vec(),
                    verified: true,
                    ..Candidate::default()
                }],
                snapshot: if e.kind == 1 {
                    Some(Snapshot {
                        busy,
                        enabled: true,
                        locked_region: b"XX".to_vec(),
                        targets: vec![Target {
                            token: 1,
                            name: b"target".to_vec(),
                            enabled: true,
                            ..Target::default()
                        }],
                        ..Snapshot::default()
                    })
                } else {
                    None
                },
                ..Reply::default()
            };
            alter(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap()
        }
        panic!("loop")
    }
    #[test]
    fn legacy_success_records_and_releases_busy() {
        let t = legacy_run(|_, _| {});
        assert_eq!(
            kinds(&t),
            [1, 3, 4, 9, 14, 2, 8, 1, 6, 9, 9, 9, 4, 9, 9, 9, 9, 2, 1, 9, 2, 63]
        );
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 9 && e.name == b"busy")
                .map(|e| e.a)
                .collect::<Vec<_>>(),
            [1, 0]
        );
    }
    #[test]
    fn legacy_chain_failed_no_candidates() {
        let t = legacy_run(|e, r| {
            if e.kind == 8 {
                r.row.passed = false
            }
            if let Some(s) = &mut r.snapshot {
                s.chain = true
            }
        });
        assert!(!kinds(&t).contains(&7));
        assert!(t.iter().any(|e| e.name == b"note" && e.b == 5));
    }
    #[test]
    fn legacy_switch_then_success() {
        let mut checks = 0;
        let t = legacy_run(|e, r| {
            if e.kind == 8 {
                checks += 1;
                r.row.passed = checks > 1
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 12).count(), 1);
        assert_eq!(t.iter().filter(|e| e.kind == 7).count(), 1);
        let event = t.iter().find(|e| e.kind == 11).unwrap();
        assert_eq!(event.b, 10);
        assert_eq!(event.strings[2], b"path_check");
        assert_eq!(event.strings[3], b"target");
    }
    #[test]
    fn legacy_candidates_sorted_filtered_and_refreshed() {
        let mut checks = 0;
        let t = legacy_run(|e, r| {
            if e.kind == 8 {
                checks += 1;
                r.row.passed = checks > 2
            }
            if e.kind == 7 {
                r.candidates = vec![
                    Candidate {
                        name: b"z".to_vec(),
                        country: b"XX".to_vec(),
                        verified: true,
                        age: 100,
                        ..Candidate::default()
                    },
                    Candidate {
                        name: b"a".to_vec(),
                        country: b"XX".to_vec(),
                        verified: true,
                        ..Candidate::default()
                    },
                    Candidate {
                        name: b"0bad".to_vec(),
                        country: b"XX".to_vec(),
                        verified: true,
                        age: 101,
                        ..Candidate::default()
                    },
                ]
            }
        });
        let names: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 12)
            .map(|e| e.name.clone())
            .collect();
        assert_eq!(names, [b"a".to_vec(), b"z".to_vec()]);
        assert_eq!(t.iter().filter(|e| e.kind == 7).count(), 2);
    }
    #[test]
    fn legacy_failed_put_stops_without_marking_switch() {
        let t = legacy_run(|e, r| {
            if e.kind == 8 {
                r.row.passed = false
            }
            if e.kind == 12 {
                r.ok = false;
                r.token = 11
            }
        });
        assert!(!kinds(&t).contains(&11));
        assert!(t
            .iter()
            .any(|e| e.name == b"note" && e.b == 9 && e.text == b"11"));
    }
    #[test]
    fn legacy_empty_region_note_and_no_candidates() {
        let t = legacy_run(|e, r| {
            if e.kind == 8 {
                r.row.passed = false
            }
            if let Some(s) = &mut r.snapshot {
                s.locked_region.clear()
            }
            if e.kind == 5 {
                r.found = false
            }
        });
        assert!(!kinds(&t).contains(&7));
        assert!(t.iter().any(|e| e.name == b"note" && e.b == 6));
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = SwitchFlow::new(q()).unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), e);
        let mut f = LegacyFlow::new(q()).unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 2,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), e);
    }
    #[test]
    fn legacy_empty_first_candidate_is_exhausted_not_skipped() {
        let t = legacy_run(|e, r| {
            if e.kind == 8 {
                r.row.passed = false;
            }
            if e.kind == 7 {
                r.candidates.push(Candidate {
                    name: vec![],
                    country: b"XX".to_vec(),
                    verified: true,
                    ..Candidate::default()
                });
            }
        });
        assert!(!kinds(&t).contains(&12));
        assert!(t.iter().any(|e| e.name == b"note" && e.b == 7));
    }
    #[test]
    fn switch_evidence_code_is_transmitted_without_rewriting() {
        let mut f = SwitchFlow::new(q()).unwrap();
        f.set_evidence_code(13);
        for _ in 0..40 {
            let e = f.next();
            if e.kind == 11 {
                assert_eq!(e.b, 13);
                return;
            }
            let r = Reply {
                kind: e.kind,
                ok: true,
                flag: e.kind != 13,
                found: true,
                verified: true,
                name: if e.kind == 5 {
                    b"XX".to_vec()
                } else {
                    b"old".to_vec()
                },
                snapshot: if e.kind == 1 {
                    Some(Snapshot::default())
                } else {
                    None
                },
                ..Reply::default()
            };
            f.advance(r).unwrap();
        }
        panic!("no event");
    }
}
