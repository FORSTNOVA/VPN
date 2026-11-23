//! Path monitor scheduler and control decisions. Timers, channels and clocks are opaque Go resources.
use crate::path_monitor_logic::{Connection, Input, Sample, Settings, Target};
#[derive(Clone, Debug, Default)]
pub struct TargetRef {
    pub token: i64,
    pub label: Vec<u8>,
    pub enabled: bool,
    pub baseline: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub guard: bool,
    pub enabled: bool,
    pub interval: i64,
    pub targets: Vec<TargetRef>,
    pub previous: Vec<Sample>,
}
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub identity: bool,
    pub cancel: bool,
    pub enabled: bool,
    pub busy: bool,
    pub interval: i64,
    pub retry_at: Vec<u8>,
    pub retry_mode: Vec<u8>,
    pub targets: Vec<TargetRef>,
    pub presets: Vec<Target>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub value: i64,
    pub token: i64,
    pub name: Vec<u8>,
    pub error: Vec<u8>,
    pub input: Input,
    pub connections: Vec<Connection>,
    pub snapshot: Option<Snapshot>,
    pub samples: Vec<Sample>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub name: Vec<u8>,
    pub text: Vec<u8>,
    pub indices: Vec<i64>,
    pub strings: Vec<Vec<u8>>,
    pub settings: Option<Settings>,
    pub samples: Vec<Sample>,
}
use crate::path_monitor_logic::{evaluate, DemandTarget, Normalizer};
use std::collections::VecDeque;
fn fx(kind: u8) -> Effect {
    Effect {
        kind,
        ..Effect::default()
    }
}
fn set(name: &[u8], text: &[u8], flag: bool) -> Effect {
    Effect {
        kind: 21,
        name: name.to_vec(),
        text: text.to_vec(),
        a: flag as i64,
        ..Effect::default()
    }
}
fn clear_retry() -> Vec<Effect> {
    vec![set(b"retry-at", b"", false), set(b"retry-mode", b"", false)]
}
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    pc: u16,
    queue: VecDeque<Effect>,
    after: u16,
    locked: bool,
    start_after: u16,
    stop_after: u16,
    status: i64,
    error: i64,
    error_input: i64,
    normalized: Option<Settings>,
    normalizer: Option<Normalizer>,
    index: usize,
    previous: Vec<Sample>,
    connections: Vec<Connection>,
    extras: Vec<TargetRef>,
    targetfacts: Vec<DemandTarget>,
    labels: Vec<Vec<u8>>,
    labels_valid: bool,
    demand_after: u16,
    admit_after: u16,
    admitted: bool,
    retry_after: u16,
    retry_now: i64,
    retry_mode: Vec<u8>,
    label_index: usize,
    timers: usize,
    stale: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 9 {
            return Err("invalid path monitor operation");
        }
        if q.targets.len() > 100000 || q.previous.len() > 100000 {
            return Err("too many path monitor inputs");
        }
        let previous = vec![];
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            pc: 0,
            queue: VecDeque::new(),
            after: 0,
            locked: false,
            start_after: 0,
            stop_after: 0,
            status: 0,
            error: 0,
            error_input: 0,
            normalized: None,
            normalizer: None,
            index: 0,
            previous,
            connections: vec![],
            extras: vec![],
            targetfacts: vec![],
            labels: vec![],
            labels_valid: false,
            demand_after: 0,
            admit_after: 0,
            admitted: false,
            retry_after: 0,
            retry_now: 0,
            retry_mode: vec![],
            label_index: 0,
            timers: 0,
            stale: false,
        };
        match f.q.op {
            0 => f.start(9990),
            1 => f.stop(9990),
            2 => {
                f.emit(10, 200);
                f.e.name = b"baseline".to_vec();
                f.e.a = f.q.interval
            }
            3 => f.demand(9990),
            4 => f.retry(0, 9990),
            5 | 6 => f.emit(4, 100),
            7 => f.emit(1, 400),
            8 => f.emit(1, 410),
            9 => f.emit(1, 420),
            _ => unreachable!(),
        }
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = fx(k);
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
            let pc = self.after;
            self.go(pc, Reply::default())
        }
    }
    fn terminal(&mut self) {
        self.emit(63, 9999);
        self.output()
    }
    fn output(&mut self) {
        self.e.a = self.status;
        self.e.b = self.error;
        self.e.text = if self.q.op == 4 {
            self.retry_mode.clone()
        } else {
            vec![]
        };
        self.e.strings = if self.q.op == 3 {
            self.labels.clone()
        } else {
            vec![]
        };
        self.e.indices = vec![
            (self.q.op == 3 && self.labels_valid) as i64,
            self.error_input,
        ];
        self.e.settings = self.normalized.clone()
    }
    fn done(&mut self) {
        if self.locked {
            self.emit(2, 9991)
        } else {
            self.terminal()
        }
    }
    fn response(&mut self, name: &[u8], after: u16) {
        self.emit(23, after);
        self.output();
        self.e.name = name.to_vec()
    }
    fn start(&mut self, after: u16) {
        self.start_after = after;
        self.emit(3, 10)
    }
    fn stop(&mut self, after: u16) {
        self.stop_after = after;
        self.emit(3, 20)
    }
    fn round(&mut self, mode: &[u8], sync: bool, after: u16) {
        self.emit(14, after);
        self.e.name = mode.to_vec();
        self.e.a = self.q.guard as i64;
        if sync {
            self.e.text = b"sync".to_vec()
        }
    }
    fn admit(&mut self, after: u16) {
        self.admit_after = after;
        self.emit(1, 30)
    }
    fn demand(&mut self, after: u16) {
        self.demand_after = after;
        self.labels.clear();
        self.labels_valid = false;
        self.emit(1, 50)
    }
    fn demand_done(&mut self) {
        if self.q.op == 2 && self.stale {
            self.loop_end();
            return;
        }
        let pc = self.demand_after;
        self.go(pc, Reply::default())
    }
    fn retry(&mut self, now: i64, after: u16) {
        self.retry_after = after;
        self.retry_now = now;
        self.retry_mode.clear();
        self.emit(1, 80)
    }
    fn retry_done(&mut self) {
        if self.q.op == 2 && self.stale {
            self.loop_end();
            return;
        }
        let pc = self.retry_after;
        self.go(pc, Reply::default())
    }
    fn norm_next(&mut self) {
        if self.index < self.q.targets.len() {
            self.emit(17, 101);
            self.e.name = b"normalize".to_vec();
            self.e.a = self.q.targets[self.index].token
        } else {
            self.normalized = Some(self.normalizer.take().expect("normalizer").finish());
            if self.q.op == 5 {
                self.done()
            } else {
                self.emit(1, 110)
            }
        }
    }
    fn target_next(&mut self) {
        if self.index < self.extras.len() {
            self.emit(17, 58);
            self.e.a = self.extras[self.index].token
        } else {
            let r = evaluate(&self.previous, &self.targetfacts, &self.connections);
            self.previous = r.current;
            self.labels = r.labels;
            self.labels_valid = true;
            self.emit(18, 60);
            self.e.samples = self.previous.clone()
        }
    }
    fn loop_end(&mut self) {
        let names = [b"baseline".as_slice(), b"demand", b"retry"];
        let v = (0..self.timers)
            .rev()
            .map(|i| {
                let mut e = fx(11);
                e.name = names[i].to_vec();
                e
            })
            .collect();
        self.seq(v, 9990)
    }
    fn throttle_next(&mut self) {
        if self.label_index >= self.labels.len() {
            self.emit(12, 210)
        } else {
            let name = self.labels[self.label_index].clone();
            self.emit(26, 229);
            self.e.name = name
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected monitor reply");
        }
        if matches!(r.kind, 1 | 3 | 4) && r.snapshot.is_none() {
            return Err("missing monitor snapshot");
        }
        if r.connections.len() > 100000 || r.samples.len() > 100000 {
            return Err("too many connections");
        }
        if let Some(s) = &r.snapshot {
            if s.targets.len() > 100000 || s.presets.len() > 100000 {
                return Err("too many monitor targets");
            }
        }
        if matches!(r.kind, 1 | 3 | 4) {
            self.s = r.snapshot.clone().unwrap()
        }
        if r.kind == 1 {
            if self.q.op == 2 && !self.s.identity {
                self.stale = true;
            }
            self.locked = true
        }
        if r.kind == 2 {
            self.locked = false
        }
        if self.e.kind == 21 {
            match self.e.name.as_slice() {
                b"retry-at" => self.s.retry_at = self.e.text.clone(),
                b"retry-mode" => self.s.retry_mode = self.e.text.clone(),
                _ => {}
            }
        }
        let pc = self.pc;
        self.go(pc, r);
        Ok(())
    }
    fn go(&mut self, pc: u16, r: Reply) {
        match pc {
            9000 => self.seq_next(),
            9990 => self.done(),
            9991 => self.terminal(),
            9999 => self.emit(0, 9999),
            10 => {
                if self.s.cancel || !self.s.enabled {
                    let pc = self.start_after;
                    self.go(pc, Reply::default())
                } else {
                    let duration = self.s.interval.wrapping_mul(1_000_000_000);
                    let mut log = fx(22);
                    log.a = duration;
                    let mut spawn = fx(13);
                    spawn.a = duration;
                    self.seq(vec![fx(8), log, spawn], self.start_after)
                }
            }
            20 => {
                let mut v = vec![];
                if self.s.cancel {
                    v.push(fx(9))
                }
                v.extend([
                    set(b"demanded", b"", false),
                    set(b"alert", b"", false),
                    set(b"phase", b"", false),
                ]);
                v.extend(clear_retry());
                self.seq(v, self.stop_after)
            }
            30 => {
                self.admitted = self.s.identity;
                self.emit(2, 31)
            }
            31 => {
                let pc = self.admit_after;
                self.go(pc, Reply::default())
            }
            50 => {
                if !self.s.identity {
                    self.emit(2, 59)
                } else {
                    self.emit(5, 51)
                }
            }
            51 => {
                if !r.flag || !self.s.enabled || self.s.busy {
                    self.emit(2, 59)
                } else {
                    self.extras = self
                        .s
                        .targets
                        .iter()
                        .filter(|t| t.enabled && !t.baseline)
                        .cloned()
                        .collect();
                    self.seq(vec![fx(15), fx(2)], 52)
                }
            }
            52 => {
                if self.extras.is_empty() {
                    self.demand_done()
                } else {
                    self.emit(16, 53)
                }
            }
            53 => {
                if !r.ok {
                    self.demand_done()
                } else {
                    self.connections = r.connections;
                    if self.q.guard {
                        self.admit(54)
                    } else {
                        self.go(55, Reply::default())
                    }
                }
            }
            54 => {
                if self.admitted {
                    self.go(55, Reply::default())
                } else {
                    self.demand_done()
                }
            }
            55 => self.emit(25, 56),
            56 => {
                if !r.flag {
                    self.stale = true;
                    self.demand_done();
                    return;
                }
                self.previous = r.samples;
                self.index = 0;
                self.targetfacts.clear();
                self.target_next()
            }
            58 => {
                self.targetfacts.push(DemandTarget {
                    label: self.extras[self.index].label.clone(),
                    host: r.input.host,
                    valid: r.input.valid,
                });
                self.index += 1;
                self.target_next()
            }
            59 => self.demand_done(),
            60 => {
                self.previous.clear();
                if !r.flag {
                    self.labels.clear();
                    self.labels_valid = false;
                    self.stale = true;
                }
                self.demand_done()
            }
            80 => {
                if !self.s.identity || self.s.busy || !self.s.enabled || self.s.retry_at.is_empty()
                {
                    self.emit(2, 83)
                } else {
                    self.emit(7, 81);
                    self.e.a = self.retry_now;
                    self.e.text = self.s.retry_at.clone()
                }
            }
            81 => {
                if !r.ok || r.flag {
                    self.emit(2, 83)
                } else {
                    self.retry_mode = self.s.retry_mode.clone();
                    let mut v = clear_retry();
                    v.push(set(b"phase", b"scanning", false));
                    v.push(fx(2));
                    self.seq(v, 83)
                }
            }
            83 => self.retry_done(),
            100 => {
                self.normalizer = Some(Normalizer::new(
                    self.q.enabled,
                    self.q.interval,
                    self.s.presets.clone(),
                ));
                self.index = 0;
                self.norm_next()
            }
            101 => {
                let mut input = r.input;
                input.enabled = self.q.targets[self.index].enabled;
                match self.normalizer.as_mut().unwrap().push(input) {
                    Ok(()) => {
                        self.index += 1;
                        self.norm_next()
                    }
                    Err(err) => {
                        self.error = err.kind as i64;
                        self.error_input = self.q.targets[self.index].token;
                        if self.q.op == 6 {
                            self.status = 400;
                            self.response(b"", 9990)
                        } else {
                            self.done()
                        }
                    }
                }
            }
            110 => {
                self.emit(19, 111);
                self.e.settings = self.normalized.clone()
            }
            111 => self.emit(20, 112),
            112 => {
                if !r.ok {
                    self.error = 4;
                    self.error_input = r.token;
                    self.status = 500;
                    self.response(b"", 9990)
                } else {
                    self.stop(113)
                }
            }
            113 => self.start(114),
            114 => self.response(b"settings", 9990),
            200 => {
                self.timers = 1;
                self.emit(10, 201);
                self.e.name = b"demand".to_vec();
                self.e.a = 1_000_000_000
            }
            201 => {
                self.timers = 2;
                self.emit(10, 202);
                self.e.name = b"retry".to_vec();
                self.e.a = 1_000_000_000
            }
            202 => {
                self.timers = 3;
                self.admit(203)
            }
            203 => {
                if self.admitted {
                    self.round(b"baseline", false, 204)
                } else {
                    self.loop_end()
                }
            }
            204 => self.emit(12, 210),
            210 => match r.name.as_slice() {
                b"cancel" => self.loop_end(),
                b"baseline" => self.admit(211),
                b"demand" => self.demand(230),
                b"retry" => {
                    self.emit(6, 250);
                    self.e.name = b"retry".to_vec();
                }
                _ => self.emit(12, 210),
            },
            211 => {
                if self.admitted {
                    self.round(b"baseline", true, 204)
                } else {
                    self.loop_end()
                }
            }
            229 => {
                if !r.flag {
                    self.loop_end()
                } else {
                    self.emit(24, 231);
                    self.e.name = self.labels[self.label_index].clone();
                    self.e.a = r.token;
                }
            }
            230 => {
                self.label_index = 0;
                self.throttle_next()
            }
            231 => {
                if r.flag {
                    self.label_index += 1;
                    self.throttle_next()
                } else {
                    self.emit(1, 232)
                }
            }
            232 => {
                if !self.s.identity {
                    self.emit(2, 239)
                } else if self.s.busy {
                    self.emit(2, 238)
                } else {
                    self.emit(5, 233)
                }
            }
            233 => {
                if r.flag && self.s.enabled {
                    self.emit(6, 234);
                    self.e.name = b"demand".to_vec();
                    self.e.text = self.labels[self.label_index].clone();
                } else {
                    self.emit(2, 238)
                }
            }
            234 => {
                self.emit(27, 236);
                self.e.name = self.labels[self.label_index].clone();
                self.e.a = r.token;
            }
            236 => self.seq(
                vec![
                    set(b"demanded", b"", true),
                    set(b"phase", b"scanning", false),
                    fx(2),
                ],
                235,
            ),
            235 => self.round(b"demand", false, 204),
            238 => {
                self.label_index += 1;
                self.throttle_next()
            }
            239 => self.loop_end(),
            250 => self.retry(r.token, 251),
            251 => {
                if self.retry_mode.is_empty() {
                    self.emit(12, 210)
                } else {
                    let mode = self.retry_mode.clone();
                    self.round(&mode, false, 204)
                }
            }
            400 => self.response(b"snapshot", 9990),
            410 => self.emit(5, 411),
            411 => {
                self.admitted = r.flag;
                self.emit(2, 412)
            }
            412 => {
                if !self.admitted {
                    self.status = 409;
                    self.error = 5;
                    self.response(b"", 9990)
                } else {
                    self.round(b"manual", false, 413)
                }
            }
            413 => self.response(b"", 9990),
            420 => {
                let mut note = set(b"note", b"", false);
                note.b = 5;
                let mut v = vec![
                    set(b"demanded", b"", false),
                    set(b"alert", b"", false),
                    note,
                ];
                v.extend(clear_retry());
                v.extend([set(b"phase", b"baseline", false), fx(2)]);
                self.seq(v, 421)
            }
            421 => self.response(b"", 9990),
            _ => panic!("invalid monitor continuation {pc}"),
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[derive(Clone)]
    struct Fake {
        s: Snapshot,
        events: VecDeque<Vec<u8>>,
        locked: bool,
        clock: i64,
        previous: Vec<Sample>,
        throttle: std::collections::BTreeMap<Vec<u8>, i64>,
    }
    fn state() -> Fake {
        Fake {
            s: Snapshot {
                identity: true,
                enabled: true,
                interval: 120,
                targets: vec![TargetRef {
                    token: 1,
                    label: b"Extra".to_vec(),
                    enabled: true,
                    baseline: false,
                }],
                presets: vec![],
                ..Snapshot::default()
            },
            events: VecDeque::new(),
            locked: false,
            clock: 0,
            previous: vec![],
            throttle: std::collections::BTreeMap::new(),
        }
    }
    fn run(
        q: Request,
        mut f: Fake,
        mut change: impl FnMut(&Effect, &mut Reply, &mut Fake),
    ) -> (Vec<Effect>, Fake) {
        let mut flow = Flow::new(q).unwrap();
        let mut trace = vec![];
        for _ in 0..2000 {
            let e = flow.next();
            if e.kind == 0 {
                return (trace, f);
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: false,
                ..Reply::default()
            };
            match e.kind {
                1 => {
                    assert!(!f.locked, "double lock");
                    f.locked = true;
                    r.snapshot = Some(f.s.clone())
                }
                2 => {
                    assert!(f.locked, "unlock without lock");
                    f.locked = false
                }
                3 | 4 => r.snapshot = Some(f.s.clone()),
                5 => r.flag = true,
                6 => {
                    f.clock += 1;
                    r.token = f.clock
                }
                12 => r.name = f.events.pop_front().unwrap_or_else(|| b"cancel".to_vec()),
                16 => {
                    r.connections = vec![Connection {
                        id: b"c".to_vec(),
                        host: b"sub.example.com".to_vec(),
                        upload: 1,
                        ..Connection::default()
                    }]
                }
                18 => {
                    r.flag = true;
                }
                25 => {
                    r.flag = true;
                    r.samples = f.previous.clone();
                }
                26 => {
                    r.flag = true;
                    r.token = *f.throttle.get(&e.name).unwrap_or(&0);
                }
                17 => {
                    r.input = Input {
                        url: e.a,
                        nonempty: true,
                        label: b"label".to_vec(),
                        host: b"example.com".to_vec(),
                        valid: true,
                        runes: 5,
                        enabled: true,
                    }
                }
                _ => {}
            }
            change(&e, &mut r, &mut f);
            match e.kind {
                8 => f.s.cancel = true,
                9 => f.s.cancel = false,
                18 if r.flag => {
                    f.previous = e.samples.clone();
                }
                27 => {
                    f.throttle.insert(e.name.clone(), e.a);
                }
                19 => {
                    let s = e.settings.as_ref().unwrap();
                    f.s.enabled = s.enabled;
                    f.s.interval = s.interval
                }
                21 => match e.name.as_slice() {
                    b"retry-at" => f.s.retry_at = e.text.clone(),
                    b"retry-mode" => f.s.retry_mode = e.text.clone(),
                    _ => {}
                },
                _ => {}
            }
            trace.push(e);
            flow.advance(r).unwrap()
        }
        panic!("did not finish")
    }
    fn q(op: u8) -> Request {
        Request {
            op,
            guard: true,
            ..Request::default()
        }
    }
    #[test]
    fn borrowed_start_and_stop_do_not_lock_or_wait() {
        let (t, f) = run(q(0), state(), |_, _, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            [3, 8, 22, 13, 63]
        );
        assert!(f.s.cancel);
        let (t, _) = run(q(1), f, |_, _, _| {});
        assert!(t.iter().any(|e| e.kind == 9));
        assert!(!t.iter().any(|e| matches!(e.kind, 1 | 12 | 14)));
    }
    #[test]
    fn start_cancel_or_disabled_skips_publish() {
        for cancel in [false, true] {
            let mut f = state();
            f.s.cancel = cancel;
            f.s.enabled = cancel;
            let (t, _) = run(q(0), f, |_, _, _| {});
            assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), [3, 63]);
        }
    }
    #[test]
    fn loop_initial_async_tick_sync_reverse_cleanup() {
        let mut f = state();
        f.events = vec![b"baseline".to_vec(), b"cancel".to_vec()].into();
        let (t, f) = run(q(2), f, |_, _, _| {});
        let rounds: Vec<_> = t.iter().filter(|e| e.kind == 14).collect();
        assert_eq!(rounds.len(), 2);
        assert!(rounds[0].text.is_empty());
        assert_eq!(rounds[1].text, b"sync");
        assert!(rounds.iter().all(|e| e.a == 1));
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 11)
                .map(|e| e.name.clone())
                .collect::<Vec<_>>(),
            [b"retry".to_vec(), b"demand".to_vec(), b"baseline".to_vec()]
        );
        assert!(!f.locked);
    }
    #[test]
    fn stale_initial_loop_never_spawns() {
        let mut f = state();
        f.s.identity = false;
        let (t, _) = run(q(2), f, |_, _, _| {});
        assert!(!t.iter().any(|e| matches!(e.kind, 12 | 14 | 9)));
        assert_eq!(t.iter().filter(|e| e.kind == 11).count(), 3);
    }
    #[test]
    fn successful_demand_replaces_samples_and_nil_distinction() {
        let (t, _) = run(q(3), state(), |_, _, _| {});
        let last = t.last().unwrap();
        assert_eq!(last.strings, [b"Extra".to_vec()]);
        assert_eq!(last.indices[0], 1);
        assert_eq!(t.iter().find(|e| e.kind == 18).unwrap().samples.len(), 1);
        let (t, _) = run(q(3), state(), |e, r, _| {
            if e.kind == 16 {
                r.ok = false
            }
        });
        assert_eq!(t.last().unwrap().indices[0], 0);
        assert!(!t.iter().any(|e| e.kind == 18));
    }
    #[test]
    fn late_get_cancellation_cannot_commit_samples() {
        let (t, _) = run(q(3), state(), |e, _, f| {
            if e.kind == 16 {
                f.s.identity = false
            }
        });
        assert!(!t.iter().any(|e| matches!(e.kind, 17 | 18)));
        assert_eq!(t.last().unwrap().indices[0], 0);
    }
    #[test]
    fn stale_authority_read_or_commit_stops_loop_without_another_wait() {
        for fail in [18, 25, 26] {
            let mut f = state();
            f.events = vec![b"demand".to_vec(), b"cancel".to_vec()].into();
            let (t, _) = run(q(2), f, |e, r, _| {
                if e.kind == fail {
                    r.flag = false
                }
            });
            assert_eq!(t.iter().filter(|e| e.kind == 12).count(), 1);
            assert!(!t.iter().any(|e| e.kind == 27));
        }
    }
    #[test]
    fn demand_uses_fresh_authority_samples_not_request_shadow() {
        let mut request = q(3);
        request.previous = vec![Sample {
            id: b"c".to_vec(),
            upload: 999,
        }];
        let (t, _) = run(request, state(), |_, _, _| {});
        assert_eq!(t.last().unwrap().strings, vec![b"Extra".to_vec()]);
        let (t, _) = run(q(3), state(), |e, r, _| {
            if e.kind == 25 {
                r.samples = vec![Sample {
                    id: b"c".to_vec(),
                    upload: 999,
                }]
            }
        });
        assert!(t.last().unwrap().strings.is_empty());
    }
    #[test]
    fn inactive_demand_tick_is_noop_not_generation_stale() {
        let mut f = state();
        f.s.enabled = false;
        f.events = vec![b"demand".to_vec(), b"cancel".to_vec()].into();
        let (t, _) = run(q(2), f, |_, _, _| {});
        assert_eq!(t.iter().filter(|e| e.kind == 12).count(), 2);
        assert!(!t.iter().any(|e| e.kind == 25 || e.kind == 18));
    }
    #[test]
    fn stale_demand_tick_exits_without_second_wait() {
        let mut f = state();
        f.events = vec![b"demand".to_vec()].into();
        let (t, _) = run(q(2), f, |e, _, f| {
            if e.kind == 16 {
                f.s.identity = false
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 12).count(), 1);
        assert_eq!(t.iter().filter(|e| e.kind == 14).count(), 1);
        assert!(!t.iter().any(|e| e.kind == 18));
    }
    #[test]
    fn demand_dispatch_has_one_lock_and_preserves_throttle_clock() {
        let mut f = state();
        f.events = vec![b"demand".to_vec(), b"demand".to_vec(), b"cancel".to_vec()].into();
        let mut since = 0;
        let mut traffic = 0;
        let (t, _) = run(q(2), f, |e, r, _| {
            if e.kind == 16 {
                traffic += 1;
                r.connections[0].upload = traffic;
            }
            if e.kind == 24 {
                since += 1;
                r.flag = since == 2
            }
        });
        let requests: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 14 && e.name == b"demand")
            .collect();
        assert_eq!(requests.len(), 1);
        let s: Vec<_> = t.iter().filter(|e| e.kind == 24).collect();
        assert_eq!(s[0].a, 0);
        assert!(s[1].a > 0);
        assert!(t.last().unwrap().strings.is_empty());
    }
    #[test]
    fn retry_exact_due_and_before_parse_error() {
        for (ok, before, want) in [
            (true, false, true),
            (true, true, false),
            (false, false, false),
        ] {
            let mut f = state();
            f.s.retry_at = b"deadline".to_vec();
            f.s.retry_mode = b"scan".to_vec();
            let (t, _) = run(q(4), f, |e, r, _| {
                if e.kind == 7 {
                    r.ok = ok;
                    r.flag = before
                }
            });
            assert_eq!(!t.last().unwrap().text.is_empty(), want);
        }
    }
    #[test]
    fn loop_retry_dispatch_and_stale_exit() {
        let mut f = state();
        f.events = vec![b"retry".to_vec(), b"cancel".to_vec()].into();
        f.s.retry_at = b"deadline".to_vec();
        f.s.retry_mode = b"scan".to_vec();
        let (t, _) = run(q(2), f, |_, _, _| {});
        assert!(t.iter().any(|e| e.kind == 14 && e.name == b"scan"));
        assert!(t.last().unwrap().text.is_empty());
    }
    #[test]
    fn update_commits_before_save_and_error_does_not_stop() {
        let req = Request {
            op: 6,
            enabled: true,
            interval: 90,
            targets: vec![TargetRef {
                token: 10,
                enabled: true,
                ..TargetRef::default()
            }],
            ..Request::default()
        };
        let (t, f) = run(req, state(), |e, r, _| {
            if e.kind == 20 {
                r.ok = false;
                r.token = 55
            }
        });
        assert_eq!(f.s.interval, 90);
        assert!(!t.iter().any(|e| e.kind == 9 || e.kind == 13));
        assert_eq!(t.last().unwrap().a, 500);
        assert_eq!(t.last().unwrap().b, 4);
    }
    #[test]
    fn update_success_stops_restarts_response_then_unlock() {
        let mut f = state();
        f.s.cancel = true;
        let (t, _) = run(
            Request {
                op: 6,
                enabled: true,
                interval: 60,
                ..Request::default()
            },
            f,
            |_, _, _| {},
        );
        let ks: Vec<_> = t.iter().map(|e| e.kind).collect();
        assert!(ks.iter().position(|k| *k == 9) < ks.iter().position(|k| *k == 8));
        assert_eq!(&ks[ks.len() - 3..], &[23, 2, 63]);
    }
    #[test]
    fn normalization_error_before_lock() {
        let req = Request {
            op: 6,
            targets: vec![TargetRef {
                token: 55,
                ..TargetRef::default()
            }],
            ..Request::default()
        };
        let (t, _) = run(req, state(), |e, r, _| {
            if e.kind == 17 {
                r.input.valid = false
            }
        });
        assert!(!t.iter().any(|e| e.kind == 1));
        assert_eq!((t.last().unwrap().a, t.last().unwrap().b), (400, 1));
        assert_eq!(t.last().unwrap().indices[1], 55);
    }
    #[test]
    fn manual_running_gate_and_cancel_not_stop_inflight() {
        let (t, _) = run(q(8), state(), |e, r, _| {
            if e.kind == 5 {
                r.flag = false
            }
        });
        assert_eq!(t.last().unwrap().a, 409);
        assert!(!t.iter().any(|e| e.kind == 14));
        let (t, _) = run(q(9), state(), |_, _, _| {});
        assert!(!t.iter().any(|e| matches!(e.kind, 9 | 11 | 14)));
        assert!(t
            .iter()
            .any(|e| e.kind == 21 && e.name == b"note" && e.b == 5));
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = Flow::new(q(7)).unwrap();
        let before = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(before, f.next());
    }
}
