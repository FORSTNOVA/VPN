//! Manual-node lifecycle. Arbitrary proxy maps and serialized bodies remain in Go arenas.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Node {
    pub token: i64,
    pub name: Vec<u8>,
    pub uri_nonempty: bool,
    pub proxy_len: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub store: bool,
    pub nodes: Vec<Node>,
    pub cached_names: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub borrowed: bool,
    pub name: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
    pub indices: Vec<i64>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub token: i64,
    pub value: i64,
    pub error: Vec<u8>,
    pub node: Node,
    pub nodes: Vec<Node>,
    pub snapshot: Option<Snapshot>,
}
// ops0load1save2provider3list4preview5add6delete; borrowed avoids lock/running.
// 1Lock,2SnapshotBorrowed,3Running,4ParseURI(data),5RenameNode(a token,b alsoProxy,data),
// 6NowUnix,7Append(a token,b timestamp; b ignored forload),8PopRollback,
// 9Replace(indices oldstate retainedGo; returnsbackup token),10Restore(a backup),
// 11ClearNil,12ReadStore,13Decode(a body),14LogUnreadable,15Encode,
// 16WriteStore(a body),17YAML,18Mkdir(a0700),19RemoveProvider,
// 20WriteTemp(a body,b0600),21RenameTemp,63Result(aHTTP,bcode,data name,text error,indices resulttokens),0Done.
// result0ok1raw2running3missingname4dupmanual5dupcache6addsave7addprovider8notfound9delsave10delprovider11list12node.
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    stage: u8,
    node: Node,
    loaded: Vec<Node>,
    load_index: usize,
    save_after: u8,
    provider_after: u8,
    body: i64,
    backup: i64,
    result_code: i64,
    error: Vec<u8>,
    error_token: i64,
    running: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 6 {
            return Err("unknown manual operation");
        }
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            stage: 0,
            node: Node::default(),
            loaded: vec![],
            load_index: 0,
            save_after: 0,
            provider_after: 0,
            body: 0,
            backup: 0,
            result_code: 0,
            error: vec![],
            error_token: 0,
            running: false,
        };
        if f.q.op == 6 && !f.q.borrowed && f.q.name.is_empty() {
            f.result(400, 3);
        } else if f.q.op == 4 {
            f.emit(4, 10);
        } else {
            f.emit(if f.q.borrowed { 2 } else { 1 }, 1);
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
    fn result(&mut self, status: i64, code: i64) {
        self.emit(63, 99);
        self.e.a = if self.q.op <= 2 { 0 } else { status };
        self.e.b = code;
        self.e.data = self.node.name.clone();
        self.e.text = self.error.clone();
        if code == 1 {
            self.e.indices = vec![self.error_token];
        } else if code == 12 {
            self.e.indices = vec![self.node.token];
        } else if code == 11 {
            self.e.indices = self.s.nodes.iter().map(|n| n.token).collect();
            self.e.data = vec![self.running as u8];
        }
    }
    fn raw_fail(&mut self, r: &Reply) {
        self.error = r.error.clone();
        self.error_token = r.token;
        self.result(400, 1);
    }
    fn begin_add(&mut self) {
        self.emit(4, 10);
    }
    fn begin_delete(&mut self) {
        let keep: Vec<_> = self
            .s
            .nodes
            .iter()
            .enumerate()
            .filter(|(_, n)| n.name != self.q.name)
            .map(|(i, _)| i)
            .collect();
        if keep.len() == self.s.nodes.len() {
            self.result(400, 8);
        } else {
            self.emit(9, 20);
            self.e.indices = keep.iter().map(|i| *i as i64).collect();
            self.s.nodes = keep.into_iter().map(|i| self.s.nodes[i].clone()).collect();
        }
    }
    fn duplicates(&mut self) {
        if self.s.nodes.iter().any(|n| n.name == self.node.name) {
            self.result(400, 4);
        } else if self.s.cached_names.contains(&self.node.name) {
            self.result(400, 5);
        } else {
            self.emit(6, 13);
        }
    }
    fn save(&mut self, after: u8) {
        self.save_after = after;
        if self.s.store {
            self.emit(15, 31);
        } else {
            self.saved();
        }
    }
    fn save_failed(&mut self, r: &Reply) {
        self.error = r.error.clone();
        self.error_token = r.token;
        match self.save_after {
            0 => self.result(400, 1),
            1 => {
                self.result_code = 6;
                self.s.nodes.pop();
                self.emit(8, 35);
            }
            2 => {
                self.result_code = 9;
                self.emit(10, 35);
                self.e.a = self.backup;
            }
            _ => unreachable!(),
        }
    }
    fn saved(&mut self) {
        match self.save_after {
            0 => self.result(200, 0),
            1 => self.provider(1),
            2 => self.provider(2),
            _ => unreachable!(),
        }
    }
    fn provider(&mut self, after: u8) {
        self.provider_after = after;
        if self.s.nodes.is_empty() {
            self.emit(19, 40);
        } else {
            self.emit(17, 41);
        }
    }
    fn provider_failed(&mut self, r: &Reply) {
        self.error = r.error.clone();
        self.error_token = r.token;
        self.result(
            400,
            match self.provider_after {
                0 => 1,
                1 => 7,
                2 => 10,
                _ => unreachable!(),
            },
        );
    }
    fn provided(&mut self) {
        if self.provider_after == 1 {
            self.result(200, 12);
        } else {
            self.result(200, 0);
        }
    }
    fn next_loaded(&mut self) {
        while self.load_index < self.loaded.len() {
            let n = self.loaded[self.load_index].clone();
            if n.name.is_empty() || !n.uri_nonempty || n.proxy_len == 0 {
                self.load_index += 1;
                continue;
            }
            self.node = n;
            self.emit(5, 54);
            self.e.a = self.node.token;
            self.e.b = 1;
            self.e.data = self.node.name.clone();
            return;
        }
        self.result(200, 0);
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected manual reply");
        }
        if matches!(self.e.kind, 1 | 2) && r.snapshot.is_none() {
            return Err("missing manual snapshot");
        }
        if let Some(s) = r.snapshot.clone() {
            self.s = s;
        }
        match self.stage {
            1 => match self.q.op {
                0 => {
                    self.s.nodes.clear();
                    self.emit(11, 50);
                }
                1 => self.save(0),
                2 => self.provider(0),
                3 => self.emit(3, 3),
                5 | 6 => {
                    if self.q.borrowed {
                        if self.q.op == 5 {
                            self.begin_add();
                        } else {
                            self.begin_delete();
                        }
                    } else {
                        self.emit(3, 2);
                    }
                }
                _ => unreachable!(),
            },
            2 => {
                if r.flag {
                    self.result(409, 2);
                } else if self.q.op == 5 {
                    self.begin_add();
                } else {
                    self.begin_delete();
                }
            }
            3 => {
                self.running = r.flag;
                self.result(200, 11);
            }
            10 => {
                if !r.ok {
                    self.raw_fail(&r);
                } else {
                    self.node = r.node;
                    if self.q.name.is_empty() {
                        if self.q.op == 4 {
                            self.result(200, 12);
                        } else {
                            self.duplicates();
                        }
                    } else {
                        self.node.name = self.q.name.clone();
                        self.emit(5, 11);
                        self.e.a = self.node.token;
                        self.e.b = (self.q.op != 4) as i64;
                        self.e.data = self.q.name.clone();
                    }
                }
            }
            11 => {
                if self.q.op == 4 {
                    self.result(200, 12);
                } else {
                    self.duplicates();
                }
            }
            13 => {
                self.s.nodes.push(self.node.clone());
                self.emit(7, 14);
                self.e.a = self.node.token;
                self.e.b = r.value;
                self.e.data = vec![1];
            }
            14 => self.save(1),
            20 => {
                self.backup = r.token;
                self.save(2);
            }
            31 => {
                if !r.ok {
                    self.save_failed(&r);
                } else {
                    self.emit(16, 32);
                    self.e.a = r.token;
                }
            }
            32 => {
                if !r.ok {
                    self.save_failed(&r);
                } else {
                    self.saved();
                }
            }
            35 => self.result(400, self.result_code),
            40 => {
                if !r.ok && !r.flag {
                    self.provider_failed(&r);
                } else {
                    self.provided();
                }
            }
            41 => {
                if !r.ok {
                    self.provider_failed(&r);
                } else {
                    self.body = r.token;
                    self.emit(18, 42);
                    self.e.a = 0o700;
                }
            }
            42 => {
                if !r.ok {
                    self.provider_failed(&r);
                } else {
                    self.emit(20, 43);
                    self.e.a = self.body;
                    self.e.b = 0o600;
                }
            }
            43 => {
                if !r.ok {
                    self.provider_failed(&r);
                } else {
                    self.emit(21, 44);
                }
            }
            44 => {
                if !r.ok {
                    self.provider_failed(&r);
                } else {
                    self.provided();
                }
            }
            50 => {
                if !self.s.store {
                    self.result(200, 0);
                } else {
                    self.emit(12, 51);
                }
            }
            51 => {
                if !r.ok || !r.flag {
                    self.result(200, 0);
                } else {
                    self.emit(13, 52);
                    self.e.a = r.token;
                }
            }
            52 => {
                if !r.ok {
                    self.emit(14, 53);
                } else {
                    self.loaded = r.nodes;
                    self.next_loaded();
                }
            }
            53 => self.result(200, 0),
            54 => {
                self.s.nodes.push(self.node.clone());
                self.emit(7, 55);
                self.e.a = self.node.token;
                self.e.b = 0;
                self.e.data = vec![0];
            }
            55 => {
                self.load_index += 1;
                self.next_loaded();
            }
            99 => self.emit(0, 100),
            _ => return Err("invalid manual stage"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn node(token: i64, name: &[u8]) -> Node {
        Node {
            token,
            name: name.to_vec(),
            uri_nonempty: true,
            proxy_len: 1,
        }
    }
    fn snap() -> Snapshot {
        Snapshot {
            store: true,
            nodes: vec![node(1, b"old")],
            cached_names: vec![b"cached".to_vec()],
        }
    }
    fn req(op: u8) -> Request {
        Request {
            op,
            borrowed: true,
            name: if op == 6 { b"old".to_vec() } else { vec![] },
        }
    }
    fn reply(e: &Effect) -> Reply {
        Reply {
            kind: e.kind,
            ok: true,
            flag: true,
            token: 100,
            value: 4294967296,
            node: node(9, b"new"),
            nodes: vec![node(10, b"loaded")],
            snapshot: if matches!(e.kind, 1 | 2) {
                Some(snap())
            } else {
                None
            },
            ..Reply::default()
        }
    }
    fn run(q: Request, mut change: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(q).unwrap();
        let mut out = vec![];
        for _ in 0..100 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = reply(&e);
            change(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop")
    }
    #[test]
    fn add_full_order_timestamp_and_modes() {
        let t = run(req(5), |_, _| {});
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![2, 4, 6, 7, 15, 16, 17, 18, 20, 21, 63]
        );
        let append = t.iter().find(|e| e.kind == 7).unwrap();
        assert_eq!(append.b, 4294967296);
        assert_eq!(append.data, vec![1]);
        assert_eq!(t.iter().find(|e| e.kind == 18).unwrap().a, 0o700);
        assert_eq!(t.iter().find(|e| e.kind == 20).unwrap().b, 0o600);
    }
    #[test]
    fn duplicate_manual_precedes_cached_and_clock() {
        for (name, code) in [(b"old".as_slice(), 4), (b"cached", 5)] {
            let t = run(req(5), |e, r| {
                if e.kind == 2 {
                    r.snapshot
                        .as_mut()
                        .unwrap()
                        .cached_names
                        .push(b"old".to_vec());
                }
                if e.kind == 4 {
                    r.node.name = name.to_vec();
                }
            });
            assert_eq!(t.last().unwrap().b, code);
            assert!(!t.iter().any(|e| e.kind == 6 || e.kind == 7));
        }
    }
    #[test]
    fn preview_override_does_not_change_proxy_or_lock() {
        let mut q = req(4);
        q.name = b"override".to_vec();
        let t = run(q, |_, _| {});
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![4, 5, 63]);
        assert_eq!(t[1].b, 0);
        assert_eq!(t.last().unwrap().data, b"override");
    }
    #[test]
    fn http_running_precedes_parse_and_missing_delete_precedes_lock() {
        let mut q = req(5);
        q.borrowed = false;
        let t = run(q, |_, _| {});
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 3, 63]);
        assert_eq!(t.last().unwrap().a, 409);
        let mut q = req(6);
        q.borrowed = false;
        q.name.clear();
        let t = run(q, |_, _| {});
        assert_eq!(t.len(), 1);
        assert_eq!(t[0].b, 3);
    }
    #[test]
    fn save_failure_rolls_back_add_only_once() {
        for fail in [15, 16] {
            let t = run(req(5), |e, r| {
                if e.kind == fail {
                    r.ok = false;
                }
            });
            assert_eq!(t.last().unwrap().b, 6);
            assert_eq!(t.iter().filter(|e| e.kind == 8).count(), 1);
            assert!(!t.iter().any(|e| e.kind == 17));
        }
    }
    #[test]
    fn provider_failure_keeps_add_and_leaves_temp() {
        for fail in [17, 18, 20, 21] {
            let t = run(req(5), |e, r| {
                if e.kind == fail {
                    r.ok = false;
                }
            });
            assert_eq!(t.last().unwrap().b, 7);
            assert!(!t
                .iter()
                .any(|e| e.kind == 8 || e.kind == 10 || e.kind == 19));
        }
    }
    #[test]
    fn delete_all_matches_preserves_order_and_restores_on_savefailure() {
        let t = run(req(6), |e, r| {
            if e.kind == 2 {
                r.snapshot.as_mut().unwrap().nodes = vec![
                    node(1, b"old"),
                    node(2, b"keep"),
                    node(3, b"old"),
                    node(4, b"last"),
                ];
            }
            if e.kind == 16 {
                r.ok = false;
            }
        });
        assert_eq!(t.iter().find(|e| e.kind == 9).unwrap().indices, vec![1, 3]);
        assert_eq!(t.iter().find(|e| e.kind == 10).unwrap().a, 100);
        assert_eq!(t.last().unwrap().b, 9);
    }
    #[test]
    fn delete_all_empty_kept_array_remove_notfound_ignored() {
        let t = run(req(6), |e, r| {
            if e.kind == 19 {
                r.ok = false;
                r.flag = true;
            }
        });
        assert!(t.iter().find(|e| e.kind == 9).unwrap().indices.is_empty());
        assert!(t.iter().any(|e| e.kind == 19));
        assert!(!t.iter().any(|e| e.kind == 17));
        assert_eq!(t.last().unwrap().b, 0);
        let t = run(req(6), |e, r| {
            if e.kind == 19 {
                r.ok = false;
                r.flag = false;
            }
        });
        assert_eq!(t.last().unwrap().b, 10);
        assert!(!t.iter().any(|e| e.kind == 10));
    }
    #[test]
    fn load_filters_only_empty_fields_and_keeps_duplicates_whitespace() {
        let t = run(req(0), |e, r| {
            if e.kind == 13 {
                r.nodes = vec![
                    node(2, b""),
                    Node {
                        uri_nonempty: false,
                        ..node(3, b"bad")
                    },
                    Node {
                        proxy_len: 0,
                        ..node(4, b"bad")
                    },
                    node(5, b" "),
                    node(6, b"same"),
                    node(7, b"same"),
                ];
            }
        });
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 7)
                .map(|e| e.a)
                .collect::<Vec<_>>(),
            vec![5, 6, 7]
        );
        assert!(t.iter().filter(|e| e.kind == 7).all(|e| e.data == [0]));
        assert!(!t.iter().any(|e| e.kind == 4 || e.kind == 6));
        assert_eq!(t.last().unwrap().a, 0);
    }
    #[test]
    fn load_malformed_logs_once_and_nilstore_never_serializes() {
        let t = run(req(0), |e, r| {
            if e.kind == 13 {
                r.ok = false;
            }
        });
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![2, 11, 12, 13, 14, 63]
        );
        for op in [0, 1] {
            let t = run(req(op), |e, r| {
                if e.kind == 2 {
                    r.snapshot.as_mut().unwrap().store = false
                }
            });
            assert!(!t.iter().any(|e| matches!(e.kind, 12 | 13 | 15 | 16)));
        }
    }
    #[test]
    fn raw_errors_preserve_token_and_borrowed_delete_does_not_trim() {
        let t = run(req(4), |e, r| {
            if e.kind == 4 {
                r.ok = false;
                r.token = 42;
                r.error = vec![255, 0];
            }
        });
        assert_eq!(t.last().unwrap().indices, vec![42]);
        assert_eq!(t.last().unwrap().text, vec![255, 0]);
        let mut q = req(6);
        q.name = b" old ".to_vec();
        let t = run(q, |_, _| {});
        assert_eq!(t.last().unwrap().b, 8);
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = Flow::new(req(0)).unwrap();
        let e = f.next();
        assert!(f
            .advance(Reply {
                kind: 2,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(e, f.next());
        assert!(f
            .advance(Reply {
                kind: 1,
                snapshot: Some(snap()),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(e, f.next());
    }
    #[test]
    fn large_load_linear_without_repeated_snapshots() {
        let mut f = Flow::new(req(0)).unwrap();
        let mut appended = 0;
        for _ in 0..110000 {
            let e = f.next();
            if e.kind == 0 {
                break;
            }
            let mut r = reply(&e);
            if e.kind == 13 {
                r.nodes = (0..50000).map(|i| node(i, b"duplicate")).collect();
            }
            if e.kind == 7 {
                appended += 1;
            }
            f.advance(r).unwrap();
        }
        assert_eq!(appended, 50000);
        assert_eq!(f.next().kind, 0);
    }
}
