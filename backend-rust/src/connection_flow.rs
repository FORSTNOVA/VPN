//! Connection orchestration only. Effects are supplied by a locked Go adapter.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Region {
    pub name: Vec<u8>,
    pub country: Vec<u8>,
    pub verified: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub chain: bool,
    pub proxy_on: bool,
    pub store: bool,
    pub mode: Vec<u8>,
    pub selection_mode: Vec<u8>,
    pub selected_node: Vec<u8>,
    pub locked_region: Vec<u8>,
    pub locked_node: Vec<u8>,
    pub cached_names: Vec<Vec<u8>>,
    pub choice: Vec<u8>,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub name: Vec<u8>,
    pub valid_name: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
    pub items: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub process_done: bool,
    pub error: Vec<u8>,
    pub name: Vec<u8>,
    pub names: Vec<Vec<u8>>,
    pub value: i64,
    pub snapshot: Option<Snapshot>,
}
// Effects:1 Lock,2 Snapshot(borrowed),3 Unlock,4 Running,5 ProxyConflict,
// 6 Start,7 Stop,8 List,9 Put(data),10 Get(data group),11 SaveSettings,
// 12 CacheNodes(data choice),13 SaveCache,14 StoreNodes,15 Candidates(data region),
// 16 FreshRegion(data node),17 SetRegion(data node,text country),18 SetLockedNode,
// 19 Selection(data mode,text selected),20 MarkCached(data chosen),21 ClearBlocked,
// 22 SetProxy(a bool),23 ProxyFlag(a bool),24 StartPatrol,25 StartPath,26 AsyncPatrol,
// 27 StopPatrol,28 StopPath,29 Latency(data selected),30 Log(a code,data context,text err),
// 63 Result(a status,b code,data selected,text rawerror,items[region,mode,autoBool,latency]),0 Done.
const AUTO: &[u8] = b"SmartVPNAuto";
const FALLBACK: &[u8] = b"SmartVPNFallback";
const CHAIN: &[u8] = b"SmartVPNChain";
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    stage: u8,
    nodes: Vec<Vec<u8>>,
    choice: Vec<u8>,
    selected: Vec<u8>,
    auto: bool,
    latency: i64,
    error: Vec<u8>,
    process_done: bool,
    has_error: bool,
    pending: (i64, i64),
    region_node: Vec<u8>,
    region_country: Vec<u8>,
    region_after: u8,
    after_unlock: u8,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 3 {
            return Err("unknown connection op");
        }
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            stage: 0,
            nodes: vec![],
            choice: vec![],
            selected: vec![],
            auto: false,
            latency: 0,
            error: vec![],
            process_done: false,
            has_error: false,
            pending: (0, 0),
            region_node: vec![],
            region_country: vec![],
            region_after: 0,
            after_unlock: 0,
        };
        if f.q.op == 3 && !f.q.valid_name {
            f.result(400, 1);
        } else {
            f.emit(if f.q.op == 2 { 2 } else { 1 }, 1);
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
    fn named(&mut self, kind: u8, stage: u8, name: Vec<u8>) {
        self.emit(kind, stage);
        self.e.data = name;
    }
    fn result(&mut self, status: i64, code: i64) {
        self.emit(63, 250);
        self.e.a = status;
        self.e.b = code;
        self.e.data = self.selected.clone();
        self.e.text = self.error.clone();
        if code == 0 && self.q.op == 0 {
            self.e.items = vec![
                self.s.locked_region.clone(),
                self.s.mode.clone(),
                vec![self.auto as u8],
                self.latency.to_le_bytes().to_vec(),
            ];
        }
    }
    fn stop_fail(&mut self, status: i64, code: i64) {
        self.pending = (status, code);
        self.emit(7, 99);
    }
    fn is_group(v: &[u8]) -> bool {
        v == AUTO || v == FALLBACK
    }
    fn selection(&mut self, after: u8) {
        let name = self.q.name.clone();
        let (mode, node) = if name == AUTO {
            (b"auto".to_vec(), vec![])
        } else if name == FALLBACK {
            (b"fallback".to_vec(), vec![])
        } else {
            (b"manual".to_vec(), name)
        };
        self.s.selection_mode = mode.clone();
        self.s.selected_node = node.clone();
        self.emit(19, after);
        self.e.data = mode;
        self.e.text = node;
    }
    fn unlock(&mut self, after: u8) {
        self.after_unlock = after;
        self.emit(3, 90);
    }
    fn region(&mut self, node: Vec<u8>, after: u8) {
        self.region_after = after;
        self.region_node = node.clone();
        if self.s.chain || node.is_empty() {
            self.after_region();
            return;
        }
        self.named(31, 69, node);
    }

    fn after_region(&mut self) {
        match self.region_after {
            1 => self.after_connect_region(),
            2 => {
                self.named(20, 43, self.q.name.clone());
            }
            3 => self.selection(48),
            _ => unreachable!(),
        }
    }
    fn after_connect_region(&mut self) {
        if !self.s.chain
            && !self.s.locked_region.is_empty()
            && !self.s.locked_node.is_empty()
            && self.s.locked_node != self.choice
        {
            self.named(9, 22, self.s.locked_node.clone());
        } else {
            self.after_pin();
        }
    }
    fn after_pin(&mut self) {
        if self.s.locked_region.is_empty() {
            self.emit(30, 23);
            self.e.a = 3;
        } else {
            self.emit(21, 24);
        }
    }
    fn choose(&mut self) {
        self.choice = self.s.choice.clone();
        if self.s.chain {
            self.choice = CHAIN.to_vec();
            self.named(9, 11, self.choice.clone());
        } else if !self.s.locked_region.is_empty() {
            if !self.s.locked_node.is_empty() && self.nodes.contains(&self.s.locked_node) {
                self.choice = self.s.locked_node.clone();
                self.named(9, 11, self.choice.clone());
            } else {
                self.named(15, 9, self.s.locked_region.clone());
            }
        } else if self.s.selection_mode == b"manual" && !self.nodes.contains(&self.choice) {
            self.choice = AUTO.to_vec();
            self.auto = true;
            self.s.selection_mode = b"auto".to_vec();
            self.s.selected_node.clear();
            self.emit(19, 10);
            self.e.data = b"auto".to_vec();
        } else {
            if self.choice.is_empty() && self.s.selection_mode != b"manual" {
                self.choice = AUTO.to_vec();
            }
            self.named(9, 11, self.choice.clone());
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected connection reply");
        }
        if matches!(self.e.kind, 1 | 2) && r.snapshot.is_none() {
            return Err("missing locked snapshot");
        }
        if let Some(s) = r.snapshot.clone() {
            self.s = s;
        }
        match self.stage {
            1 => {
                if self.q.op == 1 || self.q.op == 2 {
                    self.emit(27, 30);
                } else {
                    self.emit(4, 2);
                }
            }
            2 => {
                if self.q.op == 3 {
                    if self.s.chain {
                        self.pending = (409, 2);
                        self.unlock(1);
                    } else if r.flag {
                        self.unlock(2);
                    } else if Self::is_group(&self.q.name)
                        || self.s.cached_names.contains(&self.q.name)
                    {
                        self.selection(40);
                    } else {
                        self.pending = (409, 3);
                        self.unlock(1);
                    }
                } else if r.flag {
                    self.result(200, 20);
                } else if self.s.mode == b"system-proxy" {
                    self.emit(5, 3);
                } else {
                    self.emit(6, 4);
                }
            }
            3 => {
                if r.flag {
                    self.result(409, 4);
                } else {
                    self.emit(6, 4);
                }
            }
            4 => {
                if !r.ok {
                    self.error = r.error;
                    self.result(502, 5);
                } else {
                    self.emit(8, 5);
                }
            }
            5 => {
                if !r.ok || r.names.is_empty() {
                    self.stop_fail(502, 6);
                } else {
                    self.nodes = r.names;
                    self.choose();
                }
            }
            9 => {
                if let Some(n) = r.names.first() {
                    self.choice = n.clone();
                    self.s.locked_node = n.clone();
                    self.named(18, 10, n.clone());
                } else {
                    self.stop_fail(502, 7);
                }
            }
            10 => self.named(9, 11, self.choice.clone()),
            11 => {
                if !r.ok {
                    self.stop_fail(502, 8);
                } else {
                    self.named(10, 12, b"SmartVPN".to_vec());
                }
            }
            12 => {
                if !r.ok || !r.flag || r.name != self.choice {
                    self.stop_fail(502, 9);
                } else {
                    self.selected = self.choice.clone();
                    if Self::is_group(&self.choice) {
                        self.named(10, 13, self.choice.clone());
                    } else {
                        self.emit(11, 14);
                    }
                }
            }
            13 => {
                if r.ok && !r.name.is_empty() {
                    self.selected = r.name;
                }
                self.emit(11, 14);
            }
            14 => {
                if !r.ok {
                    self.stop_fail(500, 10);
                } else {
                    self.named(12, 15, self.choice.clone());
                }
            }
            15 => self.emit(13, 16),
            16 => {
                if !r.ok {
                    self.stop_fail(500, 11);
                } else if self.s.store {
                    self.emit(14, 17);
                } else {
                    self.region(self.selected.clone(), 1);
                }
            }
            17 => {
                if !r.ok {
                    self.emit(30, 18);
                    self.e.a = 1;
                    self.e.text = r.error;
                } else {
                    self.region(self.selected.clone(), 1);
                }
            }
            18 => self.region(self.selected.clone(), 1),
            22 => {
                if r.ok {
                    self.choice = self.s.locked_node.clone();
                    self.selected = self.choice.clone();
                    self.after_pin();
                } else {
                    self.emit(30, 23);
                    self.e.a = 2;
                    self.e.data = self.s.locked_node.clone();
                    self.e.text = r.error;
                }
            }
            23 => {
                if self.e.a == 2 && self.s.locked_region.is_empty() {
                    self.emit(30, 23);
                    self.e.a = 3;
                } else {
                    self.emit(21, 24);
                }
            }
            24 => {
                if self.s.mode == b"system-proxy" {
                    self.emit(22, 25);
                    self.e.a = 1;
                } else {
                    self.s.proxy_on = false;
                    self.emit(23, 27);
                    self.e.a = 0;
                }
            }
            25 => {
                if !r.ok {
                    self.emit(22, 26);
                    self.e.a = 0;
                } else {
                    self.s.proxy_on = true;
                    self.emit(23, 27);
                    self.e.a = 1;
                }
            }
            26 => self.stop_fail(500, 12),
            27 => self.emit(24, 28),
            28 => self.emit(25, 29),
            29 => self.emit(26, 80),
            80 => self.named(29, 81, self.selected.clone()),
            81 => {
                self.latency = r.value.max(0);
                self.result(200, 0);
            }
            30 => self.emit(28, 31),
            31 => self.emit(7, 32),
            32 => {
                if !r.ok {
                    self.has_error = true;
                    self.error = r.error;
                    self.process_done = r.process_done;
                }
                if self.s.proxy_on {
                    self.emit(22, 33);
                    self.e.a = 0;
                } else {
                    self.s.proxy_on = false;
                    self.emit(23, 34);
                }
            }
            33 => {
                let restore_failed = !r.ok;
                if !self.has_error && !r.ok {
                    self.has_error = true;
                    self.error = r.error;
                    self.process_done = r.process_done;
                }
                self.s.proxy_on = restore_failed;
                self.emit(23, 34);
                self.e.a = i64::from(restore_failed);
            }
            34 => {
                if self.q.op == 2 {
                    self.result(
                        if !self.has_error { 200 } else { 500 },
                        if !self.has_error { 20 } else { 21 },
                    );
                    self.e.a = if self.process_done { 1 } else { 0 };
                } else if !self.has_error || self.process_done {
                    self.result(200, 20);
                } else {
                    self.result(500, 13);
                }
            }
            40 => {
                if Self::is_group(&self.q.name) {
                    self.named(20, 43, self.q.name.clone());
                } else {
                    self.region(self.q.name.clone(), 2);
                }
            }
            43 => self.emit(11, 44),
            44 => {
                self.pending = if r.ok { (200, 22) } else { (500, 14) };
                self.unlock(1);
            }
            45 => {
                if !r.ok {
                    self.result(502, 15);
                } else if Self::is_group(&self.q.name) || r.names.contains(&self.q.name) {
                    self.named(9, 46, self.q.name.clone());
                } else {
                    self.result(400, 16);
                }
            }
            46 => {
                if !r.ok {
                    self.result(502, 17);
                } else {
                    self.emit(1, 47);
                }
            }
            47 => {
                if Self::is_group(&self.q.name) {
                    self.named(10, 50, b"SmartVPN".to_vec());
                } else {
                    self.region(self.q.name.clone(), 3);
                }
            }
            48 => self.emit(11, 49),
            49 => {
                self.pending = (200, 20);
                self.unlock(1);
            }
            50 => {
                if !r.ok {
                    self.region(vec![], 3);
                } else if Self::is_group(&r.name) {
                    self.selected = r.name.clone();
                    self.named(10, 51, r.name);
                } else {
                    self.region(r.name, 3);
                }
            }
            51 => {
                let n = if r.ok { r.name } else { self.selected.clone() };
                self.region(n, 3);
            }
            69 => {
                if !r.flag || r.value != 1 {
                    self.after_region();
                } else {
                    self.region_country = r.name;
                    self.named(16, 70, self.region_node.clone());
                }
            }
            70 => {
                if !r.flag
                    || (self.s.locked_region == self.region_country
                        && self.s.locked_node == self.region_node)
                {
                    self.after_region();
                } else {
                    self.s.locked_region = self.region_country.clone();
                    self.s.locked_node = self.region_node.clone();
                    self.named(17, 71, self.region_node.clone());
                    self.e.text = self.region_country.clone();
                }
            }
            71 => self.emit(11, 72),
            72 => {
                if !r.ok {
                    self.emit(30, 73);
                    self.e.a = 4;
                    self.e.text = r.error;
                } else {
                    self.after_region();
                }
            }
            73 => self.after_region(),
            90 => {
                if self.after_unlock == 1 {
                    self.result(self.pending.0, self.pending.1);
                } else {
                    self.emit(8, 45);
                }
            }
            99 => self.result(self.pending.0, self.pending.1),
            250 => self.emit(0, 251),
            _ => return Err("invalid connection stage"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn snap() -> Snapshot {
        Snapshot {
            mode: b"tun".to_vec(),
            selection_mode: b"auto".to_vec(),
            choice: AUTO.to_vec(),
            cached_names: vec![b"n".to_vec()],
            ..Snapshot::default()
        }
    }
    fn reply(e: &Effect, s: &Snapshot) -> Reply {
        let mut r = Reply {
            kind: e.kind,
            ok: true,
            ..Reply::default()
        };
        match e.kind {
            1 | 2 => r.snapshot = Some(s.clone()),
            8 => r.names = vec![b"n".to_vec()],
            10 => {
                r.flag = true;
                r.name = if e.data == b"SmartVPN" {
                    AUTO.to_vec()
                } else {
                    b"n".to_vec()
                }
            }
            15 => r.names = vec![b"n".to_vec()],
            16 => r.flag = true,
            29 => r.value = 42,
            _ => {}
        }
        r
    }
    fn run(op: u8, s: Snapshot, mut alter: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(Request {
            op,
            name: b"n".to_vec(),
            valid_name: true,
        })
        .unwrap();
        let mut trace = vec![];
        for _ in 0..100 {
            let e = f.next();
            if e.kind == 0 {
                return trace;
            }
            let mut r = reply(&e, &s);
            alter(&e, &mut r);
            trace.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop");
    }
    fn end(t: &[Effect]) -> &Effect {
        t.last().unwrap()
    }
    #[test]
    fn normal_connect_order() {
        let t = run(0, snap(), |_, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 4, 6, 8, 9, 10, 10, 11, 12, 13, 31, 30, 21, 23, 24, 25, 26, 29, 63]
        );
        assert_eq!(end(&t).a, 200);
        assert_eq!(end(&t).data, b"n");
    }
    #[test]
    fn already_running_and_conflict_precede_start() {
        let t = run(0, snap(), |e, r| {
            if e.kind == 4 {
                r.flag = true
            }
        });
        assert_eq!(t.len(), 3);
        assert_eq!(end(&t).b, 20);
        let mut s = snap();
        s.mode = b"system-proxy".to_vec();
        let t = run(0, s, |e, r| {
            if e.kind == 5 {
                r.flag = true
            }
        });
        assert_eq!(end(&t).a, 409);
        assert!(!t.iter().any(|e| e.kind == 6));
    }
    #[test]
    fn critical_failure_stops_once_but_start_failure_does_not() {
        for (kind, code, status) in [
            (6, 5, 502),
            (8, 6, 502),
            (9, 8, 502),
            (10, 9, 502),
            (11, 10, 500),
            (13, 11, 500),
        ] {
            let mut first = true;
            let t = run(0, snap(), |e, r| {
                if first && e.kind == kind {
                    r.ok = false;
                    r.error = b"fault".to_vec();
                    first = false;
                }
            });
            assert_eq!((end(&t).a, end(&t).b), (status, code));
            assert_eq!(
                t.iter().filter(|e| e.kind == 7).count(),
                usize::from(kind != 6)
            );
        }
    }
    #[test]
    fn manual_missing_mutates_before_put_and_never_rolls_back() {
        let mut s = snap();
        s.selection_mode = b"manual".to_vec();
        s.choice = b"gone".to_vec();
        let t = run(0, s, |e, r| {
            if e.kind == 9 {
                r.ok = false
            }
        });
        let i = t.iter().position(|e| e.kind == 19).unwrap();
        assert_eq!(t[i].data, b"auto");
        assert!(t[i].text.is_empty());
        assert_eq!(t[i + 1].kind, 9);
        assert_eq!(t.iter().filter(|e| e.kind == 19).count(), 1);
    }
    #[test]
    fn chain_wins_region_and_manual() {
        let mut s = snap();
        s.chain = true;
        s.locked_region = b"US".to_vec();
        let t = run(0, s, |e, r| {
            if e.kind == 10 {
                r.name = CHAIN.to_vec()
            }
        });
        assert_eq!(t.iter().find(|e| e.kind == 9).unwrap().data, CHAIN);
        assert!(!t.iter().any(|e| e.kind == 15 || e.kind == 31));
    }
    #[test]
    fn region_existing_node_no_ttl_and_candidate_not_required_in_list() {
        let mut s = snap();
        s.locked_region = b"US".to_vec();
        s.locked_node = b"n".to_vec();
        let t = run(0, s.clone(), |e, r| {
            if e.kind == 10 {
                r.name = b"n".to_vec()
            }
        });
        assert!(!t.iter().any(|e| e.kind == 15));
        s.locked_node = b"gone".to_vec();
        let t = run(0, s, |e, r| {
            if e.kind == 15 {
                r.names = vec![b"outside-list".to_vec()]
            };
            if e.kind == 10 {
                r.name = b"outside-list".to_vec()
            }
        });
        assert_eq!(
            t.iter().find(|e| e.kind == 18).unwrap().data,
            b"outside-list"
        );
        assert_eq!(end(&t).a, 200);
    }
    #[test]
    fn system_proxy_failure_undo_then_stop() {
        let mut s = snap();
        s.mode = b"system-proxy".to_vec();
        let t = run(0, s, |e, r| {
            if e.kind == 22 {
                r.ok = false
            }
        });
        let i = t.iter().position(|e| e.kind == 22).unwrap();
        assert_eq!(
            (t[i].a, t[i + 1].kind, t[i + 1].a, t[i + 2].kind),
            (1, 22, 0, 7)
        );
        assert_eq!(end(&t).b, 12);
        assert!(!t.iter().any(|e| e.kind == 24));
    }
    #[test]
    fn drop_order_kernel_error_priority_processdone() {
        for op in [1, 2] {
            let mut s = snap();
            s.proxy_on = true;
            let t = run(op, s, |e, r| {
                if e.kind == 7 || e.kind == 22 {
                    r.ok = false;
                    r.error = if e.kind == 7 {
                        b"kernel".to_vec()
                    } else {
                        b"proxy".to_vec()
                    };
                    r.process_done = e.kind == 7;
                }
            });
            assert_eq!(
                t.iter().map(|e| e.kind).collect::<Vec<_>>(),
                vec![if op == 2 { 2 } else { 1 }, 27, 28, 7, 22, 23, 63]
            );
            if op == 1 {
                assert_eq!(end(&t).a, 200);
            } else {
                assert_eq!(end(&t).text, b"kernel");
                assert_eq!(end(&t).a, 1);
            }
        }
    }
    #[test]
    fn select_pending_save_error_unlocks_with_partial_commits() {
        let t = run(3, snap(), |e, r| {
            if e.kind == 11 {
                r.ok = false
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 4, 19, 31, 20, 11, 3, 63]
        );
        assert_eq!((end(&t).a, end(&t).b), (500, 14));
    }
    #[test]
    fn select_connected_relocks_current_chain_and_ignores_save_failure() {
        let mut locks = 0;
        let t = run(3, snap(), |e, r| {
            if e.kind == 4 {
                r.flag = true
            };
            if e.kind == 1 {
                locks += 1;
                if locks == 2 {
                    r.snapshot.as_mut().unwrap().chain = true;
                }
            }
            if e.kind == 11 {
                r.ok = false;
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 4, 3, 8, 9, 1, 19, 11, 3, 63]
        );
        assert_eq!(end(&t).a, 200);
    }
    #[test]
    fn invalid_reply_does_not_change_flow() {
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
    #[test]
    fn region_save_failure_is_ignored_then_pin_failure_is_logged() {
        let mut saves = 0;
        let mut puts = 0;
        let t = run(0, snap(), |e, r| {
            if e.kind == 31 {
                r.flag = true;
                r.value = 1;
                r.name = b"US".to_vec();
            }
            if e.kind == 11 {
                saves += 1;
                if saves == 2 {
                    r.ok = false;
                    r.error = b"save region".to_vec();
                }
            }
            if e.kind == 9 {
                puts += 1;
                if puts == 2 {
                    r.ok = false;
                    r.error = b"pin".to_vec();
                }
            }
        });
        assert_eq!(end(&t).a, 200);
        assert!(t.iter().any(|e| e.kind == 30 && e.a == 4));
        assert!(t.iter().any(|e| e.kind == 30 && e.a == 2));
        assert_eq!(end(&t).data, b"n");
        assert!(!t.iter().any(|e| e.kind == 7));
    }
    #[test]
    fn stale_region_never_mutates_or_saves_extra() {
        let t = run(0, snap(), |e, r| {
            if e.kind == 31 {
                r.flag = true;
                r.value = 1;
                r.name = b"US".to_vec();
            }
            if e.kind == 16 {
                r.flag = false;
            }
        });
        assert!(!t.iter().any(|e| e.kind == 17));
        assert_eq!(t.iter().filter(|e| e.kind == 11).count(), 1);
    }
    #[test]
    fn existing_empty_manual_node_and_nonstring_confirmation_differ() {
        let mut s = snap();
        s.selection_mode = b"manual".to_vec();
        s.choice.clear();
        for string in [true, false] {
            let t = run(0, s.clone(), |e, r| {
                if e.kind == 8 {
                    r.names = vec![vec![]];
                }
                if e.kind == 10 {
                    r.name.clear();
                    r.flag = string;
                }
            });
            assert!(t.iter().find(|e| e.kind == 9).unwrap().data.is_empty());
            assert_eq!(end(&t).a, if string { 200 } else { 502 });
        }
    }
    #[test]
    fn empty_error_text_is_still_a_failed_drop() {
        let t = run(1, snap(), |e, r| {
            if e.kind == 7 {
                r.ok = false;
                r.error.clear();
            }
        });
        assert_eq!(end(&t).a, 500);
    }
}
