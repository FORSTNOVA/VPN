//! Complete region verification job continuation. No generation/cancellation
//! policy is added; each node is selected across the intervening unlock.
use std::collections::BTreeMap;
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub chain: bool,
    pub subscription: bool,
    pub job_exists: bool,
    pub job_running: bool,
    pub store: bool,
    pub sources: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub refresh: bool,
    pub has_error: bool,
    pub error: Vec<u8>,
    pub error_token: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
    pub extra: Vec<u8>,
    pub names: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default)]
pub struct Delay {
    pub name: Vec<u8>,
    pub value: i64,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub verified: bool,
    pub value: i64,
    pub ttl: i64,
    pub token: i64,
    pub name: Vec<u8>,
    pub country: Vec<u8>,
    pub error: Vec<u8>,
    pub names: Vec<Vec<u8>>,
    pub delays: Vec<Delay>,
    pub snapshot: Option<Snapshot>,
}
// 1Lock,2Unlock,3SnapshotBorrowed,4Now(a1start/2record/3finish),5CreateJob,
// 6SnapshotResponse(while locked),7Spawn(a refresh),8Running,9StartKernel,
// 10StopKernel,11Get(data group),12List,13Delays(a8s),14SetTotal(a),
// 15LookupRegion(data name),16RegionAge(data; replyvalue duration,ttl),
// 17Put(data node),18QueryGeo(a index,b8s,data sourceName),
// 19SaveRegionMemory(a status0unreachable1single2conflict3verified,bdetail1delay2put3zero4single5conflict6verified,data node,text country,extra IP,names sources),
// 20StoreRegion,21Log(a1save2restore,data node,text err),
// 22Increment(a0skip1failed2verified; plusDone),23FinishJob(a hasError,b errtoken,text err),
// 63Result(aHTTP,b0startsuccess1chains2nosub3running4run/finishdone),0Done.
#[derive(Clone, Debug, Default)]
struct Probe {
    status: i64,
    detail: i64,
    ip: Vec<u8>,
    country: Vec<u8>,
    sources: Vec<Vec<u8>>,
}
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    stage: u8,
    nodes: Vec<Vec<u8>>,
    delays: BTreeMap<Vec<u8>, i64>,
    index: usize,
    source: usize,
    sources: Vec<Vec<u8>>,
    answers: Vec<Probe>,
    probe: Probe,
    original: Vec<u8>,
    temporary: bool,
    finish_error: bool,
    error: Vec<u8>,
    error_token: i64,
    unlock_after: u8,
    counter: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 2 {
            return Err("unknown region job op");
        }
        let kind = if q.op == 2 { 3 } else { 1 };
        Ok(Self {
            finish_error: q.op == 2 && q.has_error,
            error: if q.op == 2 && q.has_error {
                q.error.clone()
            } else {
                vec![]
            },
            error_token: if q.op == 2 && q.has_error {
                q.error_token
            } else {
                0
            },
            q,
            s: Snapshot::default(),
            e: Effect {
                kind,
                ..Effect::default()
            },
            stage: 1,
            nodes: vec![],
            delays: BTreeMap::new(),
            index: 0,
            source: 0,
            sources: vec![],
            answers: vec![],
            probe: Probe::default(),
            original: vec![],
            temporary: false,
            unlock_after: 0,
            counter: 0,
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
    fn result(&mut self, status: i64, code: i64) {
        self.emit(63, 99);
        self.e.a = status;
        self.e.b = code;
    }
    fn unlock(&mut self, after: u8) {
        self.unlock_after = after;
        self.emit(2, 90);
    }
    fn finish(&mut self) {
        if !self.s.job_exists {
            self.finish_done();
        } else {
            self.emit(4, 70);
            self.e.a = 3;
        }
    }
    fn finish_done(&mut self) {
        if self.q.op == 2 {
            self.result(0, 4);
        } else {
            self.unlock(4);
        }
    }
    fn fail_finish(&mut self, r: &Reply) {
        self.finish_error = true;
        self.error = r.error.clone();
        self.error_token = r.token;
        self.finish();
    }
    fn node(&self) -> Vec<u8> {
        self.nodes[self.index].clone()
    }
    fn next_node(&mut self) {
        if self.index >= self.nodes.len() {
            self.emit(1, 60);
        } else {
            self.emit(1, 20);
        }
    }
    fn delay_or_switch(&mut self) {
        if self.delays.get(&self.node()).copied().unwrap_or(0) <= 0 {
            self.probe = Probe {
                detail: 1,
                ..Probe::default()
            };
            self.save();
        } else {
            self.emit(17, 25);
            self.e.data = self.node();
        }
    }
    fn save(&mut self) {
        self.emit(4, 40);
        self.e.a = 2;
    }
    fn query(&mut self) {
        if self.answers.len() == 2 || self.source >= self.sources.len() {
            self.combine();
            self.save();
        } else {
            self.emit(18, 30);
            self.e.a = self.source as i64;
            self.e.b = 8;
            self.e.data = self.sources[self.source].clone();
        }
    }
    fn combine(&mut self) {
        self.probe = match self.answers.len() {
            0 => Probe {
                detail: 3,
                ..Probe::default()
            },
            1 => Probe {
                status: 1,
                detail: 4,
                ..self.answers[0].clone()
            },
            _ => {
                let mut p = self.answers[0].clone();
                p.sources = self
                    .answers
                    .iter()
                    .flat_map(|a| a.sources.clone())
                    .collect();
                let same = self.answers[0].country == self.answers[1].country;
                p.status = if same { 3 } else { 2 };
                p.detail = if same { 6 } else { 5 };
                p
            }
        };
    }
    fn final_stop(&mut self) {
        if self.temporary {
            self.emit(10, 62);
        } else {
            self.finish();
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected region job reply");
        }
        if matches!(self.e.kind, 1 | 3) && r.snapshot.is_none() {
            return Err("missing region job lock snapshot");
        }
        if let Some(s) = r.snapshot.clone() {
            self.s = s;
        }
        match self.stage {
            1 => match self.q.op {
                0 => {
                    if self.s.chain {
                        self.unlock(1);
                    } else if !self.s.subscription {
                        self.unlock(2);
                    } else if self.s.job_running {
                        self.unlock(3);
                    } else {
                        self.emit(4, 2);
                        self.e.a = 1;
                    }
                }
                1 => {
                    if !self.s.job_running {
                        self.unlock(4);
                    } else {
                        self.emit(8, 10);
                    }
                }
                2 => self.finish(),
                _ => unreachable!(),
            },
            2 => self.emit(5, 3),
            3 => self.emit(6, 4),
            4 => self.unlock(5),
            5 => self.result(200, 0),
            10 => {
                self.temporary = !r.flag;
                if self.temporary {
                    self.emit(9, 11);
                } else {
                    self.emit(11, 12);
                    self.e.data = b"SmartVPN".to_vec();
                }
            }
            11 => {
                if !r.ok {
                    self.fail_finish(&r);
                } else {
                    self.emit(11, 12);
                    self.e.data = b"SmartVPN".to_vec();
                }
            }
            12 => {
                self.original = if r.ok { r.name } else { vec![] };
                if self.original == b"SmartVPNAuto" || self.original == b"SmartVPNFallback" {
                    self.emit(11, 13);
                    self.e.data = self.original.clone();
                } else {
                    self.emit(12, 14);
                }
            }
            13 => {
                if r.ok {
                    self.original = r.name;
                }
                self.emit(12, 14);
            }
            14 => {
                if !r.ok {
                    self.finish_error = true;
                    self.error = r.error;
                    self.error_token = r.token;
                    if self.temporary {
                        self.emit(10, 15);
                    } else {
                        self.finish();
                    }
                } else {
                    self.nodes = r.names;
                    self.emit(13, 16);
                    self.e.a = 8;
                }
            }
            15 => self.finish(),
            16 => {
                self.delays = r.delays.into_iter().map(|d| (d.name, d.value)).collect();
                self.emit(14, 17);
                self.e.a = self.nodes.len() as i64;
            }
            17 => self.unlock(6),
            20 => {
                if !self.s.job_running {
                    self.unlock(7);
                } else if !self.q.refresh {
                    self.emit(15, 21);
                    self.e.data = self.node();
                } else {
                    self.delay_or_switch();
                }
            }
            21 => {
                if r.flag && r.verified {
                    self.emit(16, 22);
                    self.e.data = self.node();
                } else {
                    self.delay_or_switch();
                }
            }
            22 => {
                if r.value < r.ttl {
                    self.counter = 0;
                    self.emit(22, 44);
                    self.e.a = 0;
                } else {
                    self.delay_or_switch();
                }
            }
            25 => {
                if !r.ok {
                    self.probe = Probe {
                        detail: 2,
                        ..Probe::default()
                    };
                    self.save();
                } else {
                    self.source = 0;
                    self.sources = self.s.sources.clone();
                    self.answers.clear();
                    self.query();
                }
            }
            30 => {
                if r.ok {
                    self.answers.push(Probe {
                        ip: r.name,
                        country: r.country,
                        sources: vec![self.sources[self.source].clone()],
                        ..Probe::default()
                    });
                }
                self.source += 1;
                self.query();
            }
            40 => {
                self.emit(19, 41);
                self.e.a = self.probe.status;
                self.e.b = self.probe.detail;
                self.e.data = self.node();
                self.e.text = self.probe.country.clone();
                self.e.extra = self.probe.ip.clone();
                self.e.names = self.probe.sources.clone();
            }
            41 => {
                if self.s.store {
                    self.emit(20, 42);
                } else {
                    self.counter = if self.probe.status == 3 { 2 } else { 1 };
                    self.emit(22, 44);
                    self.e.a = self.counter;
                }
            }
            42 => {
                if !r.ok {
                    self.emit(21, 43);
                    self.e.a = 1;
                    self.e.data = self.node();
                    self.e.text = r.error;
                } else {
                    self.counter = if self.probe.status == 3 { 2 } else { 1 };
                    self.emit(22, 44);
                    self.e.a = self.counter;
                }
            }
            43 => {
                self.counter = if self.probe.status == 3 { 2 } else { 1 };
                self.emit(22, 44);
                self.e.a = self.counter;
            }
            44 => {
                self.index += 1;
                self.unlock(6);
            }
            60 => {
                if !self.original.is_empty() && self.original != b"REJECT" {
                    self.emit(17, 61);
                    self.e.data = self.original.clone();
                } else {
                    self.final_stop();
                }
            }
            61 => {
                if !r.ok {
                    self.emit(21, 63);
                    self.e.a = 2;
                    self.e.text = r.error;
                } else {
                    self.final_stop();
                }
            }
            62 => self.finish(),
            63 => self.final_stop(),
            70 => {
                self.emit(23, 71);
                self.e.a = self.finish_error as i64;
                self.e.b = self.error_token;
                self.e.text = self.error.clone();
            }
            71 => self.finish_done(),
            90 => match self.unlock_after {
                1 => self.result(409, 1),
                2 => self.result(400, 2),
                3 => self.result(409, 3),
                4 => self.result(0, 4),
                5 => {
                    self.emit(7, 5);
                    self.e.a = self.q.refresh as i64;
                }
                6 => self.next_node(),
                7 => self.emit(1, 60),
                _ => unreachable!(),
            },
            99 => self.emit(0, 100),
            _ => return Err("invalid region job stage"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn snap() -> Snapshot {
        Snapshot {
            subscription: true,
            job_exists: true,
            job_running: true,
            sources: vec![b"s1".to_vec(), b"s2".to_vec(), b"s3".to_vec()],
            ..Snapshot::default()
        }
    }
    fn reply(e: &Effect) -> Reply {
        let mut r = Reply {
            kind: e.kind,
            ok: true,
            ..Reply::default()
        };
        match e.kind {
            1 | 3 => r.snapshot = Some(snap()),
            8 => r.flag = true,
            11 => r.name = b"original".to_vec(),
            12 => r.names = vec![b"a".to_vec(), b"b".to_vec()],
            13 => {
                r.delays = vec![
                    Delay {
                        name: b"a".to_vec(),
                        value: 10,
                    },
                    Delay {
                        name: b"b".to_vec(),
                        value: 20,
                    },
                ]
            }
            16 => {
                r.value = 0;
                r.ttl = 10;
            }
            18 => {
                r.name = b"1.2.3.4".to_vec();
                r.country = b"US".to_vec();
            }
            _ => {}
        }
        r
    }
    fn run(q: Request, mut alter: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(q).unwrap();
        let mut out = vec![];
        for _ in 0..200 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = reply(&e);
            alter(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop")
    }
    fn worker() -> Request {
        Request {
            op: 1,
            ..Request::default()
        }
    }
    #[test]
    fn start_snapshot_is_captured_before_unlock_and_spawn() {
        let t = run(Request::default(), |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().job_running = false;
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 4, 5, 6, 2, 7, 63]
        );
    }
    #[test]
    fn start_validation_priority_and_manual_is_irrelevant() {
        for (chain, sub, running, code) in [
            (true, false, true, 1),
            (false, false, true, 2),
            (false, true, true, 3),
        ] {
            let t = run(Request::default(), |e, r| {
                if e.kind == 1 {
                    let s = r.snapshot.as_mut().unwrap();
                    s.chain = chain;
                    s.subscription = sub;
                    s.job_running = running;
                }
            });
            assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 2, 63]);
            assert_eq!(t.last().unwrap().b, code);
        }
    }
    #[test]
    fn all_nodes_then_single_original_restore_across_unlocks() {
        let t = run(worker(), |_, _| {});
        let puts: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 17)
            .map(|e| e.data.clone())
            .collect();
        assert_eq!(
            puts,
            vec![b"a".to_vec(), b"b".to_vec(), b"original".to_vec()]
        );
        assert_eq!(t.iter().filter(|e| e.kind == 18).count(), 4);
        assert_eq!(t.iter().filter(|e| e.kind == 19 && e.a == 3).count(), 2);
        assert!(t.iter().any(|e| e.kind == 23 && e.a == 0));
        assert!(!t.iter().any(|e| e.kind == 10));
        let first = t.iter().position(|e| e.kind == 17).unwrap();
        assert!(t[first + 1..].iter().any(|e| e.kind == 2));
    }
    #[test]
    fn skip_strict_ttl_and_refresh_bypass() {
        for (age, refresh, skips) in [(9, false, 2), (10, false, 0), (0, true, 0)] {
            let mut q = worker();
            q.refresh = refresh;
            let t = run(q, |e, r| {
                if e.kind == 15 {
                    r.flag = true;
                    r.verified = true;
                }
                if e.kind == 16 {
                    r.value = age;
                }
            });
            assert_eq!(t.iter().filter(|e| e.kind == 22 && e.a == 0).count(), skips);
            if refresh {
                assert!(!t.iter().any(|e| e.kind == 15 || e.kind == 16));
            }
        }
    }
    #[test]
    fn delay_failure_and_put_failure_save_before_counter() {
        let t = run(worker(), |e, r| {
            if e.kind == 13 {
                r.ok = false;
                r.delays[0].value = 0;
            }
            if e.kind == 17 && e.data == b"b" {
                r.ok = false;
            }
        });
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 19)
                .map(|e| (e.a, e.b))
                .collect::<Vec<_>>(),
            vec![(0, 1), (0, 2)]
        );
        assert_eq!(t.iter().filter(|e| e.kind == 22 && e.a == 1).count(), 2);
        assert!(!t.iter().any(|e| e.kind == 18));
    }
    #[test]
    fn first_two_successful_geo_answers_and_conflict() {
        let t = run(worker(), |e, r| {
            if e.kind == 18 {
                if e.a == 0 {
                    r.ok = false;
                } else {
                    r.country = if e.a == 1 {
                        b"US".to_vec()
                    } else {
                        b"JP".to_vec()
                    };
                }
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 18).count(), 6);
        for e in t.iter().filter(|e| e.kind == 19) {
            assert_eq!((e.a, e.b), (2, 5));
            assert_eq!(e.names, vec![b"s2".to_vec(), b"s3".to_vec()]);
        }
    }
    #[test]
    fn zero_and_single_source_results() {
        for successes in [0, 1] {
            let t = run(worker(), |e, r| {
                if e.kind == 18 {
                    r.ok = e.a < successes;
                }
            });
            assert!(t
                .iter()
                .filter(|e| e.kind == 19)
                .all(|e| e.a == successes && e.b == if successes == 0 { 3 } else { 4 }));
        }
    }
    #[test]
    fn temporary_start_and_list_failure_cleanup_preserves_error() {
        for fail in [9, 12] {
            let t = run(worker(), |e, r| {
                if e.kind == 8 {
                    r.flag = false;
                }
                if e.kind == fail {
                    r.ok = false;
                    r.error = b"original-failure".to_vec();
                    r.token = 99;
                }
                if e.kind == 10 {
                    r.ok = false;
                    r.error = b"stop".to_vec();
                }
            });
            assert_eq!(
                t.iter().filter(|e| e.kind == 10).count(),
                usize::from(fail == 12)
            );
            let finish = t.iter().find(|e| e.kind == 23).unwrap();
            assert_eq!((finish.a, finish.b), (1, 99));
            assert_eq!(finish.text, b"original-failure");
        }
    }
    #[test]
    fn temporary_decided_once_and_finish_nil_does_not_clear_error() {
        let t = run(worker(), |e, r| {
            if e.kind == 8 {
                r.flag = false
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 8).count(), 1);
        assert_eq!(t.iter().filter(|e| e.kind == 10).count(), 1);
        let finish = t.iter().find(|e| e.kind == 23).unwrap();
        assert_eq!(finish.a, 0);
        assert!(finish.text.is_empty());
    }
    #[test]
    fn replacement_job_running_does_not_cancel_and_stopped_job_still_restores() {
        let mut locks = 0;
        let t = run(worker(), |e, r| {
            if e.kind == 1 {
                locks += 1;
                if locks == 3 {
                    r.snapshot.as_mut().unwrap().job_running = false;
                }
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 19).count(), 1);
        let puts: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 17)
            .map(|e| e.data.clone())
            .collect();
        assert_eq!(puts, vec![b"a".to_vec(), b"original".to_vec()]);
    }
    #[test]
    fn store_and_restore_errors_log_only() {
        let t = run(worker(), |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().store = true;
            }
            if e.kind == 20 || e.kind == 17 && e.data == b"original" {
                r.ok = false;
                r.error = b"ignored".to_vec();
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 21 && e.a == 1).count(), 2);
        assert_eq!(t.iter().filter(|e| e.kind == 21 && e.a == 2).count(), 1);
        assert_eq!(t.iter().find(|e| e.kind == 23).unwrap().a, 0);
    }
    #[test]
    fn finish_borrowed_nil_job_noop_and_error_bytes_preserved() {
        let t = run(
            Request {
                op: 2,
                has_error: true,
                error: vec![255, 0],
                error_token: 7,
                ..Request::default()
            },
            |_, _| {},
        );
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![3, 4, 23, 63]
        );
        assert_eq!(t[2].text, vec![255, 0]);
        let t = run(
            Request {
                op: 2,
                ..Request::default()
            },
            |e, r| {
                if e.kind == 3 {
                    r.snapshot.as_mut().unwrap().job_exists = false
                }
            },
        );
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![3, 63]);
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = Flow::new(worker()).unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(e, f.next());
        assert!(f
            .advance(Reply {
                kind: 2,
                snapshot: Some(snap()),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(e, f.next());
    }
    #[test]
    fn more_than_100k_steps_without_cloning_node_queue() {
        let mut f = Flow::new(worker()).unwrap();
        let count = 25000;
        let mut steps = 0;
        let mut saved = 0;
        loop {
            let e = f.next();
            if e.kind == 0 {
                break;
            }
            let mut r = reply(&e);
            if e.kind == 12 {
                r.names = (0..count).map(|i| i.to_string().into_bytes()).collect();
            }
            if e.kind == 13 {
                r.delays.clear();
            }
            if e.kind == 19 {
                saved += 1;
            }
            f.advance(r).unwrap();
            steps += 1;
            assert!(steps < 300000);
        }
        assert_eq!(saved, count);
        assert!(steps > 100000);
    }
    #[test]
    fn reject_or_empty_original_never_restored_and_empty_nodes_still_delay() {
        for original in [b"REJECT".as_slice(), b""] {
            let t = run(worker(), |e, r| {
                if e.kind == 11 {
                    r.name = original.to_vec();
                }
                if e.kind == 12 {
                    r.names.clear();
                }
            });
            assert_eq!(t.iter().filter(|e| e.kind == 13).count(), 1);
            assert!(!t.iter().any(|e| e.kind == 17));
            assert!(t.iter().any(|e| e.kind == 23 && e.a == 0));
        }
    }
    #[test]
    fn unrelated_finish_error_request_fields_do_not_leak_into_worker() {
        let mut q = worker();
        q.has_error = true;
        q.error = b"irrelevant".to_vec();
        q.error_token = 42;
        let t = run(q, |_, _| {});
        let e = t.iter().find(|e| e.kind == 23).unwrap();
        assert_eq!((e.a, e.b), (0, 0));
        assert!(e.text.is_empty());
    }
}
