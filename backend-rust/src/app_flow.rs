//! Application reports and health-control workflows. Platform resources and opaque DTO fields remain in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Health {
    pub state: Vec<u8>,
    pub cooldown: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Region {
    pub country: Vec<u8>,
    pub verified: bool,
    pub expired: bool,
    pub cooldown: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Count {
    pub name: Vec<u8>,
    pub count: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Plan {
    pub running: bool,
    pub proxy_enabled: bool,
    pub zero_job: bool,
    pub strict_privacy: bool,
    pub tun_active: bool,
    pub active_node: Vec<u8>,
    pub active_region: Vec<u8>,
    pub tun_reason: Vec<u8>,
    pub locked_region: Vec<u8>,
    pub counts: Vec<Count>,
    pub pool_size: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub params: [i64; 6],
    pub limit: Vec<u8>,
    pub interval: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub other: bool,
    pub proxy_on: bool,
    pub webrtc: bool,
    pub dns: bool,
    pub job_nil: bool,
    pub chain_enabled: bool,
    pub mode_supported: bool,
    pub selection_mode: Vec<u8>,
    pub selected_node: Vec<u8>,
    pub locked_region: Vec<u8>,
    pub locked_node: Vec<u8>,
    pub mode: Vec<u8>,
    pub default_mode: Vec<u8>,
    pub text: Vec<u8>,
    pub count: i64,
    pub params: [i64; 6],
    pub health: Vec<Health>,
    pub regions: Vec<Region>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub text: Vec<u8>,
    pub count: i64,
    pub interval: i64,
    pub params: [i64; 6],
    pub plan: Plan,
    pub status: i64,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}

pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    child: Option<Box<Flow>>,
    resume: u16,
    active_resume: u16,
    plan: Plan,
    regions: Vec<Region>,
    params: [i64; 6],
    proxy_on: bool,
    active: Vec<u8>,
    interval: i64,
    events: i64,
    index: i64,
}
fn choice(r: &Reply) -> Vec<u8> {
    if r.chain_enabled {
        return b"SmartVPNChain".to_vec();
    }
    if !r.locked_region.is_empty() && !r.locked_node.is_empty() {
        return r.locked_node.clone();
    }
    match r.selection_mode.as_slice() {
        b"fallback" => b"SmartVPNFallback".to_vec(),
        b"manual" => r.selected_node.clone(),
        _ => b"SmartVPNAuto".to_vec(),
    }
}
fn mode(r: Reply) -> Vec<u8> {
    if r.mode_supported {
        r.mode
    } else {
        r.default_mode
    }
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 9 {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            child: None,
            resume: 0,
            active_resume: 0,
            plan: Plan::default(),
            regions: vec![],
            params: [0; 6],
            proxy_on: false,
            active: vec![],
            interval: 0,
            events: 0,
            index: 0,
        };
        match f.q.op {
            0 => f.emit(1, 1),
            1 => f.emit(13, 30),
            2 => {
                if f.q.params[1] != 0 && f.q.params[1] < 10 {
                    f.response(400, b"interval", 99)
                } else {
                    f.params = crate::health_core::normalize(f.q.params);
                    f.emit(1, 40)
                }
            }
            3 => f.emit(1, 50),
            4 => {
                f.emit(36, 60);
                f.e.text = f.q.limit.clone()
            }
            5 => f.emit(20, 70),
            6 => f.emit(20, 75),
            7 => {
                f.emit(33, 85);
                f.e.interval = f.q.interval
            }
            8 => f.emit(1, 90),
            9 => f.emit(4, 100),
            _ => unreachable!(),
        };
        Ok(f)
    }
    pub fn next(&self) -> &Effect {
        if let Some(c) = &self.child {
            c.next()
        } else {
            &self.e
        }
    }
    fn emit(&mut self, kind: u8, pc: u16) {
        self.e = Effect {
            kind,
            ..Default::default()
        };
        self.pc = pc
    }
    fn done(&mut self) {
        self.e = Effect {
            kind: 63,
            plan: self.plan.clone(),
            ..Default::default()
        }
    }
    fn response(&mut self, status: i64, text: &[u8], pc: u16) {
        self.emit(15, pc);
        self.e.status = status;
        self.e.text = text.to_vec();
        self.e.plan = self.plan.clone()
    }
    fn log(&mut self, text: &[u8], pc: u16) {
        self.emit(6, pc);
        self.e.text = text.to_vec()
    }
    fn child(&mut self, op: u8, resume: u16) -> Result<(), Error> {
        self.child = Some(Box::new(Self::new(Request {
            op,
            ..Default::default()
        })?));
        self.resume = resume;
        Ok(())
    }
    fn get_active(&mut self, resume: u16) {
        self.active_resume = resume;
        self.emit(7, 110);
        self.e.text = b"SmartVPN".to_vec()
    }
    fn active_done(&mut self) {
        match self.active_resume {
            20 => {
                self.plan.active_node = self.active.clone();
                self.emit(8, 21);
                self.e.text = self.active.clone()
            }
            102 => {
                if self.active.is_empty() || self.active == b"REJECT" || self.active == b"DIRECT" {
                    self.emit(16, 103)
                } else {
                    self.emit(16, 104)
                }
            }
            _ => unreachable!(),
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if r.kind != self.next().kind {
            return Err(Error::Invalid);
        }
        if r.kind == 64 {
            return Err(Error::Done);
        }
        if let Some(c) = &mut self.child {
            c.advance(r)?;
            if c.next().kind == 63 {
                let out = self.child.take().unwrap().next().plan.clone();
                self.child_result(out)?;
            }
            return Ok(());
        }
        if r.kind == 63 {
            self.emit(64, 0);
            return Ok(());
        }
        match self.pc {
            1 => self.emit(3, 2),
            2 => {
                self.proxy_on = r.proxy_on;
                self.plan.zero_job = r.job_nil;
                self.plan.strict_privacy = r.webrtc && r.dns;
                self.emit(4, 3)
            }
            3 => {
                self.plan.running = r.flag;
                if r.flag && self.proxy_on {
                    self.emit(5, 4)
                } else if r.flag {
                    self.get_active(20)
                } else {
                    self.emit(9, 22)
                }
            }
            4 => {
                self.plan.proxy_enabled = r.flag;
                if !r.ok {
                    self.log(b"proxy", 5)
                } else {
                    self.get_active(20)
                }
            }
            5 => self.get_active(20),
            21 => {
                self.plan.active_region = r.text;
                self.emit(9, 22)
            }
            22 => {
                self.plan.tun_reason = r.text;
                self.emit(10, 23)
            }
            23 => self.emit(11, 24),
            24 => {
                let selected = choice(&r);
                self.emit(39, 25);
                self.e.text = selected
            }
            25 => self.child(1, 26)?,
            27 => {
                let m = mode(r);
                self.emit(40, 28);
                self.e.text = m
            }
            28 => {
                if self.plan.running {
                    self.emit(12, 29)
                } else {
                    self.response(200, b"state", 98)
                }
            }
            29 => {
                self.plan.tun_active = mode(r) != b"system-proxy";
                self.response(200, b"state", 98)
            }
            30 => {
                let mut counts = std::collections::BTreeMap::new();
                for name in [
                    b"HEALTHY".as_slice(),
                    b"SUSPECT",
                    b"UNHEALTHY",
                    b"COOLDOWN",
                    b"RECOVERY",
                ] {
                    counts.insert(name.to_vec(), 0i64);
                }
                for h in r.health {
                    let key = if h.cooldown {
                        b"COOLDOWN".to_vec()
                    } else {
                        h.state
                    };
                    let n = counts.entry(key).or_insert(0);
                    *n = n.wrapping_add(1)
                }
                self.plan.counts = counts
                    .into_iter()
                    .map(|(name, count)| Count { name, count })
                    .collect();
                self.regions = r.regions;
                self.emit(14, 31)
            }
            31 => {
                self.plan.locked_region = r.text;
                self.emit(14, 32)
            }
            32 => {
                if !r.text.is_empty() {
                    self.plan.pool_size = self
                        .regions
                        .iter()
                        .filter(|x| x.verified && !x.expired && !x.cooldown && x.country == r.text)
                        .count() as i64
                }
                self.regions.clear();
                self.done()
            }
            40 => {
                self.emit(17, 41);
                self.e.params = self.params
            }
            41 => self.emit(18, 42),
            42 => {
                if r.flag {
                    self.emit(19, 43);
                    self.e.params = self.params
                } else {
                    self.emit(20, 44)
                }
            }
            43 => {
                if !r.ok {
                    self.response(500, b"save-params", 98)
                } else {
                    self.emit(20, 44)
                }
            }
            44 => {
                if r.flag && self.params[1] != 0 {
                    self.emit(21, 45)
                } else {
                    self.emit(24, 47)
                }
            }
            45 => self.child(5, 46)?,
            47 => self.emit(16, 48),
            48 => self.response(200, b"params", 98),
            50 => self.emit(16, 51),
            51 => self.emit(4, 52),
            52 => {
                if !r.flag {
                    self.response(409, b"connect", 98)
                } else {
                    self.child(9, 53)?
                }
            }
            60 => {
                self.events = if r.count > 0 && r.count <= 500 {
                    r.count
                } else {
                    50
                };
                self.emit(18, 61)
            }
            61 => {
                if !r.flag {
                    self.response(500, b"no-store", 99)
                } else {
                    self.emit(37, 62);
                    self.e.count = self.events
                }
            }
            62 => {
                if !r.ok {
                    self.response(500, b"events", 99)
                } else {
                    self.events = r.count;
                    self.index = 0;
                    self.event_next()
                }
            }
            63 => {
                self.index = self.index.wrapping_add(1);
                self.event_next()
            }
            70 => {
                if r.flag {
                    self.done()
                } else {
                    self.emit(16, 71)
                }
            }
            71 => {
                self.interval = r.params[1].wrapping_mul(1_000_000_000);
                self.emit(22, 72)
            }
            72 => {
                self.emit(23, 73);
                self.e.interval = self.interval
            }
            73 => self.done(),
            75 => {
                if r.flag {
                    self.emit(21, 76)
                } else {
                    self.emit(16, 77)
                }
            }
            76 => self.emit(16, 77),
            77 => {
                if !r.flag {
                    self.done()
                } else {
                    self.emit(4, 78)
                }
            }
            78 => {
                if r.flag {
                    self.emit(11, 79)
                } else {
                    self.emit(30, 82)
                }
            }
            79 => {
                let c = choice(&r);
                self.emit(32, 80);
                self.e.text = c
            }
            80 => {
                if !r.ok {
                    self.log(b"stop", 81)
                } else {
                    self.emit(30, 82)
                }
            }
            81 => self.emit(30, 82),
            82 => self.emit(31, 83),
            83 => self.done(),
            85 => self.emit(34, 86),
            86 => {
                if r.flag {
                    self.emit(41, 87)
                } else {
                    self.emit(35, 88)
                }
            }
            87 => self.emit(34, 86),
            88 => self.done(),
            90 => self.child(9, 91)?,
            100 => {
                if !r.flag {
                    self.done()
                } else {
                    self.emit(25, 101)
                }
            }
            101 => {
                if r.flag {
                    self.done()
                } else {
                    self.emit(26, 102)
                }
            }
            102 => self.get_active(102),
            103 => {
                if r.flag {
                    self.emit(29, 108)
                } else {
                    self.done()
                }
            }
            104 => {
                self.emit(27, 105);
                self.e.interval = r.params[0].wrapping_mul(1_000_000)
            }
            105 => {
                if !r.ok {
                    self.log(b"sweep", 106)
                } else {
                    self.emit(28, 108);
                    self.e.text = self.active.clone()
                }
            }
            106 => {
                self.emit(28, 108);
                self.e.text = self.active.clone();
                self.e.count = 1
            }
            108 => self.done(),
            110 => {
                self.active = if r.ok { r.text } else { vec![] };
                if self.active == b"SmartVPNAuto" || self.active == b"SmartVPNFallback" {
                    self.emit(7, 111);
                    self.e.text = self.active.clone()
                } else {
                    self.active_done()
                }
            }
            111 => {
                if r.ok {
                    self.active = r.text
                }
                self.active_done()
            }
            98 => self.emit(2, 99),
            99 => self.done(),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
    fn event_next(&mut self) {
        if self.index < self.events {
            self.emit(38, 63);
            self.e.count = self.index
        } else {
            self.response(200, b"events", 99)
        }
    }
    fn child_result(&mut self, p: Plan) -> Result<(), Error> {
        match self.resume {
            26 => {
                self.plan.counts = p.counts;
                self.plan.pool_size = p.pool_size;
                self.plan.locked_region = p.locked_region;
                self.emit(12, 27)
            }
            46 => self.emit(24, 47),
            53 => self.child(1, 54)?,
            54 => {
                self.plan = p;
                self.response(200, b"patrol", 98)
            }
            91 => self.emit(2, 99),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn ok() -> Reply {
        Reply {
            ok: true,
            ..Default::default()
        }
    }
    fn run(q: Request, mut fx: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(q).unwrap();
        let mut trace = vec![];
        for _ in 0..1000 {
            let e = f.next().clone();
            if e.kind == 63 {
                return (e, trace);
            }
            trace.push(e.clone());
            let mut r = fx(&e);
            r.kind = e.kind;
            f.advance(r).unwrap()
        }
        panic!("not finished")
    }
    fn facts(e: &Effect) -> Reply {
        match e.kind {
            3 => Reply {
                proxy_on: true,
                webrtc: true,
                dns: true,
                job_nil: true,
                ..ok()
            },
            4 => Reply { flag: true, ..ok() },
            7 => Reply {
                text: b"node".to_vec(),
                ..ok()
            },
            8 => Reply {
                text: b"JP".to_vec(),
                ..ok()
            },
            12 => Reply {
                mode_supported: true,
                mode: b"tun".to_vec(),
                ..ok()
            },
            14 => Reply {
                text: b"JP".to_vec(),
                ..ok()
            },
            16 => Reply {
                params: [3000, 60, 2, 60, 30, 3],
                ..ok()
            },
            _ => ok(),
        }
    }
    #[test]
    fn snapshot_unknown_states_and_cooldown_precedence() {
        let (r, _) = run(
            Request {
                op: 1,
                ..Default::default()
            },
            |e| {
                if e.kind == 13 {
                    Reply {
                        health: vec![
                            Health {
                                state: vec![],
                                cooldown: false,
                            },
                            Health {
                                state: b"UNKNOWN".to_vec(),
                                cooldown: false,
                            },
                            Health {
                                state: b"HEALTHY".to_vec(),
                                cooldown: true,
                            },
                        ],
                        ..ok()
                    }
                } else {
                    facts(e)
                }
            },
        );
        let m = r
            .plan
            .counts
            .into_iter()
            .map(|c| (c.name, c.count))
            .collect::<std::collections::BTreeMap<_, _>>();
        assert_eq!(m.len(), 7);
        assert_eq!(m[b"COOLDOWN".as_slice()], 1);
        assert_eq!(m[b"HEALTHY".as_slice()], 0);
        assert_eq!(m[b"".as_slice()], 1);
    }
    #[test]
    fn snapshot_two_pin_reads_have_distinct_projection_and_filter_semantics() {
        let mut pins = 0;
        let (r, t) = run(
            Request {
                op: 1,
                ..Default::default()
            },
            |e| match e.kind {
                13 => Reply {
                    regions: vec![
                        Region {
                            country: b"US".to_vec(),
                            verified: true,
                            ..Default::default()
                        },
                        Region {
                            country: b"US".to_vec(),
                            verified: true,
                            cooldown: true,
                            ..Default::default()
                        },
                        Region {
                            country: b"US".to_vec(),
                            verified: true,
                            expired: true,
                            ..Default::default()
                        },
                    ],
                    ..ok()
                },
                14 => {
                    pins += 1;
                    Reply {
                        text: if pins == 1 {
                            b"JP".to_vec()
                        } else {
                            b"US".to_vec()
                        },
                        ..ok()
                    }
                }
                _ => facts(e),
            },
        );
        assert_eq!(r.plan.locked_region, b"JP");
        assert_eq!(r.plan.pool_size, 1);
        assert_eq!(t.len(), 3);
    }
    #[test]
    fn params_validation_before_lock_and_zero_normalizes() {
        let (_, t) = run(
            Request {
                op: 2,
                params: [0, 9, 0, 0, 0, 0],
                ..Default::default()
            },
            facts,
        );
        assert_eq!(t.len(), 1);
        assert_eq!(t[0].status, 400);
        let (_, t) = run(
            Request {
                op: 2,
                ..Default::default()
            },
            facts,
        );
        assert_eq!(
            t.iter().find(|e| e.kind == 17).unwrap().params,
            [1000, 10, 1, 10, 0, 1]
        );
        assert!(t
            .iter()
            .map(|e| e.kind)
            .collect::<Vec<_>>()
            .ends_with(&[24, 16, 15, 2]));
    }
    #[test]
    fn params_save_failure_does_not_rollback_or_restart() {
        let (_, t) = run(
            Request {
                op: 2,
                ..Default::default()
            },
            |e| match e.kind {
                18 => Reply { flag: true, ..ok() },
                19 => Reply::default(),
                _ => facts(e),
            },
        );
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 17, 18, 19, 15, 2]
        );
        assert_eq!(t[4].status, 500);
    }
    #[test]
    fn params_restart_has_close_then_new_and_spawn_before_path() {
        let mut presents = 0;
        let (_, t) = run(
            Request {
                op: 2,
                params: [3000, 60, 2, 60, 30, 3],
                ..Default::default()
            },
            |e| {
                if e.kind == 20 {
                    presents += 1;
                    Reply {
                        flag: presents == 1,
                        ..ok()
                    }
                } else {
                    facts(e)
                }
            },
        );
        let k = t.iter().map(|e| e.kind).collect::<Vec<_>>();
        assert_eq!(k, vec![1, 17, 18, 20, 21, 20, 16, 22, 23, 24, 16, 15, 2]);
    }
    #[test]
    fn start_only_checks_existing_channel_not_running() {
        let (_, t) = run(
            Request {
                op: 5,
                ..Default::default()
            },
            facts,
        );
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![20, 16, 22, 23]
        );
        assert_eq!(t[3].interval, 60_000_000_000);
        let (_, t) = run(
            Request {
                op: 5,
                ..Default::default()
            },
            |_| Reply { flag: true, ..ok() },
        );
        assert_eq!(t.len(), 1);
    }
    #[test]
    fn stop_switch_failure_is_log_only_then_clears_both_fields() {
        let (_, t) = run(
            Request {
                op: 6,
                ..Default::default()
            },
            |e| match e.kind {
                20 | 16 => Reply {
                    flag: true,
                    ..facts(e)
                },
                32 => Reply::default(),
                _ => facts(e),
            },
        );
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![20, 21, 16, 4, 11, 32, 6, 30, 31]
        );
        assert_eq!(t[5].text, b"SmartVPNAuto");
    }
    #[test]
    fn once_stopped_only_locks_checks_and_unlocks() {
        let (_, t) = run(
            Request {
                op: 8,
                ..Default::default()
            },
            |_| ok(),
        );
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 4, 2]);
    }
    #[test]
    fn sweep_job_gate_does_not_measure_and_has_no_initial_clock() {
        let (_, t) = run(
            Request {
                op: 9,
                ..Default::default()
            },
            |e| {
                if e.kind == 25 {
                    Reply { flag: true, ..ok() }
                } else {
                    facts(e)
                }
            },
        );
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![4, 25]);
    }
    #[test]
    fn sweep_nested_empty_and_failed_bulk_use_correct_existing_subcase() {
        let (_, t) = run(
            Request {
                op: 9,
                ..Default::default()
            },
            |e| match e.kind {
                7 if e.text == b"SmartVPN" => Reply {
                    text: b"SmartVPNAuto".to_vec(),
                    ..ok()
                },
                7 => ok(),
                16 => Reply {
                    flag: true,
                    ..facts(e)
                },
                _ => facts(e),
            },
        );
        assert!(t.iter().any(|e| e.kind == 29));
        assert!(!t.iter().any(|e| e.kind == 27));
        let (_, t) = run(
            Request {
                op: 9,
                ..Default::default()
            },
            |e| {
                if e.kind == 27 {
                    Reply::default()
                } else {
                    facts(e)
                }
            },
        );
        assert!(t
            .iter()
            .map(|e| e.kind)
            .collect::<Vec<_>>()
            .ends_with(&[27, 6, 28]));
        assert_eq!(t.last().unwrap().count, 1);
        assert_eq!(t.last().unwrap().text, b"node");
    }
    #[test]
    fn state_proxy_error_keeps_returned_bool_and_response_under_lock() {
        let (r, t) = run(Request::default(), |e| {
            if e.kind == 5 {
                Reply {
                    flag: true,
                    ..Default::default()
                }
            } else {
                facts(e)
            }
        });
        assert!(
            r.plan.running
                && r.plan.proxy_enabled
                && r.plan.zero_job
                && r.plan.strict_privacy
                && r.plan.tun_active
        );
        assert_eq!(r.plan.active_node, b"node");
        assert_eq!(r.plan.active_region, b"JP");
        assert!(t.iter().any(|e| e.kind == 6 && e.text == b"proxy"));
        assert!(t
            .iter()
            .map(|e| e.kind)
            .collect::<Vec<_>>()
            .ends_with(&[15, 2]));
    }
    #[test]
    fn state_not_running_skips_proxy_active_and_second_mode() {
        let (r, t) = run(Request::default(), |e| {
            if e.kind == 4 {
                ok()
            } else {
                facts(e)
            }
        });
        assert!(!r.plan.running && !r.plan.tun_active);
        assert!(!t.iter().any(|e| matches!(e.kind, 5 | 7 | 8)));
        assert_eq!(t.iter().filter(|e| e.kind == 12).count(), 1);
    }
    #[test]
    fn choice_priority_chain_pin_fallback_manual_and_default() {
        let mut r = Reply {
            chain_enabled: true,
            locked_region: b"JP".to_vec(),
            locked_node: b"pin".to_vec(),
            selection_mode: b"fallback".to_vec(),
            ..ok()
        };
        assert_eq!(choice(&r), b"SmartVPNChain");
        r.chain_enabled = false;
        assert_eq!(choice(&r), b"pin");
        r.locked_region.clear();
        assert_eq!(choice(&r), b"SmartVPNFallback");
        r.selection_mode = b"manual".to_vec();
        assert!(choice(&r).is_empty());
        r.selection_mode.clear();
        assert_eq!(choice(&r), b"SmartVPNAuto");
    }
    #[test]
    fn events_default_limit_projection_order_and_empty_result() {
        for (parsed, limit) in [(-1, 50), (0, 50), (500, 500), (501, 50), (7, 7)] {
            let (_, t) = run(
                Request {
                    op: 4,
                    ..Default::default()
                },
                |e| match e.kind {
                    36 => Reply {
                        count: parsed,
                        ..ok()
                    },
                    18 => Reply { flag: true, ..ok() },
                    37 => Reply { count: 3, ..ok() },
                    _ => facts(e),
                },
            );
            assert_eq!(t.iter().find(|e| e.kind == 37).unwrap().count, limit);
            assert_eq!(
                t.iter()
                    .filter(|e| e.kind == 38)
                    .map(|e| e.count)
                    .collect::<Vec<_>>(),
                vec![0, 1, 2]
            );
        }
    }
    #[test]
    fn long_loop_keeps_one_ticker_no_initial_sample_or_generation() {
        let mut f = Flow::new(Request {
            op: 7,
            interval: 123,
            ..Default::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, 33);
        f.advance(Reply { kind: 33, ..ok() }).unwrap();
        for _ in 0..100001 {
            assert_eq!(f.next().kind, 34);
            f.advance(Reply {
                kind: 34,
                flag: true,
                ..ok()
            })
            .unwrap();
            assert_eq!(f.next().kind, 41);
            f.advance(Reply { kind: 41, ..ok() }).unwrap();
            assert!(f.child.is_none());
        }
        f.advance(Reply { kind: 34, ..ok() }).unwrap();
        assert_eq!(f.next().kind, 35);
        f.advance(Reply { kind: 35, ..ok() }).unwrap();
        assert_eq!(f.next().kind, 63);
    }
    #[test]
    fn wrong_reply_does_not_consume() {
        let mut f = Flow::new(Request::default()).unwrap();
        let e = f.next().clone();
        assert_eq!(f.advance(Reply { kind: 3, ..ok() }), Err(Error::Invalid));
        assert_eq!(f.next(), &e);
    }
    #[test]
    fn extreme_duration_facts_wrap_and_params_clamp() {
        for v in [i64::MIN, i64::MAX] {
            let (_, trace) = run(
                Request {
                    op: 5,
                    ..Default::default()
                },
                |e| {
                    if e.kind == 16 {
                        Reply {
                            params: [v; 6],
                            ..ok()
                        }
                    } else {
                        ok()
                    }
                },
            );
            assert_eq!(
                trace.iter().find(|e| e.kind == 23).unwrap().interval,
                v.wrapping_mul(1_000_000_000)
            );
            let (_, trace) = run(
                Request {
                    op: 9,
                    ..Default::default()
                },
                |e| match e.kind {
                    4 => Reply { flag: true, ..ok() },
                    7 => Reply {
                        text: b"node".to_vec(),
                        ..ok()
                    },
                    16 => Reply {
                        params: [v; 6],
                        ..ok()
                    },
                    _ => ok(),
                },
            );
            assert_eq!(
                trace.iter().find(|e| e.kind == 27).unwrap().interval,
                v.wrapping_mul(1_000_000)
            );
        }
        let mut p = [i64::MIN; 6];
        p[1] = 0;
        let (_, t) = run(
            Request {
                op: 2,
                params: p,
                ..Default::default()
            },
            |_| ok(),
        );
        assert_eq!(
            t.iter().find(|e| e.kind == 17).unwrap().params,
            [1000, 10, 1, 10, 0, 1]
        );
        let (_, t) = run(
            Request {
                op: 2,
                params: [i64::MAX; 6],
                ..Default::default()
            },
            |_| ok(),
        );
        assert_eq!(
            t.iter().find(|e| e.kind == 17).unwrap().params,
            [15000, 600, 10, 3600, 600, 10]
        );
    }
}
