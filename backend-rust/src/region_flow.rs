//! Region-lock decisions and report projection; Go retains opaque DTO fields and time arithmetic.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Record {
    pub name: Vec<u8>,
    pub country: Vec<u8>,
    pub verified: bool,
    pub age: i64,
    pub cooldown: bool,
    pub latency: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub chain: bool,
    pub locked_region: Vec<u8>,
    pub locked_node: Vec<u8>,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub country: Vec<u8>,
    pub valid: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
    pub indices: Vec<i64>,
    pub stale: Vec<bool>,
    pub pool: Vec<i64>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub name: Vec<u8>,
    pub error: Vec<u8>,
    pub ttl: i64,
    pub records: Vec<Record>,
    pub snapshot: Option<Snapshot>,
}
// 1 Lock,2 TimeFacts (a1validation,2candidate,3report,4finalpool),
// 3 CommitRegion(data),4 CommitNode(data),5 SaveSettings,6 Running,
// 7 NowForRunning(block saved timestamp),8 Get(data group),9 Put(data target),
// 10 Block(data country; Go health sub-usecase),11 LogPin(data node,text error),
// 12 Event(data country; saved event time),13 NowEvent,63Result(aHTTP,bcode,data region,pool count or indices),0Done.
// code0lock success,1invalidcountry,2chain,3nolockable,4save,5report success.
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    stage: u8,
    active: Vec<u8>,
}
pub fn sorted_indices(records: &[Record]) -> Vec<usize> {
    let mut v: Vec<_> = (0..records.len()).collect();
    v.sort_by(|a, b| records[*a].name.cmp(&records[*b].name));
    v
}
pub fn pool_indices(records: &[Record], country: &[u8], ttl: i64) -> Vec<usize> {
    if country.is_empty() {
        return vec![];
    }
    let mut v: Vec<_> = records
        .iter()
        .enumerate()
        .filter(|(_, r)| r.verified && r.country == country && r.age <= ttl && !r.cooldown)
        .map(|(i, _)| i)
        .collect();
    v.sort_by(|a, b| records[*a].name.cmp(&records[*b].name));
    v
}
pub fn best_candidate(records: &[Record], pool: &[usize]) -> Vec<u8> {
    let Some(first) = pool.first() else {
        return vec![];
    };
    let mut best = None;
    for &i in pool {
        let r = &records[i];
        if r.latency <= 0 {
            continue;
        }
        if r.name.is_empty() {
            return records[*first].name.clone();
        }
        if best.is_none_or(|j: usize| r.latency < records[j].latency) {
            best = Some(i);
        }
    }
    records[best.unwrap_or(*first)].name.clone()
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 1 {
            return Err("unknown region operation");
        }
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            stage: 0,
            active: vec![],
        };
        if f.q.op == 0
            && (!f.q.valid
                || (!f.q.country.is_empty()
                    && (f.q.country.len() != 2 || !f.q.country.iter().all(u8::is_ascii_uppercase))))
        {
            f.result(400, 1);
        } else {
            f.emit(1, 1);
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
    fn facts(&mut self, purpose: i64, stage: u8) {
        self.emit(2, stage);
        self.e.a = purpose;
    }
    fn result(&mut self, status: i64, code: i64) {
        self.emit(63, 99);
        self.e.a = status;
        self.e.b = code;
        self.e.data = self.s.locked_region.clone();
    }
    fn event(&mut self) {
        self.emit(13, 15);
    }
    fn compare_active(&mut self) {
        if self.active != self.s.locked_node {
            self.emit(9, 10);
            self.e.data = self.s.locked_node.clone();
        } else {
            self.event();
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || self.e.kind != r.kind {
            return Err("unexpected region reply");
        }
        if self.e.kind == 1 && r.snapshot.is_none() {
            return Err("missing region snapshot");
        }
        if let Some(s) = r.snapshot {
            self.s = s;
        }
        match self.stage {
            1 => {
                if self.q.op == 1 {
                    self.facts(3, 20);
                } else if !self.q.country.is_empty() {
                    if self.s.chain {
                        self.result(409, 2);
                    } else {
                        self.facts(1, 2);
                    }
                } else {
                    self.emit(3, 3);
                }
            }
            2 => {
                if !r
                    .records
                    .iter()
                    .any(|v| v.verified && v.country == self.q.country && v.age < r.ttl)
                {
                    self.result(400, 3);
                } else {
                    self.emit(3, 3);
                    self.e.data = self.q.country.clone();
                }
            }
            3 => {
                self.s.locked_region = self.q.country.clone();
                if self.q.country.is_empty() {
                    self.s.locked_node.clear();
                    self.emit(4, 5);
                } else {
                    self.facts(2, 4);
                }
            }
            4 => {
                let pool = pool_indices(&r.records, &self.s.locked_region, r.ttl);
                let best = best_candidate(&r.records, &pool);
                self.s.locked_node = best.clone();
                self.emit(4, 5);
                self.e.data = best;
            }
            5 => self.emit(5, 6),
            6 => {
                if !r.ok {
                    self.result(500, 4);
                } else if self.q.country.is_empty() {
                    self.event();
                } else {
                    self.emit(6, 7);
                }
            }
            7 => {
                if r.flag {
                    self.emit(7, 8);
                } else {
                    self.event();
                }
            }
            8 => {
                if self.s.locked_node.is_empty() {
                    self.emit(10, 11);
                    self.e.data = self.q.country.clone();
                } else {
                    self.emit(8, 9);
                    self.e.data = b"SmartVPN".to_vec();
                }
            }
            9 => {
                self.active = if r.ok { r.name } else { vec![] };
                if self.active == b"SmartVPNAuto" || self.active == b"SmartVPNFallback" {
                    self.emit(8, 13);
                    self.e.data = self.active.clone();
                } else {
                    self.compare_active();
                }
            }
            13 => {
                if r.ok {
                    self.active = r.name;
                }
                self.compare_active();
            }
            10 => {
                if !r.ok {
                    self.emit(11, 11);
                    self.e.data = self.s.locked_node.clone();
                    self.e.text = r.error;
                } else {
                    self.event();
                }
            }
            11 => self.event(),
            12 => self.facts(4, 14),
            15 => {
                self.emit(12, 12);
                self.e.data = self.q.country.clone();
            }
            14 => {
                let pool = pool_indices(&r.records, &self.s.locked_region, r.ttl);
                self.result(200, 0);
                self.e.indices = vec![pool.len() as i64];
            }
            20 => {
                let order = sorted_indices(&r.records);
                let pool = pool_indices(&r.records, &self.s.locked_region, r.ttl);
                self.result(200, 5);
                self.e.stale = order
                    .iter()
                    .map(|i| r.records[*i].verified && r.records[*i].age > r.ttl)
                    .collect();
                self.e.indices = order.into_iter().map(|i| i as i64).collect();
                self.e.pool = pool.into_iter().map(|i| i as i64).collect();
            }
            99 => self.emit(0, 100),
            _ => return Err("invalid region stage"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn rec(n: &[u8], age: i64, latency: i64) -> Record {
        Record {
            name: n.to_vec(),
            country: b"US".to_vec(),
            verified: true,
            age,
            latency,
            ..Record::default()
        }
    }
    fn run(op: u8, country: &[u8], mut alter: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(Request {
            op,
            country: country.to_vec(),
            valid: true,
        })
        .unwrap();
        let mut out = vec![];
        for _ in 0..40 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                ttl: 10,
                records: vec![rec(b"a", 0, 20), rec(b"b", 0, 10)],
                snapshot: if e.kind == 1 {
                    Some(Snapshot {
                        locked_region: b"US".to_vec(),
                        ..Snapshot::default()
                    })
                } else {
                    None
                },
                ..Reply::default()
            };
            alter(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop")
    }
    #[test]
    fn ttl_boundary_report_and_pool_differ_from_lockability() {
        let t = run(1, b"", |_, r| {
            r.records = vec![rec(b"z", 11, 1), rec(b"a", 10, 2), rec(b"future", -1, 3)]
        });
        let e = t.last().unwrap();
        assert_eq!(e.indices, vec![1, 2, 0]);
        assert_eq!(e.stale, vec![false, false, true]);
        assert_eq!(e.pool, vec![1, 2]);
        let t = run(0, b"US", |_, r| r.records = vec![rec(b"equal", 10, 1)]);
        assert_eq!((t.last().unwrap().a, t.last().unwrap().b), (400, 3));
    }
    #[test]
    fn clear_in_chain_does_not_unblock_run_or_switch() {
        let t = run(0, b"", |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().chain = true
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 3, 4, 5, 13, 12, 2, 63]
        );
        assert_eq!(t.last().unwrap().indices, vec![0]);
    }
    #[test]
    fn nonempty_chain_and_invalid_country_fail_before_writes() {
        let t = run(0, b"US", |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().chain = true
            }
        });
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 63]);
        assert_eq!(t.last().unwrap().a, 409);
        for country in [b"uS".as_slice(), b"USA", b"1A", b"\xff\xff"] {
            let f = Flow::new(Request {
                op: 0,
                country: country.to_vec(),
                valid: true,
            })
            .unwrap();
            assert_eq!((f.next().kind, f.next().a), (63, 400));
        }
    }
    #[test]
    fn cooldown_can_validate_then_block() {
        let t = run(0, b"US", |e, r| {
            for record in &mut r.records {
                record.cooldown = true;
            }
            if e.kind == 6 {
                r.flag = true;
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 2, 3, 2, 4, 5, 6, 7, 10, 13, 12, 2, 63]
        );
        assert!(t.iter().find(|e| e.kind == 4).unwrap().data.is_empty());
        assert_eq!(t.last().unwrap().indices, vec![0]);
    }
    #[test]
    fn best_positive_ties_bytes_and_unknown_latency_fallback() {
        let mut v = vec![rec(b"z", 0, 5), rec(b"a", 0, 5), rec(b"unknown", 0, 0)];
        let pool = pool_indices(&v, b"US", 10);
        assert_eq!(best_candidate(&v, &pool), b"a");
        for r in &mut v {
            r.latency = -9;
        }
        assert_eq!(best_candidate(&v, &pool), b"a");
        v.push(rec(b"", 0, 1));
        let pool = pool_indices(&v, b"US", 10);
        assert!(best_candidate(&v, &pool).is_empty());
    }
    #[test]
    fn save_failure_keeps_prior_mutations_and_skips_effects() {
        let t = run(0, b"US", |e, r| {
            if e.kind == 5 {
                r.ok = false
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 2, 3, 2, 4, 5, 63]
        );
        assert_eq!(t.last().unwrap().a, 500);
        assert_eq!(t.iter().find(|e| e.kind == 4).unwrap().data, b"b");
    }
    #[test]
    fn running_pin_error_logs_but_event_and_count_still_finish() {
        let t = run(0, b"US", |e, r| {
            if e.kind == 6 {
                r.flag = true;
            }
            if e.kind == 8 {
                r.name = b"old".to_vec();
            }
            if e.kind == 9 {
                r.ok = false;
                r.error = b"pin failed".to_vec();
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 2, 3, 2, 4, 5, 6, 7, 8, 9, 11, 13, 12, 2, 63]
        );
        assert_eq!(t.last().unwrap().a, 200);
    }
    #[test]
    fn nested_active_same_node_avoids_pin() {
        let t = run(0, b"US", |e, r| {
            if e.kind == 6 {
                r.flag = true;
            }
            if e.kind == 8 {
                r.name = if e.data == b"SmartVPN" {
                    b"SmartVPNAuto".to_vec()
                } else {
                    b"b".to_vec()
                };
            }
        });
        assert_eq!(t.iter().filter(|e| e.kind == 8).count(), 2);
        assert!(!t.iter().any(|e| e.kind == 9));
    }
    #[test]
    fn report_empty_country_still_sorts_and_marks_only_verified() {
        let t = run(1, b"", |e, r| {
            if e.kind == 1 {
                r.snapshot.as_mut().unwrap().locked_region.clear();
            }
            r.records = vec![
                Record {
                    verified: false,
                    ..rec(b"a", i64::MAX, -10)
                },
                rec(&[255], i64::MIN, 1),
                rec(&[0], 0, 1),
            ];
        });
        let e = t.last().unwrap();
        assert_eq!(e.indices, vec![2, 0, 1]);
        assert_eq!(e.stale, vec![false, false, false]);
        assert!(e.pool.is_empty());
    }
    #[test]
    fn large_report_is_sorted_without_quadratic_search() {
        let records: Vec<_> = (0..100000)
            .rev()
            .map(|n| rec(format!("{n:06}").as_bytes(), 0, n))
            .collect();
        let order = sorted_indices(&records);
        assert_eq!(order.len(), 100000);
        assert_eq!(records[order[0]].name, b"000000");
        let p = pool_indices(&records, b"US", 10);
        assert_eq!(p, order);
    }
    #[test]
    fn rejected_reply_does_not_consume() {
        let mut f = Flow::new(Request {
            op: 1,
            ..Request::default()
        })
        .unwrap();
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
                kind: 2,
                snapshot: Some(Snapshot::default()),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), e);
    }
}
