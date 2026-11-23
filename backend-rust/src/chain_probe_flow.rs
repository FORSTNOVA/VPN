//! Chain-probe orchestration. Graphs, context deadlines, adapters and HTTP bodies stay in Go.
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
}
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub subscription: bool,
    pub tun: bool,
    pub strict: bool,
    pub store: bool,
    pub proxies: i64,
    pub urls: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub bits: i64,
    pub value: i64,
    pub token: i64,
    pub error: Vec<u8>,
    pub snapshot: Option<Snapshot>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub c: i64,
    pub text: Vec<u8>,
    pub indices: Vec<i64>,
}
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    index_error: bool,
    index: i64,
    records: i64,
    kept: Vec<i64>,
    adapters: i64,
    resolver: bool,
    transport: bool,
    parent: bool,
    cleaning: bool,
    phase: u8,
    stage_next: u8,
    nodebits: i64,
    cold: i64,
    warm: i64,
    elapsed: i64,
    reused: bool,
    httpstatus: i64,
    code: i64,
    token: i64,
    error: Vec<u8>,
    status: i64,
    observation: i64,
    policy: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 4 {
            return Err("invalid chain-probe operation");
        }
        let k = match q.op {
            0 => 1,
            1 => 29,
            _ => 3,
        };
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            index_error: false,
            index: 0,
            records: 0,
            kept: vec![],
            adapters: 0,
            resolver: false,
            transport: false,
            parent: false,
            cleaning: false,
            phase: 0,
            stage_next: 0,
            nodebits: 0,
            cold: 0,
            warm: 0,
            elapsed: 0,
            reused: false,
            httpstatus: 0,
            code: 0,
            token: 0,
            error: vec![],
            status: 0,
            observation: 0,
            policy: 0,
        };
        f.emit(k);
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
    fn stage(&mut self, code: i64, next: u8) {
        self.stage_next = next;
        self.emit(12);
        self.e.a = code
    }
    fn raw(&mut self, r: &Reply) {
        self.code = 1;
        self.token = r.token;
        self.error = r.error.clone()
    }
    fn result_frame(&mut self, k: u8) {
        self.emit(k);
        self.e.a = self.cold;
        self.e.b = self.warm;
        self.e.c = self.code;
        self.e.text = self.error.clone();
        self.e.indices = vec![
            self.status,
            self.token,
            self.observation,
            self.policy,
            self.httpstatus,
        ];
        self.e.indices.extend_from_slice(&self.kept)
    }
    fn finish(&mut self) {
        if self.q.op <= 1 {
            self.result_frame(36)
        } else {
            self.result_frame(63)
        }
    }
    fn http_error(&mut self, status: i64, code: i64) {
        self.status = status;
        self.code = code;
        self.finish()
    }
    fn measured(&mut self) {
        if self.q.op == 0 {
            self.result_frame(28);
        } else {
            if self.q.op == 3 && self.cold > 0 {
                self.code = 0;
                self.token = 0;
                self.error.clear()
            }
            self.finish()
        }
    }
    fn cleanup(&mut self) {
        self.cleaning = true;
        if self.transport {
            self.transport = false;
            self.emit(25)
        } else if self.adapters > 0 {
            self.adapters -= 1;
            self.emit(26);
            self.e.a = self.adapters
        } else if self.resolver {
            self.resolver = false;
            self.emit(27)
        } else {
            self.cleaning = false;
            self.measured()
        }
    }
    fn measure(&mut self) {
        self.phase = 0;
        self.index = 0;
        self.emit(11);
        self.e.a = 0
    }
    fn node(&mut self) {
        if self.index < self.s.proxies {
            self.emit(13);
            self.e.a = self.index
        } else if self.adapters == 0 {
            self.code = 5;
            self.cleanup()
        } else {
            self.emit(19)
        }
    }
    fn request(&mut self) {
        self.stage(5, 20);
    }
    fn request_done(&mut self) {
        self.emit(24);
        self.e.a = self.phase as i64
    }
    fn record_next(&mut self) {
        if self.index < self.records {
            self.emit(34);
            self.e.a = self.index
        } else {
            self.status = 200;
            self.finish()
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        let k = self.e.kind;
        if k == 0 || r.kind != k {
            return Err("unexpected chain-probe reply");
        }
        if matches!(k, 1 | 3 | 5) && r.snapshot.is_none() {
            return Err("missing chain-probe snapshot");
        }
        if let Some(s) = &r.snapshot {
            if s.proxies < 0 || s.proxies > 100000 {
                return Err("invalid proxy count");
            }
        }
        if k == 33 && r.ok && (r.value < 0 || r.value > 100000) {
            return Err("invalid record count");
        }
        if let Some(s) = r.snapshot.clone() {
            if k == 7 {
                self.s.proxies = s.proxies;
            } else {
                self.s = s
            }
        }
        match k {
            1 => self.emit(4),
            2 => {
                if self.index_error {
                    self.http_error(400, 1)
                } else if self.q.op == 1 {
                    self.emit(33)
                } else {
                    self.emit(7)
                }
            }
            3 => {
                if self.q.op == 4 {
                    self.request()
                } else {
                    self.measure()
                }
            }
            4 => {
                self.index_error = !r.ok;
                if !r.ok {
                    self.raw(&r)
                }
                if self.q.op == 0 {
                    self.emit(5)
                } else {
                    self.emit(2)
                }
            }
            5 => {
                if self.s.subscription {
                    self.emit(6)
                } else {
                    self.emit(2)
                }
            }
            6 => self.emit(2),
            7 => {
                if self.q.op == 1 {
                    if r.ok {
                        self.emit(8);
                        self.e.a = self.index;
                        self.e.b = 1
                    } else {
                        self.index += 1;
                        self.record_next()
                    }
                } else if r.ok {
                    self.emit(8)
                } else {
                    self.raw(&r);
                    self.http_error(400, 1)
                }
            }
            8 => {
                if self.q.op == 1 {
                    if r.flag {
                        self.kept.push(self.index)
                    }
                    self.index += 1;
                    self.record_next()
                } else {
                    self.emit(9)
                }
            }
            9 => {
                self.parent = true;
                self.emit(10)
            }
            10 => {
                self.policy = if r.bits & 1 != 0 {
                    0
                } else if self.s.tun || r.bits & 2 != 0 {
                    if self.s.strict {
                        2
                    } else {
                        1
                    }
                } else {
                    0
                };
                self.s.urls = r.bits & 1 != 0 || self.policy != 0;
                self.emit(38);
                self.e.a = self.policy
            }
            38 => self.measure(),
            11 => match self.e.a {
                0 => self.stage(0, 13),
                1 => self.request(),
                2 => self.emit(30),
                _ => return Err("invalid clock purpose"),
            },
            12 => {
                let next = self.stage_next;
                self.emit(next);
                if next == 13 {
                    self.node()
                } else if next == 15 {
                    self.e.a = if self.nodebits & (1 << 10) != 0 {
                        0
                    } else if self.nodebits & (1 << 11) != 0 {
                        1
                    } else if self.nodebits & (1 << 12) != 0 {
                        2
                    } else {
                        3
                    }
                } else if next == 20 || next == 24 {
                    self.e.a = self.phase as i64
                } else if next == 25 {
                    self.cleanup()
                }
            }
            13 => {
                self.nodebits = r.bits;
                if self.index == 0 {
                    self.emit(14)
                } else {
                    self.emit(18);
                    self.e.a = self.index
                }
            }
            14 => {
                if !r.ok {
                    self.raw(&r);
                    self.stage(1, 25)
                } else {
                    self.resolver = r.flag;
                    if self.resolver
                        && self.s.urls
                        && self.nodebits & (1 << 7) != 0
                        && self.nodebits & (1 << 8) != 0
                        && self.nodebits & (1 << 9) == 0
                    {
                        self.stage(2, 15)
                    } else {
                        self.emit(17);
                        self.e.a = self.nodebits & 127
                    }
                }
            }
            15 => {
                if r.ok {
                    self.emit(16)
                } else {
                    self.code = 3;
                    self.cleanup()
                }
            }
            16 => {
                self.emit(17);
                self.e.a = self.nodebits & 127
            }
            17 => {
                self.emit(18);
                self.e.a = self.index
            }
            18 => {
                if r.ok {
                    self.adapters += 1;
                    self.index += 1;
                    self.node()
                } else {
                    self.code = 4;
                    self.cleanup()
                }
            }
            19 => {
                self.transport = true;
                self.request()
            }
            20 => {
                if r.ok {
                    self.emit(21);
                    self.e.a = self.phase as i64
                } else {
                    self.raw(&r);
                    self.request_done()
                }
            }
            21 => {
                if !r.ok {
                    self.code = if r.bits & 1 != 0 { 6 } else { 7 };
                    self.request_done()
                } else {
                    self.httpstatus = r.value;
                    self.reused = r.bits & 2 != 0;
                    self.emit(22);
                    self.e.a = self.phase as i64
                }
            }
            22 => {
                self.elapsed = r.value.max(1);
                self.emit(23)
            }
            23 => {
                if self.httpstatus != 204 {
                    self.code = 8;
                    self.stage(3, 24)
                } else {
                    if self.phase == 0 {
                        self.cold = self.elapsed
                    } else {
                        self.warm = self.elapsed
                    }
                    self.request_done()
                }
            }
            24 => {
                if self.code != 0 {
                    if self.phase == 1 {
                        self.warm = 0
                    }
                    if self.q.op == 4 {
                        self.measured()
                    } else {
                        self.cleanup()
                    }
                } else if self.phase == 0 {
                    self.phase = 1;
                    self.emit(11);
                    self.e.a = 1
                } else if !self.reused {
                    self.warm = 0;
                    self.code = 9;
                    self.stage(4, 25)
                } else if self.q.op == 4 {
                    self.measured()
                } else {
                    self.cleanup()
                }
            }
            25..=27 => self.cleanup(),
            28 => {
                self.observation = if r.bits & 1 != 0 {
                    1
                } else if r.bits & 2 != 0 {
                    2
                } else {
                    0
                };
                self.observation |= r.bits & 12;
                self.emit(11);
                self.e.a = 2
            }
            29 => {
                if !r.flag {
                    self.http_error(503, 10)
                } else if self.q.op == 1 {
                    self.emit(1)
                } else {
                    self.result_frame(31)
                }
            }
            30 => self.emit(29),
            31 => self.emit(32),
            32 => {
                if r.ok {
                    self.status = 200;
                    self.finish()
                } else {
                    self.http_error(500, 11)
                }
            }
            33 => {
                if r.ok {
                    self.records = r.value;
                    self.index = 0;
                    self.record_next()
                } else {
                    self.http_error(500, 12)
                }
            }
            34 => {
                if r.ok {
                    self.emit(35);
                    self.e.a = self.index
                } else {
                    self.http_error(500, 13)
                }
            }
            35 => {
                if r.ok {
                    self.emit(7);
                    self.e.a = self.index;
                    self.e.b = 1
                } else {
                    self.index += 1;
                    self.record_next()
                }
            }
            36 => {
                if self.parent {
                    self.emit(37)
                } else {
                    self.result_frame(63)
                }
            }
            37 => {
                self.parent = false;
                self.result_frame(63)
            }
            63 => self.emit(0),
            _ => return Err("invalid chain-probe effect"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(op: u8, mut change: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(Request { op }).unwrap();
        let mut out = vec![];
        for _ in 0..200 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: true,
                bits: 2,
                value: if e.kind == 21 {
                    204
                } else if e.kind == 33 {
                    2
                } else {
                    5
                },
                snapshot: if matches!(e.kind, 1 | 3 | 5 | 7) {
                    Some(Snapshot {
                        store: true,
                        proxies: 2,
                        ..Snapshot::default()
                    })
                } else {
                    None
                },
                ..Reply::default()
            };
            change(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap()
        }
        panic!("loop")
    }
    fn ks(v: &[Effect]) -> Vec<u8> {
        v.iter().map(|e| e.kind).collect()
    }
    #[test]
    fn full_probe_fingerprint_precedes_mutation_and_cleanup() {
        let t = run(0, |_, _| {});
        let k = ks(&t);
        assert!(k.iter().position(|&x| x == 8) < k.iter().position(|&x| x == 13));
        let clean: Vec<_> = t
            .iter()
            .filter(|e| matches!(e.kind, 25..=27))
            .map(|e| (e.kind, e.a))
            .collect();
        assert_eq!(clean, [(25, 0), (26, 1), (26, 0), (27, 0)]);
        assert_eq!(&k[k.len() - 3..], &[36, 37, 63]);
        let r = t.last().unwrap();
        assert_eq!((r.a, r.b, r.c), (5, 5, 0));
    }
    #[test]
    fn index_error_still_mode_policy_then_unlock() {
        let t = run(0, |e, r| {
            if let Some(s) = &mut r.snapshot {
                s.subscription = true
            }
            if e.kind == 4 {
                r.ok = false;
                r.token = 7
            }
        });
        assert_eq!(ks(&t), [1, 4, 5, 6, 2, 36, 63]);
        assert_eq!(t.last().unwrap().indices[0], 400);
    }
    #[test]
    fn compile_error_no_parent_or_measure() {
        let t = run(0, |e, r| {
            if e.kind == 7 {
                r.ok = false;
                r.token = 9
            }
        });
        assert_eq!(ks(&t), [1, 4, 5, 2, 7, 36, 63]);
    }
    #[test]
    fn dns_policy_selection() {
        for (tun, strict, bits, want) in [
            (false, false, 0, 0),
            (true, false, 0, 1),
            (true, true, 0, 2),
            (false, true, 2, 2),
            (true, true, 1, 0),
        ] {
            let t = run(0, |e, r| {
                if let Some(s) = &mut r.snapshot {
                    s.tun = tun;
                    s.strict = strict
                }
                if e.kind == 10 {
                    r.bits = bits
                }
            });
            assert_eq!(t.iter().find(|e| e.kind == 38).unwrap().a, want);
        }
    }
    #[test]
    fn cold_failure_no_warm_and_lifo_cleanup() {
        let t = run(2, |e, r| {
            if e.kind == 21 {
                r.ok = false;
                r.bits = 1
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 20).count(), 1);
        let result = t.last().unwrap();
        assert_eq!((result.a, result.b, result.c), (0, 0, 6));
        assert!(ks(&t).contains(&25));
    }
    #[test]
    fn cancellation_is_not_timeout() {
        let t = run(4, |e, r| {
            if e.kind == 21 {
                r.ok = false;
                r.bits = 0
            }
        });
        assert_eq!(t.last().unwrap().c, 7);
    }
    #[test]
    fn bodyclose_before_status_validation_minimum_and_warm_reuse() {
        let t = run(4, |e, r| {
            if e.kind == 22 {
                r.value = -10
            }
        });
        assert_eq!((t.last().unwrap().a, t.last().unwrap().b), (1, 1));
        let t = run(4, |e, r| {
            if e.kind == 21 {
                r.value = 200
            }
        });
        assert_eq!(ks(&t), [3, 12, 20, 21, 22, 23, 12, 24, 63]);
        assert_eq!(t.last().unwrap().c, 8);
    }
    #[test]
    fn warm_failure_preserves_cold_and_prefix_suppresses_error() {
        for op in [2, 3, 4] {
            let t = run(op, |e, r| {
                if e.kind == 21 && e.a == 1 {
                    r.ok = false
                }
            });
            assert_eq!(t.last().unwrap().a, 5);
            assert_eq!(t.last().unwrap().b, 0);
            assert_eq!(t.last().unwrap().c, if op == 3 { 0 } else { 7 });
        }
    }
    #[test]
    fn warm_not_reused_special_failure() {
        let t = run(2, |e, r| {
            if e.kind == 21 && e.a == 1 {
                r.bits = 0
            }
        });
        let x = t.last().unwrap();
        assert_eq!((x.a, x.b, x.c), (5, 0, 9));
        assert!(t.iter().any(|e| e.kind == 12 && e.a == 4));
    }
    #[test]
    fn resolver_failure_and_adapter_failure_partial_cleanup() {
        let t = run(2, |e, r| {
            if e.kind == 14 {
                r.ok = false;
                r.token = 17
            }
        });
        assert!(!ks(&t).contains(&27));
        assert!(t.iter().any(|e| e.kind == 12 && e.a == 1));
        let t = run(2, |e, r| {
            if e.kind == 18 && e.a == 1 {
                r.ok = false
            }
        });
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 26)
                .map(|e| e.a)
                .collect::<Vec<_>>(),
            [0]
        );
        assert!(ks(&t).contains(&27));
        assert!(!ks(&t).contains(&25));
    }
    #[test]
    fn ech_gate_and_name_priority() {
        for presence in [0, 1 << 12, 1 << 11, 1 << 10] {
            let t = run(2, |e, r| {
                if let Some(s) = &mut r.snapshot {
                    s.urls = true
                }
                if e.kind == 13 {
                    r.bits = (1 << 7) | (1 << 8) | presence
                }
            });
            let ech = t.iter().find(|e| e.kind == 15).unwrap();
            assert_eq!(
                ech.a,
                match presence {
                    0 => 3,
                    4096 => 2,
                    2048 => 1,
                    _ => 0,
                }
            );
        }
        let t = run(2, |e, r| {
            if let Some(s) = &mut r.snapshot {
                s.urls = true
            }
            if e.kind == 13 {
                r.bits = (1 << 7) | (1 << 8) | (1 << 9)
            }
        });
        assert!(!ks(&t).contains(&15));
    }
    #[test]
    fn no_nodes_no_transport() {
        let t = run(2, |_, r| {
            if let Some(s) = &mut r.snapshot {
                s.proxies = 0
            }
        });
        assert_eq!(ks(&t), [3, 11, 12, 63]);
        assert_eq!(t.last().unwrap().c, 5);
    }
    #[test]
    fn list_bad_details_aborts_bad_path_and_hash_skip() {
        let t = run(1, |e, r| {
            if e.kind == 34 {
                r.ok = false
            }
        });
        assert_eq!(t.last().unwrap().c, 13);
        let t = run(1, |e, r| {
            if e.kind == 35 && e.a == 0 {
                r.ok = false
            }
        });
        assert_eq!(&t.last().unwrap().indices[5..], &[1]);
        let t = run(1, |e, r| {
            if e.kind == 8 {
                r.flag = false
            }
        });
        assert_eq!(t.last().unwrap().indices.len(), 5);
    }
    #[test]
    fn request_prepare_error_cancels_without_do() {
        let t = run(4, |e, r| {
            if e.kind == 20 {
                r.ok = false;
                r.token = 44
            }
        });
        assert_eq!(ks(&t), [3, 12, 20, 24, 63]);
        assert_eq!(t.last().unwrap().indices[1], 44);
    }
    #[test]
    fn probe_reports_after_measure_even_on_error() {
        let t = run(0, |e, r| {
            if e.kind == 21 {
                r.ok = false
            }
        });
        assert!(ks(&t).contains(&28));
        assert!(ks(&t).contains(&32));
        assert_eq!(t.last().unwrap().indices[0], 200);
    }
    #[test]
    fn invalid_reply_does_not_advance() {
        let mut f = Flow::new(Request { op: 0 }).unwrap();
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
                kind: 1,
                snapshot: Some(Snapshot {
                    proxies: -1,
                    ..Snapshot::default()
                }),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), e);
    }
    #[test]
    fn compile_snapshot_does_not_overwrite_mode_facts() {
        let t = run(0, |e, r| {
            if e.kind == 5 {
                let s = r.snapshot.as_mut().unwrap();
                s.tun = true;
                s.strict = true;
            }
            if e.kind == 10 {
                r.bits = 0;
            }
        });
        assert_eq!(t.iter().find(|e| e.kind == 38).unwrap().a, 2);
        let encoded = t.iter().find(|e| e.kind == 31).unwrap();
        assert_eq!((encoded.a, encoded.b, encoded.c), (5, 5, 0));
    }
}
