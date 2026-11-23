//! Kernel lifecycle decisions; all resources stay in the Go effect arena.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub mode: Vec<u8>,
    pub subscription: Vec<u8>,
    pub manual_count: i64,
    pub socks_port: i64,
    pub mixed_port: i64,
    pub ctrl_port: i64,
    pub kernel: bool,
    pub relay: bool,
    pub history: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Options {
    pub tun: bool,
    pub dns: bool,
    pub active: bool,
    pub china: bool,
    pub domestic: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
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
    pub options: Option<Options>,
}
// 1 Snapshot(borrowed),2 Running,3 TunAvailable,4 ProfileMatches,
// 5 FetchProfile(existing usecase),6 WriteManualProvider,7 AllocateMixed,
// 8 reserved,9 AllocateControlAndCommit,10 reserved,
// 11 ReadProfile,12 ConfigOptions(a profile token,b hasSubscription),
// 13 CommitChinaActive(a bool),14 NewRelay(a mixed port),15 WriteConfig,
// 16 NewRunner,17 RunnerStartAndPublish(success only),18 reserved,
// 19 WaitReady(a seconds),20 VerifyOwner(a mixedport),21 WaitNodes(a seconds),
// 22 VerifyPrivacy,23 StartHistory,24 TransferRelay(a hasLocal),25 MonitorLocalRelay,
// 26 CloseLocalRelay,27 CloseAppRelayAndClear,28 reserved,29 CollectHistory,
// 30 StopHistory,31 RunnerStopAndClearKernelActive,32 reserved,33 RemoveConfig,
// 34 OptionsRelay,35 GenerateConfig,
// 63 Result(a code,b originalErrorToken,text error),0 Done.
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    o: Options,
    e: Effect,
    stage: u8,
    local: bool,
    checker: bool,
    error: Vec<u8>,
    token: i64,
    code: i64,
    stop_for_failure: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 1 {
            return Err("invalid kernel operation");
        }
        Ok(Self {
            q,
            s: Snapshot::default(),
            o: Options::default(),
            e: Effect {
                kind: 1,
                ..Effect::default()
            },
            stage: 1,
            local: false,
            checker: false,
            error: vec![],
            token: 0,
            code: 0,
            stop_for_failure: false,
        })
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
    fn finish(&mut self) {
        if self.local {
            self.emit(26, 90);
        } else {
            self.result();
        }
    }
    fn result(&mut self) {
        self.emit(63, 99);
        self.e.a = self.code;
        self.e.b = self.token;
        self.e.text = self.error.clone();
    }
    fn fail(&mut self, code: i64, r: &Reply) {
        self.code = code;
        self.error = r.error.clone();
        self.token = r.token;
        self.finish();
    }
    fn fail_published(&mut self, code: i64, r: &Reply) {
        self.code = code;
        self.error = r.error.clone();
        self.token = r.token;
        self.stop_for_failure = true;
        self.begin_stop();
    }
    fn begin_stop(&mut self) {
        if self.s.relay {
            self.emit(27, 61);
        } else {
            self.after_relay();
        }
    }
    fn after_relay(&mut self) {
        if self.s.history {
            self.emit(29, 63);
        } else {
            self.after_history();
        }
    }
    fn after_history(&mut self) {
        if self.s.kernel {
            self.emit(31, 65);
        } else {
            self.finish();
        }
    }
    fn validate_source(&mut self) {
        if self.s.subscription.is_empty() && self.s.manual_count == 0 {
            self.code = 2;
            self.finish();
        } else if !self.s.subscription.is_empty() && self.s.subscription != b"__merged__" {
            self.emit(4, 4);
        } else {
            self.emit(6, 6);
        }
    }
    fn after_ready(&mut self) {
        if self.checker && self.s.socks_port != 0 {
            self.emit(20, 20);
            self.e.a = self.s.mixed_port;
        } else {
            self.emit(21, 21);
            self.e.a = 20;
        }
    }
    fn after_nodes(&mut self) {
        if self.o.tun && self.o.active {
            self.emit(22, 22);
        } else {
            self.emit(23, 23);
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected kernel reply");
        }
        if self.e.kind == 1 && r.snapshot.is_none() {
            return Err("missing kernel snapshot");
        }
        if self.e.kind == 12 && r.ok && r.options.is_none() {
            return Err("missing config options");
        }
        if let Some(s) = r.snapshot.clone() {
            self.s = s;
        }
        match self.stage {
            1 => {
                if self.q.op == 1 {
                    self.begin_stop();
                } else {
                    self.emit(2, 2);
                }
            }
            2 => {
                if r.flag {
                    self.finish();
                } else if self.s.mode != b"system-proxy" {
                    self.emit(3, 3);
                } else {
                    self.validate_source();
                }
            }
            3 => {
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.validate_source();
                }
            }
            4 => {
                if !r.flag {
                    self.emit(5, 5);
                } else {
                    self.emit(6, 6);
                }
            }
            5 => {
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.emit(6, 6);
                }
            }
            6 => {
                if !r.ok {
                    self.fail(3, &r);
                } else {
                    self.emit(7, 7);
                    self.e.a = self.s.socks_port;
                }
            }
            7 => {
                self.s.mixed_port = r.value;
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.emit(9, 9);
                }
            }
            9 => {
                self.s.ctrl_port = r.value;
                if !r.ok {
                    self.fail(4, &r);
                } else if self.s.ctrl_port == self.s.mixed_port {
                    self.emit(9, 9);
                } else {
                    self.emit(11, 11);
                }
            }
            11 => {
                if !r.ok && !self.s.subscription.is_empty() {
                    self.fail(5, &r);
                } else {
                    self.emit(12, 12);
                    self.e.a = r.token;
                    self.e.b = (!self.s.subscription.is_empty()) as i64;
                }
            }
            12 => {
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.o = r.options.unwrap();
                    self.emit(13, 13);
                    self.e.a =
                        (self.o.china && (!self.o.tun || !self.o.active || self.o.domestic)) as i64;
                }
            }
            13 => {
                if self.o.tun && self.o.dns {
                    self.emit(14, 14);
                    self.e.a = self.s.mixed_port;
                } else {
                    self.emit(35, 35);
                }
            }
            14 => {
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.local = true;
                    self.emit(34, 34);
                }
            }
            34 => self.emit(35, 35),
            35 => self.emit(15, 15),
            15 => {
                if !r.ok {
                    self.fail(6, &r);
                } else {
                    self.emit(16, 16);
                }
            }
            16 => {
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.checker = r.flag;
                    self.emit(17, 17);
                }
            }
            17 => {
                if !r.ok {
                    self.fail(1, &r);
                } else {
                    self.s.kernel = true;
                    self.emit(19, 19);
                    self.e.a = 10;
                }
            }
            18 => {
                self.emit(19, 19);
                self.e.a = 10;
            }
            19 => {
                if !r.ok {
                    self.fail_published(7, &r);
                } else {
                    self.after_ready();
                }
            }
            20 => {
                if !r.ok {
                    self.fail_published(1, &r);
                } else {
                    self.emit(21, 21);
                    self.e.a = 20;
                }
            }
            21 => {
                if !r.ok {
                    self.fail_published(8, &r);
                } else {
                    self.after_nodes();
                }
            }
            22 => {
                if !r.ok {
                    self.fail_published(1, &r);
                } else {
                    self.emit(23, 23);
                }
            }
            23 => {
                self.emit(24, 24);
                self.e.a = self.local as i64;
            }
            24 => {
                if self.local {
                    self.local = false;
                    self.emit(25, 25);
                } else {
                    self.finish();
                }
            }
            25 => self.finish(),
            61 => {
                self.s.relay = false;
                self.after_relay();
            }
            63 => self.emit(30, 64),
            64 => self.after_history(),
            65 => {
                if !self.stop_for_failure && !r.ok {
                    self.code = 1;
                    self.token = r.token;
                    self.error = r.error;
                }
                self.s.kernel = false;
                self.emit(33, 67);
            }
            67 => self.finish(),
            90 => {
                self.local = false;
                self.result();
            }
            99 => self.emit(0, 100),
            _ => return Err("invalid kernel stage"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn state() -> Snapshot {
        Snapshot {
            mode: b"system-proxy".to_vec(),
            subscription: b"__merged__".to_vec(),
            socks_port: 8888,
            ..Snapshot::default()
        }
    }
    fn run(op: u8, s: Snapshot, mut change: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(Request { op }).unwrap();
        let mut t = vec![];
        for _ in 0..150 {
            let e = f.next();
            if e.kind == 0 {
                return t;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                ..Reply::default()
            };
            match e.kind {
                1 => r.snapshot = Some(s.clone()),
                7 => r.value = 8888,
                9 => r.value = 9999,
                11 => r.token = 100,
                12 => r.options = Some(Options::default()),
                16 => r.flag = true,
                _ => {}
            }
            change(&e, &mut r);
            t.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop")
    }
    fn end(t: &[Effect]) -> &Effect {
        t.last().unwrap()
    }
    fn tun(e: &Effect, r: &mut Reply) {
        if e.kind == 12 {
            r.options = Some(Options {
                tun: true,
                dns: true,
                active: true,
                china: true,
                domestic: true,
            });
        }
    }
    #[test]
    fn complete_no_relay_start_order() {
        let t = run(0, state(), |_, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 2, 6, 7, 9, 11, 12, 13, 35, 15, 16, 17, 19, 20, 21, 23, 24, 63]
        );
        assert_eq!(end(&t).a, 0);
        assert_eq!(t.iter().find(|e| e.kind == 24).unwrap().a, 0);
    }
    #[test]
    fn full_tun_relay_transfers_before_monitor() {
        let mut s = state();
        s.mode = b"tun".to_vec();
        let t = run(0, s, tun);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![
                1, 2, 3, 6, 7, 9, 11, 12, 13, 14, 34, 35, 15, 16, 17, 19, 20, 21, 22, 23, 24, 25,
                63
            ]
        );
        assert!(!t.iter().any(|e| e.kind == 26));
        assert_eq!(t.iter().find(|e| e.kind == 13).unwrap().a, 1);
    }
    #[test]
    fn every_failure_cleanup_boundary() {
        for (kind, code) in [
            (3, 1),
            (5, 1),
            (6, 3),
            (7, 1),
            (9, 4),
            (11, 5),
            (12, 1),
            (14, 1),
            (15, 6),
            (16, 1),
            (17, 1),
            (19, 7),
            (20, 1),
            (21, 8),
            (22, 1),
        ] {
            let mut s = state();
            s.mode = b"tun".to_vec();
            s.subscription = b"https://sub".to_vec();
            let t = run(0, s, |e, r| {
                tun(e, r);
                if e.kind == kind {
                    r.ok = false;
                    r.error = b"original".to_vec();
                    r.token = 77;
                }
            });
            assert_eq!(end(&t).a, code, "kind {kind}");
            assert_eq!(end(&t).b, 77);
            let published = matches!(kind, 19..=22);
            assert_eq!(
                t.iter().filter(|e| e.kind == 31).count(),
                usize::from(published),
                "kind {kind}"
            );
            assert_eq!(
                t.iter().filter(|e| e.kind == 33).count(),
                usize::from(published)
            );
            assert_eq!(
                t.iter().filter(|e| e.kind == 26).count(),
                usize::from(matches!(kind, 15 | 16 | 17 | 19 | 20 | 21 | 22))
            );
        }
    }
    #[test]
    fn duplicate_control_ports_retry_after_each_assignment() {
        let mut calls = 0;
        let t = run(0, state(), |e, r| {
            if e.kind == 9 {
                calls += 1;
                r.value = if calls < 3 { 8888 } else { 7777 };
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 9).count(), 3);
        assert_eq!(end(&t).a, 0);
    }

    #[test]
    fn allocation_error_still_commits_partial_port() {
        for kind in [7, 9] {
            let t = run(0, state(), |e, r| {
                if e.kind == kind {
                    r.ok = false;
                    r.value = 4321;
                }
            });
            let i = t.iter().position(|e| e.kind == kind).unwrap();
            assert_eq!(t[i + 1].kind, 63);
            assert!(!t.iter().any(|e| e.kind == 11));
        }
    }
    #[test]
    fn nil_kernel_stop_closes_relay_history_but_keeps_config() {
        let mut s = state();
        s.relay = true;
        s.history = true;
        let t = run(1, s, |_, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 27, 29, 30, 63]
        );
    }
    #[test]
    fn stop_error_still_clears_kernel_and_config() {
        let mut s = state();
        s.kernel = true;
        s.relay = true;
        s.history = true;
        let t = run(1, s, |e, r| {
            if e.kind == 31 {
                r.ok = false;
                r.token = 9;
                r.error = b"stop".to_vec();
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 27, 29, 30, 31, 33, 63]
        );
        assert_eq!((end(&t).a, end(&t).b), (1, 9));
    }
    #[test]
    fn published_failure_keeps_original_over_cleanup_error() {
        let mut s = state();
        s.relay = true;
        s.history = true;
        let t = run(0, s, |e, r| {
            tun(e, r);
            if e.kind == 19 || e.kind == 31 {
                r.ok = false;
                r.token = if e.kind == 19 { 88 } else { 99 };
                r.error = if e.kind == 19 {
                    b"ready".to_vec()
                } else {
                    b"stop".to_vec()
                };
            }
        });
        assert_eq!((end(&t).a, end(&t).b), (7, 88));
        assert_eq!(end(&t).text, b"ready");
        let kinds = t.iter().map(|e| e.kind).collect::<Vec<_>>();
        let at = kinds.iter().position(|k| *k == 19).unwrap();
        assert_eq!(&kinds[at + 1..], &[27, 29, 30, 31, 33, 26, 63]);
    }
    #[test]
    fn running_shortcut_and_manual_read_error() {
        let t = run(0, state(), |e, r| {
            if e.kind == 2 {
                r.flag = true
            }
        });
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 2, 63]);
        let mut s = state();
        s.subscription.clear();
        s.manual_count = 1;
        let t = run(0, s, |e, r| {
            if e.kind == 11 {
                r.ok = false;
                r.token = 123;
            }
        });
        assert_eq!(t.iter().find(|e| e.kind == 12).unwrap().a, 123);
        assert_eq!(end(&t).a, 0);
    }
    #[test]
    fn source_presence_and_owner_privacy_gates() {
        let mut s = state();
        s.subscription.clear();
        let t = run(0, s, |_, _| {});
        assert_eq!(end(&t).a, 2);
        let mut s = state();
        s.socks_port = 0;
        let t = run(0, s, |e, r| {
            if e.kind == 12 {
                r.options = Some(Options {
                    tun: true,
                    active: false,
                    dns: false,
                    ..Options::default()
                });
            }
        });
        assert!(!t.iter().any(|e| e.kind == 20 || e.kind == 22));
    }
    #[test]
    fn invalid_reply_leaves_stage_and_ownership_unchanged() {
        let mut f = Flow::new(Request::default()).unwrap();
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
                kind: 99,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(before, f.next());
    }
}
