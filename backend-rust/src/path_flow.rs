//! Single-round path decisions. Real probes, clocks and mutable application state remain in Go.
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub mode: Vec<u8>,
    pub target: Vec<u8>,
    pub region: Vec<u8>,
    pub trigger: Vec<u8>,
    pub evidence: Vec<u8>,
}
#[derive(Clone, Debug, Default)]
pub struct Target {
    pub token: i64,
    pub name: Vec<u8>,
    pub enabled: bool,
    pub baseline: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Row {
    pub token: i64,
    pub name: Vec<u8>,
    pub passed: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Candidate {
    pub name: Vec<u8>,
    pub country: Vec<u8>,
    pub verified: bool,
    pub cooldown: bool,
    pub age: i64,
}
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub busy: bool,
    pub enabled: bool,
    pub chain: bool,
    pub demanded: bool,
    pub locked_region: Vec<u8>,
    pub retry_mode: Vec<u8>,
    pub retry_at: Vec<u8>,
    pub alert_id: u64,
    pub targets: Vec<Target>,
    pub baseline: Vec<Target>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub found: bool,
    pub verified: bool,
    pub name: Vec<u8>,
    pub error: Vec<u8>,
    pub token: i64,
    pub ttl: i64,
    pub row: Row,
    pub candidates: Vec<Candidate>,
    pub snapshot: Option<Snapshot>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub text: Vec<u8>,
    pub name: Vec<u8>,
    pub indices: Vec<i64>,
    pub strings: Vec<Vec<u8>>,
}
use std::collections::VecDeque;
#[derive(Clone, Debug)]
pub enum Flow {
    Main(Box<MainFlow>),
    Switch(Box<crate::path_switch::SwitchFlow>),
    Legacy(Box<crate::path_switch::LegacyFlow>),
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        match q.op {
            0 | 1 => Ok(Self::Main(Box::new(MainFlow::new(q)))),
            2 => Ok(Self::Legacy(Box::new(crate::path_switch::LegacyFlow::new(
                q,
            )?))),
            3 => Ok(Self::Switch(Box::new(crate::path_switch::SwitchFlow::new(
                q,
            )?))),
            _ => Err("invalid path operation"),
        }
    }
    pub fn next(&self) -> Effect {
        match self {
            Self::Main(f) => f.next(),
            Self::Switch(f) => f.next(),
            Self::Legacy(f) => f.next(),
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        match self {
            Self::Main(f) => f.advance(r),
            Self::Switch(f) => f.advance(r),
            Self::Legacy(f) => f.advance(r),
        }
    }
}
fn effect(kind: u8) -> Effect {
    Effect {
        kind,
        ..Effect::default()
    }
}
fn patch(name: &[u8], text: &[u8], flag: bool) -> Effect {
    Effect {
        kind: 9,
        name: name.to_vec(),
        text: text.to_vec(),
        a: flag as i64,
        ..Effect::default()
    }
}
fn message(name: &[u8], code: i64) -> Effect {
    Effect {
        kind: 9,
        name: name.to_vec(),
        b: code + 1,
        ..Effect::default()
    }
}
fn clear_retry() -> Vec<Effect> {
    vec![
        patch(b"retry-at", b"", false),
        patch(b"retry-mode", b"", false),
    ]
}
fn finish_recovery() -> Vec<Effect> {
    let mut v = vec![
        patch(b"note", b"", false),
        patch(b"alert", b"", false),
        patch(b"demanded", b"", false),
        patch(b"phase", b"idle", false),
    ];
    v.extend(clear_retry());
    v
}
fn schedule(mode: &[u8]) -> Vec<Effect> {
    let mut at = patch(b"retry-at", b"", false);
    at.b = -1;
    vec![
        patch(b"retry-mode", mode, false),
        effect(6),
        at,
        patch(b"phase", b"waiting", false),
    ]
}
fn passed(rows: &[Row]) -> bool {
    rows.iter().all(|r| r.passed)
}
#[derive(Clone, Debug)]
pub struct MainFlow {
    q: Request,
    s: Snapshot,
    e: Effect,
    pc: u16,
    queue: VecDeque<Effect>,
    after: u16,
    locked: bool,
    owned: bool,
    get_after: u16,
    active: Vec<u8>,
    check_after: u16,
    check_targets: Vec<Target>,
    check_i: usize,
    rows: Vec<Row>,
    record_rows: Vec<Row>,
    record_after: u16,
    original: Vec<u8>,
    region: Vec<u8>,
    demanded: bool,
    baseline: Vec<Target>,
    extras: Vec<Target>,
    base: Vec<Row>,
    original_base: Option<Vec<Row>>,
    last: Vec<Row>,
    ordered: Vec<Vec<u8>>,
    candidate_i: usize,
    child: Option<Box<crate::path_switch::SwitchFlow>>,
    child_after: u16,
}
impl MainFlow {
    fn new(q: Request) -> Self {
        Self {
            q,
            s: Snapshot::default(),
            e: effect(1),
            pc: 1,
            queue: VecDeque::new(),
            after: 0,
            locked: false,
            owned: false,
            get_after: 0,
            active: vec![],
            check_after: 0,
            check_targets: vec![],
            check_i: 0,
            rows: vec![],
            record_rows: vec![],
            record_after: 0,
            original: vec![],
            region: vec![],
            demanded: false,
            baseline: vec![],
            extras: vec![],
            base: vec![],
            original_base: None,
            last: vec![],
            ordered: vec![],
            candidate_i: 0,
            child: None,
            child_after: 0,
        }
    }
    fn next(&self) -> Effect {
        if let Some(c) = &self.child {
            c.next()
        } else {
            self.e.clone()
        }
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = effect(k);
        self.pc = pc
    }
    fn seq(&mut self, v: Vec<Effect>, after: u16) {
        self.queue = v.into();
        self.after = after;
        self.seq_next()
    }
    fn seq_next(&mut self) {
        if let Some(e) = self.queue.pop_front() {
            self.e = e;
            self.pc = 9000
        } else {
            let after = self.after;
            self.go(after, Reply::default())
        }
    }
    fn active(&mut self, after: u16) {
        self.get_after = after;
        self.emit(4, 9001);
        self.e.name = b"SmartVPN".to_vec()
    }
    fn region(&mut self, after: u16) {
        if !self.s.locked_region.is_empty() {
            self.region = self.s.locked_region.clone();
            self.go(after, Reply::default())
        } else {
            self.after = after;
            self.active(9010)
        }
    }
    fn check(&mut self, targets: Vec<Target>, after: u16) {
        self.check_targets = targets;
        self.check_after = after;
        self.check_i = 0;
        self.rows.clear();
        self.check_next()
    }
    fn check_next(&mut self) {
        if self.check_i < self.check_targets.len() {
            self.emit(8, 9020);
            self.e.a = self.check_targets[self.check_i].token
        } else {
            let after = self.check_after;
            self.go(after, Reply::default())
        }
    }
    fn record(&mut self, rows: Vec<Row>, after: u16) {
        self.record_rows = rows;
        self.record_after = after;
        self.emit(6, 9030)
    }
    fn cleanup(&mut self) {
        let mut v = vec![];
        if self.locked {
            v.push(effect(2))
        }
        if self.owned {
            v.push(effect(1));
            v.push(patch(b"busy", b"", false));
            if self.q.op == 1 {
                v.push(patch(b"running", b"", false))
            }
            v.push(effect(2));
        }
        self.seq(v, 9998)
    }
    fn queue_retry(&self, mode: &[u8], msg: i64) -> Vec<Effect> {
        let mut v = vec![];
        if self.s.alert_id == 0 || self.s.retry_at.is_empty() {
            v.push(patch(b"alert-increment", b"", false))
        }
        v.push(message(b"alert", msg));
        v.extend(schedule(mode));
        v
    }
    fn switch(&mut self, target: Vec<u8>, restore: bool, after: u16) {
        self.child_after = after;
        let q = Request {
            op: 3,
            target,
            region: self.region.clone(),
            trigger: if restore {
                b"access-path-restore".to_vec()
            } else {
                b"access-path".to_vec()
            },
            evidence: vec![],
            ..Request::default()
        };
        let mut c = crate::path_switch::SwitchFlow::new(q).expect("known switch operation");
        c.set_evidence_code(if restore { 15 } else { 13 });
        self.child = Some(Box::new(c));
    }
    fn candidate(&mut self) {
        if self.candidate_i >= self.ordered.len() {
            self.emit(1, 160);
            return;
        }
        let target = self.ordered[self.candidate_i].clone();
        if target == self.original {
            self.check(self.baseline.clone(), 130)
        } else {
            self.switch(target, false, 129)
        }
    }
    fn patch_state(&mut self, e: &Effect) {
        if e.kind != 9 {
            return;
        }
        match e.name.as_slice() {
            b"busy" => {
                self.s.busy = e.a != 0;
                self.owned = e.a != 0
            }
            b"demanded" => self.s.demanded = e.a != 0,
            b"retry-mode" => self.s.retry_mode = e.text.clone(),
            b"retry-at" => {
                self.s.retry_at = if e.b == -1 {
                    b"scheduled".to_vec()
                } else {
                    e.text.clone()
                }
            }
            b"alert-increment" => self.s.alert_id = self.s.alert_id.wrapping_add(1),
            _ => {}
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if let Some(c) = &mut self.child {
            c.advance(r)?;
            if c.next().kind == 63 {
                let result = c.next();
                self.child = None;
                let after = self.child_after;
                let success = result.a == 0;
                self.go(
                    after,
                    Reply {
                        ok: success,
                        ..Reply::default()
                    },
                )
            }
            return Ok(());
        }
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected path reply");
        }
        if r.kind == 1 && r.snapshot.is_none() {
            return Err("missing locked path snapshot");
        }
        if let Some(s) = &r.snapshot {
            if s.targets.len() > 100000 || s.baseline.len() > 100000 {
                return Err("too many path targets");
            }
        }
        if r.candidates.len() > 100000 {
            return Err("too many candidates");
        }
        if r.kind == 1 {
            self.s = r.snapshot.clone().unwrap();
            self.locked = true
        }
        if r.kind == 2 {
            self.locked = false
        }
        let e = self.e.clone();
        self.patch_state(&e);
        let pc = self.pc;
        self.go(pc, r);
        Ok(())
    }
    fn go(&mut self, pc: u16, r: Reply) {
        match pc {
            9000 => self.seq_next(),
            9001 => {
                self.active = if r.ok { r.name } else { vec![] };
                if self.active == b"SmartVPNAuto" || self.active == b"SmartVPNFallback" {
                    self.emit(4, 9002);
                    self.e.name = self.active.clone()
                } else {
                    let after = self.get_after;
                    self.go(after, Reply::default())
                }
            }
            9002 => {
                if r.ok {
                    self.active = r.name
                }
                let after = self.get_after;
                self.go(after, Reply::default())
            }
            9010 => {
                self.emit(5, 9011);
                self.e.name = self.active.clone()
            }
            9011 => {
                self.region = if r.found { r.name } else { vec![] };
                let after = self.after;
                self.go(after, Reply::default())
            }
            9020 => {
                self.rows.push(r.row);
                self.check_i += 1;
                self.check_next()
            }
            9030 => {
                let mut results = patch(b"results", b"", false);
                results.indices = self.record_rows.iter().map(|r| r.token).collect();
                let mut failures = patch(b"failures", b"", false);
                failures.strings = self
                    .record_rows
                    .iter()
                    .filter(|r| !r.passed)
                    .map(|r| r.name.clone())
                    .collect();
                self.seq(vec![patch(b"at", b"", false), results, failures], 9031)
            }
            9031 => self.active(9032),
            9032 => self.seq(
                vec![
                    patch(b"active", &self.active, false),
                    patch(b"running", b"", self.s.busy),
                ],
                self.record_after,
            ),
            9998 => self.emit(63, 9999),
            9999 => self.emit(0, 9999),
            1 => self.emit(3, 2),
            2 => {
                if !r.flag || self.s.busy || (self.q.op == 0 && !self.s.enabled) {
                    self.cleanup()
                } else if self.q.op == 0 {
                    self.baseline = self.s.baseline.clone();
                    self.seq(
                        vec![
                            patch(b"busy", b"", true),
                            patch(b"phase", b"baseline", false),
                            effect(14),
                            effect(2),
                        ],
                        10,
                    )
                } else {
                    self.active(100)
                }
            }
            10 => self.check(self.baseline.clone(), 11),
            11 => self.emit(1, 12),
            12 => self.record(self.rows.clone(), 13),
            13 => {
                let mut v = vec![];
                if passed(&self.rows) {
                    v.push(patch(b"phase", b"baseline", false));
                    v.push(message(b"note", 0));
                    if self.s.retry_mode == b"scan" {
                        v.extend(clear_retry());
                        v.push(patch(b"alert", b"", false));
                        if self.s.demanded
                            && self.s.targets.iter().any(|t| t.enabled && !t.baseline)
                        {
                            v.extend(schedule(b"targets"))
                        }
                    }
                } else {
                    v.push(patch(b"phase", b"waiting", false));
                    if self.s.chain {
                        v.extend(clear_retry());
                        v.push(message(b"note", 1))
                    } else {
                        v.push(message(b"note", 2));
                        if self.s.retry_at.is_empty() {
                            v.extend(self.queue_retry(b"scan", 3))
                        }
                    }
                }
                self.seq(v, 9900)
            }
            9900 => self.cleanup(),
            100 => {
                if self.active.is_empty() || self.active.eq_ignore_ascii_case(b"REJECT") {
                    self.cleanup()
                } else {
                    self.original = self.active.clone();
                    self.region(101)
                }
            }
            101 => {
                self.baseline = self.s.baseline.clone();
                self.extras = self
                    .s
                    .targets
                    .iter()
                    .filter(|t| t.enabled && !t.baseline)
                    .cloned()
                    .collect();
                let mut v = vec![];
                self.demanded = self.s.demanded;
                if self.q.mode == b"demand" || self.q.mode == b"manual" {
                    v.push(patch(b"demanded", b"", true));
                    self.demanded = true
                }
                v.extend([
                    effect(14),
                    patch(b"busy", b"", true),
                    patch(b"phase", b"scanning", false),
                    patch(b"switched", b"", false),
                    effect(2),
                ]);
                self.seq(v, 102)
            }
            102 => {
                if self.q.mode == b"targets" {
                    self.check(self.baseline.clone(), 110)
                } else if self.region.is_empty() {
                    self.seq(
                        vec![
                            effect(1),
                            message(b"note", 5),
                            patch(b"demanded", b"", false),
                        ],
                        9900,
                    )
                } else {
                    self.emit(1, 120)
                }
            }
            110 => {
                self.base = self.rows.clone();
                if passed(&self.base) {
                    self.check(self.extras.clone(), 111)
                } else {
                    self.emit(1, 114)
                }
            }
            111 => {
                let mut combined = self.base.clone();
                combined.extend(self.rows.clone());
                self.rows = combined;
                self.emit(1, 112)
            }
            112 => self.record(self.rows.clone(), 113),
            113 => {
                let v = if passed(&self.rows) {
                    finish_recovery()
                } else {
                    let mut v = vec![message(b"note", 10), patch(b"alert", b"", false)];
                    v.extend(schedule(b"targets"));
                    v
                };
                self.seq(v, 9900)
            }
            114 => self.record(self.base.clone(), 115),
            115 => {
                let mut v = vec![message(b"note", 11)];
                v.extend(self.queue_retry(b"scan", 3));
                self.seq(v, 9900)
            }
            120 => {
                self.emit(7, 121);
                self.e.name = self.region.clone()
            }
            121 => {
                let mut names: Vec<_> = r
                    .candidates
                    .into_iter()
                    .filter(|c| {
                        c.verified && c.country == self.region && !c.cooldown && c.age <= r.ttl
                    })
                    .map(|c| c.name)
                    .collect();
                names.sort();
                self.ordered = vec![self.original.clone()];
                self.ordered
                    .extend(names.into_iter().filter(|n| n != &self.original));
                self.emit(2, 122)
            }
            122 => {
                self.candidate_i = 0;
                self.candidate()
            }
            129 => {
                if r.ok {
                    self.check(self.baseline.clone(), 130)
                } else {
                    self.candidate_i += 1;
                    self.candidate()
                }
            }
            130 => {
                self.base = self.rows.clone();
                if self.ordered[self.candidate_i] == self.original {
                    self.original_base = Some(self.base.clone())
                }
                if !passed(&self.base) {
                    self.last = self.base.clone();
                    self.emit(1, 131)
                } else if !self.demanded || self.extras.is_empty() {
                    self.emit(1, 134)
                } else {
                    self.check(self.extras.clone(), 140)
                }
            }
            131 => self.record(self.base.clone(), 132),
            132 => self.emit(2, 133),
            133 => {
                self.candidate_i += 1;
                self.candidate()
            }
            134 => self.record(self.base.clone(), 135),
            135 => {
                let mut v = finish_recovery();
                v.push(message(b"note", 13));
                self.seq(v, 9900)
            }
            140 => {
                self.last = self.base.clone();
                self.last.extend(self.rows.clone());
                self.emit(1, 141)
            }
            141 => self.record(self.last.clone(), 142),
            142 => {
                if passed(&self.last) {
                    self.seq(finish_recovery(), 9900)
                } else {
                    self.emit(2, 133)
                }
            }
            160 => self.active(161),
            161 => self.emit(2, 162),
            162 => {
                if self.active != self.original {
                    self.switch(self.original.clone(), true, 163)
                } else {
                    self.go(163, Reply::default())
                }
            }
            163 => {
                if let Some(base) = self.original_base.clone() {
                    self.base = base;
                    self.go(165, Reply::default())
                } else {
                    self.check(self.baseline.clone(), 164)
                }
            }
            164 => {
                self.base = self.rows.clone();
                self.go(165, Reply::default())
            }
            165 => {
                self.rows = self.base.clone();
                if self.demanded && !self.last.is_empty() {
                    let names: Vec<_> = self.baseline.iter().map(|t| &t.name).collect();
                    self.rows.extend(
                        self.last
                            .iter()
                            .filter(|r| !names.contains(&&r.name))
                            .cloned(),
                    )
                }
                self.emit(1, 166)
            }
            166 => self.record(self.rows.clone(), 167),
            167 => {
                let v = if passed(&self.base) {
                    let mut v = vec![message(b"note", 15), patch(b"alert", b"", false)];
                    if self.demanded {
                        v.extend(schedule(b"targets"))
                    }
                    v
                } else {
                    let mut note = message(b"note", 16);
                    note.strings = vec![self.region.clone()];
                    let mut v = vec![note];
                    v.extend(self.queue_retry(b"scan", 17));
                    v
                };
                self.seq(v, 9900)
            }
            _ => panic!("invalid path continuation {pc}"),
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[derive(Clone)]
    struct Fake {
        s: Snapshot,
        active: Vec<u8>,
        rows: std::collections::BTreeMap<i64, Row>,
        next: i64,
    }
    fn fake() -> Fake {
        let base = vec![
            Target {
                token: 1,
                name: b"G".to_vec(),
                enabled: true,
                baseline: true,
            },
            Target {
                token: 2,
                name: b"Y".to_vec(),
                enabled: true,
                baseline: true,
            },
        ];
        Fake {
            s: Snapshot {
                enabled: true,
                locked_region: b"US".to_vec(),
                baseline: base.clone(),
                targets: vec![
                    base[0].clone(),
                    base[1].clone(),
                    Target {
                        token: 3,
                        name: b"Extra".to_vec(),
                        enabled: true,
                        baseline: false,
                    },
                ],
                ..Snapshot::default()
            },
            active: b"original".to_vec(),
            rows: Default::default(),
            next: 10,
        }
    }
    fn run(
        op: u8,
        mode: &[u8],
        mut f: Fake,
        mut change: impl FnMut(&Effect, &mut Reply, &mut Fake),
    ) -> (Vec<Effect>, Fake) {
        let mut flow = Flow::new(Request {
            op,
            mode: mode.to_vec(),
            ..Request::default()
        })
        .unwrap();
        let mut trace = vec![];
        for _ in 0..2000 {
            let e = flow.next();
            if e.kind == 0 {
                return (trace, f);
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: true,
                found: true,
                verified: true,
                ttl: 100,
                ..Reply::default()
            };
            match e.kind {
                1 => r.snapshot = Some(f.s.clone()),
                4 => r.name = f.active.clone(),
                5 => r.name = b"US".to_vec(),
                7 => {
                    r.candidates = vec![Candidate {
                        name: b"candidate".to_vec(),
                        country: b"US".to_vec(),
                        verified: true,
                        ..Candidate::default()
                    }]
                }
                8 => {
                    f.next += 1;
                    r.row = Row {
                        token: f.next,
                        name: match e.a {
                            1 => b"G".to_vec(),
                            2 => b"Y".to_vec(),
                            _ => b"Extra".to_vec(),
                        },
                        passed: true,
                    }
                }
                13 => r.flag = false,
                _ => {}
            }
            change(&e, &mut r, &mut f);
            if e.kind == 8 {
                f.rows.insert(r.row.token, r.row.clone());
            }
            if e.kind == 12 && r.ok {
                f.active = e.name.clone()
            }
            if e.kind == 9 {
                match e.name.as_slice() {
                    b"busy" => f.s.busy = e.a != 0,
                    b"demanded" => f.s.demanded = e.a != 0,
                    b"retry-mode" => f.s.retry_mode = e.text.clone(),
                    b"retry-at" => {
                        f.s.retry_at = if e.b == -1 {
                            b"future".to_vec()
                        } else {
                            e.text.clone()
                        }
                    }
                    b"alert-increment" => f.s.alert_id = f.s.alert_id.wrapping_add(1),
                    _ => {}
                }
            }
            trace.push(e);
            flow.advance(r).unwrap()
        }
        panic!("round did not terminate")
    }
    fn sets<'a>(t: &'a [Effect], name: &[u8]) -> Vec<&'a Effect> {
        t.iter().filter(|e| e.kind == 9 && e.name == name).collect()
    }
    #[test]
    fn baseline_success_never_probes_extras_or_switches() {
        let (t, f) = run(0, b"", fake(), |_, _, _| {});
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 8)
                .map(|e| e.a)
                .collect::<Vec<_>>(),
            [1, 2]
        );
        assert!(!t.iter().any(|e| e.kind == 12));
        assert!(!f.s.busy);
        assert_eq!(sets(&t, b"running").last().unwrap().a, 1);
        assert_eq!(sets(&t, b"note").last().unwrap().b, 1);
    }
    #[test]
    fn baseline_gates_and_recovery_does_not_gate_enabled() {
        for gate in 0..3 {
            let mut f = fake();
            if gate == 1 {
                f.s.busy = true
            }
            if gate == 2 {
                f.s.enabled = false
            }
            let (t, _) = run(0, b"", f, |e, r, _| {
                if gate == 0 && e.kind == 3 {
                    r.flag = false
                }
            });
            assert!(!t.iter().any(|e| e.kind == 8));
        }
        let mut f = fake();
        f.s.enabled = false;
        let (t, _) = run(1, b"manual", f, |_, _, _| {});
        assert!(t.iter().any(|e| e.kind == 8));
    }
    #[test]
    fn baseline_failed_chain_clears_retry_no_switch() {
        let mut f = fake();
        f.s.chain = true;
        f.s.retry_at = b"future".to_vec();
        f.s.retry_mode = b"scan".to_vec();
        let (t, f) = run(0, b"", f, |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
        });
        assert!(f.s.retry_at.is_empty());
        assert!(!t.iter().any(|e| e.kind == 12));
        assert_eq!(sets(&t, b"note").last().unwrap().b, 2);
    }
    #[test]
    fn baseline_queue_alert_wrapping_and_existing_retry() {
        let mut f = fake();
        f.s.alert_id = u64::MAX;
        let (t, f) = run(0, b"", f, |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
        });
        assert_eq!(f.s.alert_id, 0);
        assert_eq!(sets(&t, b"alert-increment").len(), 1);
        let mut f = fake();
        f.s.retry_at = b"future".to_vec();
        let (t, _) = run(0, b"", f, |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
        });
        assert!(sets(&t, b"alert-increment").is_empty());
    }
    #[test]
    fn baseline_clears_scan_then_schedules_demanded_targets() {
        let mut f = fake();
        f.s.retry_mode = b"scan".to_vec();
        f.s.retry_at = b"old".to_vec();
        f.s.demanded = true;
        let (t, f) = run(0, b"", f, |_, _, _| {});
        assert_eq!(f.s.retry_mode, b"targets");
        assert_eq!(sets(&t, b"retry-at").len(), 2);
    }
    #[test]
    fn targets_mode_does_not_require_region() {
        let mut f = fake();
        f.s.locked_region.clear();
        let (t, _) = run(1, b"targets", f, |e, r, _| {
            if e.kind == 5 {
                r.found = false
            }
        });
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 8)
                .map(|e| e.a)
                .collect::<Vec<_>>(),
            [1, 2, 3]
        );
        assert!(!t.iter().any(|e| e.kind == 7 || e.kind == 12));
    }
    #[test]
    fn scan_unknown_region_no_probe_and_clears_demand() {
        let mut f = fake();
        f.s.locked_region.clear();
        let (t, f) = run(1, b"manual", f, |e, r, _| {
            if e.kind == 5 {
                r.found = false
            }
        });
        assert!(!t.iter().any(|e| e.kind == 8));
        assert!(!f.s.demanded);
        assert_eq!(sets(&t, b"note").last().unwrap().b, 6);
    }
    #[test]
    fn baseline_failure_prevents_extras_per_candidate() {
        let (t, _) = run(1, b"manual", fake(), |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
        });
        assert!(!t.iter().any(|e| e.kind == 8 && e.a == 3));
        assert_eq!(t.iter().filter(|e| e.kind == 7).count(), 1);
        assert!(t
            .iter()
            .filter(|e| e.kind == 12)
            .all(|e| e.name != b"REJECT"));
    }
    #[test]
    fn fixed_candidates_sort_filter_and_switch_failure_skips() {
        let (t, _) = run(1, b"manual", fake(), |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
            if e.kind == 7 {
                r.candidates = vec![
                    Candidate {
                        name: b"z".to_vec(),
                        country: b"US".to_vec(),
                        verified: true,
                        age: 100,
                        ..Candidate::default()
                    },
                    Candidate {
                        name: b"a".to_vec(),
                        country: b"US".to_vec(),
                        verified: true,
                        ..Candidate::default()
                    },
                    Candidate {
                        name: b"wrong".to_vec(),
                        country: b"JP".to_vec(),
                        verified: true,
                        ..Candidate::default()
                    },
                    Candidate {
                        name: b"cool".to_vec(),
                        country: b"US".to_vec(),
                        verified: true,
                        cooldown: true,
                        ..Candidate::default()
                    },
                ]
            }
            if e.kind == 12 {
                r.ok = false
            }
        });
        let puts: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 12)
            .map(|e| e.name.clone())
            .collect();
        assert_eq!(puts, [b"a".to_vec(), b"z".to_vec()]);
    }
    #[test]
    fn failed_restore_is_ignored_and_keeps_mixed_original_baseline() {
        let (t, f) = run(1, b"manual", fake(), |e, r, f| {
            if e.kind == 8 && e.a == 3 {
                r.row.passed = false;
                r.row.name = if f.active == b"original" {
                    b"origextra".to_vec()
                } else {
                    b"candidateextra".to_vec()
                }
            }
            if e.kind == 12 && e.name == b"original" {
                r.ok = false
            }
        });
        assert_eq!(f.active, b"candidate");
        let last = sets(&t, b"results").last().unwrap().indices.clone();
        let names: Vec<_> = last.iter().map(|i| f.rows[i].name.clone()).collect();
        assert_eq!(
            names,
            [b"G".to_vec(), b"Y".to_vec(), b"candidateextra".to_vec()]
        );
        assert_eq!(f.s.retry_mode, b"targets");
        assert_eq!(sets(&t, b"running").last().unwrap().a, 0);
    }
    #[test]
    fn empty_non_nil_baseline_not_reprobed() {
        let mut f = fake();
        f.s.baseline.clear();
        f.s.targets.retain(|t| !t.baseline);
        let (t, _) = run(1, b"manual", f, |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
        });
        assert!(t.iter().filter(|e| e.kind == 8).all(|e| e.a == 3));
    }
    #[test]
    fn targets_baseline_failure_queues_scan_without_pool() {
        let (t, f) = run(1, b"targets", fake(), |e, r, _| {
            if e.kind == 8 {
                r.row.passed = false
            }
        });
        assert_eq!(f.s.retry_mode, b"scan");
        assert!(!t.iter().any(|e| e.kind == 7 || e.kind == 12));
    }
    #[test]
    fn invalid_reply_preserves_first_effect() {
        let mut f = Flow::new(Request::default()).unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), e);
        assert!(f
            .advance(Reply {
                kind: 8,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), e);
    }
}
