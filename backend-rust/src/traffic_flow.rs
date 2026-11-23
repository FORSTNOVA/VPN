//! Traffic lifecycle orchestration. All locks, timers, snapshots and storage stay in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub period: Vec<u8>,
    pub date: Vec<u8>,
    pub group: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub store: bool,
    pub cancel: bool,
    pub matches: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub text: Vec<u8>,
    pub data: Vec<u8>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub value: i64,
    pub token: i64,
    pub error: Vec<u8>,
    pub snapshot: Option<Snapshot>,
}
// 1 lock,2 borrowed snapshot,3 running,4 GET,5 clear baseline,6 now,
// 7 rate,8 set baseline,9 rows,10 record,11 sampling error,12 publish cancel,
// 13 spawn,14 close cancel+nil,15 ticker new,16 wait (tick=true),17 reserved,
// 18 unlock,19 ticker stop,20 date parse,21 pagination parse,22 read history.
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    ending: bool,
}
impl Flow {
    pub fn new(mut q: Request) -> Result<Self, &'static str> {
        if q.op > 5 {
            return Err("invalid traffic operation");
        }
        if q.period.is_empty() {
            q.period = b"day".to_vec()
        }
        if q.group.is_empty() {
            q.group = b"app".to_vec()
        }
        let k = match q.op {
            0 => 1,
            1..=3 => 2,
            4 => 15,
            5 => 20,
            _ => unreachable!(),
        };
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            ending: false,
        };
        f.emit(k);
        if k == 20 {
            f.e.text = f.q.period.clone();
            f.e.data = f.q.date.clone()
        }
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, k: u8) {
        self.e = Effect {
            kind: k,
            ..Effect::default()
        }
    }
    fn result(&mut self, code: i64) {
        self.emit(63);
        self.e.a = code
    }
    fn collected(&mut self) {
        if self.q.op == 4 {
            self.emit(18)
        } else {
            self.result(0)
        }
    }
    fn collect(&mut self) {
        if self.s.store {
            self.emit(3)
        } else {
            self.collected()
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        let k = self.e.kind;
        if k == 0 || r.kind != k {
            return Err("unexpected traffic reply");
        }
        if matches!(k, 1 | 2) && r.snapshot.is_none() {
            return Err("missing traffic snapshot");
        }
        if let Some(s) = r.snapshot {
            self.s = s
        }
        match k {
            1 => match self.q.op {
                0 => self.emit(3),
                4 => {
                    if self.s.matches {
                        self.collect()
                    } else {
                        self.ending = true;
                        self.emit(18)
                    }
                }
                5 => {
                    if self.s.store {
                        self.emit(6);
                        self.e.a = 1
                    } else {
                        self.result(14)
                    }
                }
                _ => unreachable!(),
            },
            2 => match self.q.op {
                1 => self.collect(),
                2 => {
                    if !self.s.store || self.s.cancel {
                        self.result(0)
                    } else {
                        self.emit(12)
                    }
                }
                3 => {
                    if self.s.cancel {
                        self.emit(14)
                    } else {
                        self.result(0)
                    }
                }
                _ => unreachable!(),
            },
            3 => {
                if r.flag {
                    self.emit(4)
                } else if self.q.op == 0 {
                    self.result(1)
                } else {
                    self.collected()
                }
            }
            4 => {
                if r.ok {
                    self.emit(6)
                } else if self.q.op == 0 {
                    self.emit(5)
                } else {
                    self.emit(11);
                    self.e.a = 1
                }
            }
            5 => self.result(2),
            6 => match self.q.op {
                0 => self.emit(7),
                1 | 4 => self.emit(10),
                5 => self.emit(22),
                _ => unreachable!(),
            },
            7 => self.emit(8),
            8 => self.emit(9),
            9 => self.result(3),
            10 => {
                self.emit(11);
                self.e.a = if r.ok { 0 } else { 2 }
            }
            11 => self.collected(),
            12 => self.emit(13),
            13 | 14 => self.result(0),
            15 => self.emit(16),
            16 => {
                if r.flag {
                    self.emit(1)
                } else {
                    self.emit(19)
                }
            }
            18 => {
                if self.ending {
                    self.emit(19)
                } else {
                    self.emit(16)
                }
            }
            19 => self.result(0),
            20 => {
                if !r.ok {
                    self.result(
                        if matches!(
                            self.q.period.as_slice(),
                            b"hour" | b"day" | b"month" | b"year"
                        ) {
                            11
                        } else {
                            10
                        },
                    )
                } else if !matches!(self.q.group.as_slice(), b"app" | b"connection") {
                    self.result(12)
                } else {
                    self.emit(21)
                }
            }
            21 => {
                if r.ok {
                    self.emit(1)
                } else {
                    self.result(13)
                }
            }
            22 => self.result(if r.ok { 16 } else { 15 }),
            63 => self.emit(0),
            _ => return Err("invalid traffic effect"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn trace(q: Request, s: Snapshot, mut change: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(q).unwrap();
        let mut out = Vec::new();
        for _ in 0..100 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: true,
                snapshot: if matches!(e.kind, 1 | 2) {
                    Some(s.clone())
                } else {
                    None
                },
                ..Reply::default()
            };
            change(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap();
        }
        panic!("unterminated")
    }
    fn kinds(v: &[Effect]) -> Vec<u8> {
        v.iter().map(|e| e.kind).collect()
    }
    fn req(op: u8) -> Request {
        Request {
            op,
            ..Request::default()
        }
    }
    fn state() -> Snapshot {
        Snapshot {
            store: true,
            cancel: false,
            matches: true,
        }
    }
    #[test]
    fn current_success_order() {
        assert_eq!(
            kinds(&trace(req(0), state(), |_, _| {})),
            [1, 3, 4, 6, 7, 8, 9, 63]
        );
    }
    #[test]
    fn current_stopped_preserves_baseline() {
        let t = trace(req(0), state(), |e, r| {
            if e.kind == 3 {
                r.flag = false
            }
        });
        assert_eq!(kinds(&t), [1, 3, 63]);
        assert_eq!(t.last().unwrap().a, 1);
    }
    #[test]
    fn current_get_failure_clears_baseline_only() {
        let t = trace(req(0), state(), |e, r| {
            if e.kind == 4 {
                r.ok = false
            }
        });
        assert_eq!(kinds(&t), [1, 3, 4, 5, 63]);
        assert_eq!(t.last().unwrap().a, 2);
    }
    #[test]
    fn collect_absent_store_shortcircuits_running() {
        let mut s = state();
        s.store = false;
        assert_eq!(kinds(&trace(req(1), s, |_, _| {})), [2, 63]);
    }
    #[test]
    fn collect_stopped_preserves_error() {
        assert_eq!(
            kinds(&trace(req(1), state(), |e, r| if e.kind == 3 {
                r.flag = false
            })),
            [2, 3, 63]
        );
    }
    #[test]
    fn collect_error_and_clear_order() {
        for failure in [0, 4, 10] {
            let t = trace(req(1), state(), |e, r| {
                if e.kind == failure {
                    r.ok = false
                }
            });
            let error = t.iter().find(|e| e.kind == 11).unwrap();
            assert_eq!(
                error.a,
                match failure {
                    4 => 1,
                    10 => 2,
                    _ => 0,
                }
            );
            if failure == 4 {
                assert!(!t.iter().any(|e| e.kind == 6 || e.kind == 10))
            } else {
                assert_eq!(kinds(&t), [2, 3, 4, 6, 10, 11, 63])
            }
        }
    }
    #[test]
    fn start_ignores_running_and_does_not_collect() {
        for store in [false, true] {
            for cancel in [false, true] {
                let t = trace(
                    req(2),
                    Snapshot {
                        store,
                        cancel,
                        matches: true,
                    },
                    |_, _| {},
                );
                assert_eq!(
                    kinds(&t),
                    if store && !cancel {
                        vec![2, 12, 13, 63]
                    } else {
                        vec![2, 63]
                    }
                )
            }
        }
    }
    #[test]
    fn stop_does_not_sample_or_wait() {
        for cancel in [false, true] {
            let mut s = state();
            s.cancel = cancel;
            assert_eq!(
                kinds(&trace(req(3), s, |_, _| {})),
                if cancel { vec![2, 14, 63] } else { vec![2, 63] }
            )
        }
    }
    #[test]
    fn loop_cancel_before_first_tick() {
        assert_eq!(
            kinds(&trace(req(4), state(), |e, r| if e.kind == 16 {
                r.flag = false
            })),
            [15, 16, 19, 63]
        );
    }
    #[test]
    fn stale_loop_unlocks_stops_without_closing_new_channel() {
        let mut s = state();
        s.matches = false;
        assert_eq!(kinds(&trace(req(4), s, |_, _| {})), [15, 16, 1, 18, 19, 63]);
    }
    #[test]
    fn loop_rechecks_identity_each_tick() {
        let mut waits = 0;
        let t = trace(req(4), state(), |e, r| {
            if e.kind == 16 {
                waits += 1;
                r.flag = waits < 3
            }
        });
        assert_eq!(
            kinds(&t),
            [15, 16, 1, 3, 4, 6, 10, 11, 18, 16, 1, 3, 4, 6, 10, 11, 18, 16, 19, 63]
        );
    }
    #[test]
    fn query_defaults_and_two_distinct_clock_ports() {
        let t = trace(req(5), state(), |_, _| {});
        assert_eq!(kinds(&t), [20, 21, 1, 6, 22, 63]);
        assert_eq!(t[0].text, b"day");
        assert_eq!(t[3].a, 1);
        assert_eq!(t.last().unwrap().a, 16);
    }
    #[test]
    fn query_failure_priority() {
        for (period, group, fail, code) in [
            ("bad", "bad", 20, 10),
            ("day", "bad", 20, 11),
            ("day", "bad", 0, 12),
            ("day", "app", 21, 13),
            ("day", "app", 22, 15),
        ] {
            let q = Request {
                op: 5,
                period: period.as_bytes().to_vec(),
                group: group.as_bytes().to_vec(),
                ..Request::default()
            };
            let t = trace(q, state(), |e, r| {
                if e.kind == fail {
                    r.ok = false
                }
            });
            assert_eq!(t.last().unwrap().a, code);
        }
    }
    #[test]
    fn query_nostore_avoids_second_clock_and_read() {
        let mut s = state();
        s.store = false;
        let t = trace(req(5), s, |_, _| {});
        assert_eq!(kinds(&t), [20, 21, 1, 63]);
        assert_eq!(t.last().unwrap().a, 14);
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
        assert_eq!(before, f.next());
        assert!(f
            .advance(Reply {
                kind: 4,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(before, f.next());
    }
    #[test]
    fn long_loop_has_no_total_step_limit() {
        let mut f = Flow::new(req(4)).unwrap();
        let mut steps = 0;
        let mut waits = 0;
        while f.next().kind != 0 {
            let e = f.next();
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: true,
                ..Reply::default()
            };
            if e.kind == 1 {
                r.snapshot = Some(state())
            }
            if e.kind == 16 {
                waits += 1;
                r.flag = waits <= 20000
            }
            f.advance(r).unwrap();
            steps += 1;
        }
        assert!(steps > 100000);
    }
}
