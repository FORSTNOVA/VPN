//! Profile cache/install continuation. Blob arenas live in the caller; no filesystem/network IO here.
use std::collections::VecDeque;
#[derive(Clone, Debug, Default)]
pub struct Entry {
    pub id: Vec<u8>,
    pub url: Vec<u8>,
    pub label: Vec<u8>,
    pub prefix: Vec<u8>,
    pub hash: Vec<u8>,
    pub enabled: bool,
    pub url_nonempty: bool,
    pub updated: i64,
}
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub entries: Vec<Entry>,
    pub active_url: Vec<u8>,
    pub active_hash: Vec<u8>,
    pub selected: Vec<u8>,
    pub selected_node: Vec<u8>,
    pub manual_names: Vec<Vec<u8>>,
    pub cached_count: i64,
    pub auto_hours: i64,
    pub mixed_proxy: Vec<u8>,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub id: Vec<u8>,
    pub body: i64,
    pub body_nil: bool,
    pub format: Vec<u8>,
    pub sources: Vec<i64>,
    pub url: Vec<u8>,
    pub entry: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub c: i64,
    pub data: Vec<u8>,
    pub text: Vec<u8>,
    pub items: Vec<i64>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub ok: bool,
    pub flag: bool,
    pub nil: bool,
    pub token: i64,
    pub count: i64,
    pub err_kind: u8,
    pub error: Vec<u8>,
    pub names: Vec<Vec<u8>>,
    pub tokens: Vec<i64>,
    pub info: i64,
}
#[derive(Clone, Debug)]
pub struct Flow {
    acquisition: Option<Box<crate::acquisition_flow::Flow>>,
    acquisition_merge: Option<Box<crate::acquisition_merge::Flow>>,
    snapshot: Snapshot,
    request: Request,
    queue: VecDeque<(Effect, u8)>,
    body: i64,
    nodes: i64,
    count: i64,
    sources: Vec<i64>,
    cursor: usize,
    entry: usize,
    previous: i64,
    previous_nil: bool,
    running: bool,
    config: i64,
    original_error: Vec<u8>,
    rename_error: Vec<u8>,
    names: Vec<Vec<u8>>,
}
fn effect(kind: u8, a: i64, b: i64) -> Effect {
    Effect {
        kind,
        a,
        b,
        ..Effect::default()
    }
}
impl Flow {
    pub fn new(snapshot: Snapshot, request: Request) -> Result<Self, &'static str> {
        if request.op >= 7 && request.op <= 15 {
            let mut f = Self::new(
                snapshot.clone(),
                Request {
                    op: 4,
                    ..Request::default()
                },
            )?;
            f.queue.clear();
            if matches!(request.op, 11 | 13 | 14 | 15) {
                f.acquisition_merge = Some(Box::new(crate::acquisition_merge::Flow::new(
                    snapshot, request,
                )?));
            } else {
                f.acquisition = Some(Box::new(crate::acquisition_flow::Flow::new(
                    snapshot, request,
                )?));
            }
            return Ok(f);
        }
        if request.op > 6
            || snapshot.entries.len() > 100_000
            || request.sources.len() > 100_000
            || snapshot.manual_names.len() > 100_000
        {
            return Err("invalid profile request");
        }
        let mut f = Self {
            acquisition: None,
            acquisition_merge: None,
            body: request.body,
            sources: if request.op == 5 {
                request.sources.clone()
            } else {
                Vec::new()
            },
            snapshot,
            request,
            queue: VecDeque::new(),
            nodes: 0,
            count: 0,
            cursor: 0,
            entry: 0,
            previous: 0,
            previous_nil: true,
            running: false,
            config: 0,
            original_error: vec![],
            rename_error: vec![],
            names: vec![],
        };
        match f.request.op {
            0 | 1 => {
                if f.request.op == 1 {
                    f.request.id = f.snapshot.active_hash.clone();
                }
                if f.request.body_nil
                    || f.request.id.is_empty()
                    || (f.request.op == 1 && f.snapshot.active_url.is_empty())
                {
                    f.result(0);
                } else {
                    f.file(1, 1, 0, f.request.id.clone(), vec![], 1);
                }
            }
            2 => f.file(2, 1, 0, f.request.id.clone(), vec![], 10),
            3 | 6 => f.next_source(),
            4 => f.replace(false),
            5 => f.merge(),
            _ => unreachable!(),
        }
        Ok(f)
    }
    fn emit(&mut self, e: Effect, next: u8) {
        self.queue.push_back((e, next));
    }
    fn file(&mut self, k: u8, path: i64, token: i64, id: Vec<u8>, text: Vec<u8>, next: u8) {
        let mut e = effect(k, path, token);
        e.data = id;
        e.text = text;
        self.emit(e, next);
    }
    fn result(&mut self, code: i64) {
        let mut e = effect(63, code, self.nodes);
        e.c = self.count;
        e.items = self.sources.clone();
        if code == 10 {
            e.text = self.original_error.clone();
        }
        if code == 12 {
            e.text = self.original_error.clone();
            e.data = self.rename_error.clone();
        }
        self.emit(e, 0);
    }
    fn fail(&mut self, code: i64, error: Vec<u8>) {
        self.original_error = error;
        self.result(code);
    }
    fn parse(&mut self, next: u8) {
        self.emit(effect(6, 0, self.body), next);
    }
    fn merge(&mut self) {
        let mut e = effect(7, 0, 0);
        e.items = self.sources.clone();
        self.emit(e, 30);
    }
    fn next_source(&mut self) {
        while self.cursor < self.snapshot.entries.len() {
            let i = self.cursor;
            self.cursor += 1;
            let e = &self.snapshot.entries[i];
            if e.enabled && e.url_nonempty {
                self.entry = i;
                self.file(2, 1, 0, e.id.clone(), vec![], 20);
                return;
            }
        }
        if self.request.op == 6 {
            if self.sources.is_empty() {
                self.result(9);
            } else {
                self.merge();
            }
        } else {
            self.result(0);
        }
    }
    fn replace(&mut self, restore: bool) {
        self.file(
            3,
            4,
            if restore { self.previous } else { self.body },
            vec![],
            vec![],
            if restore { 45 } else { 40 },
        );
    }
    fn merged_commit(&mut self) {
        self.file(3, 5, 0, vec![], b"__merged__".to_vec(), 0);
        self.file(3, 6, 0, vec![], b"merged".to_vec(), 0);
        let mut e = effect(32, 0, 0);
        e.data = b"__merged__".to_vec();
        self.emit(e, 0);
        self.emit(effect(11, 0, 0), 0);
        self.apply_nodes(false);
        self.emit(effect(12, 0, self.nodes), 0);
        self.result(0);
    }
    fn apply_nodes(&mut self, label: bool) {
        let mut e = effect(33, 0, self.nodes);
        e.data = self.snapshot.selected_node.clone();
        if label
            && !self.snapshot.active_url.is_empty()
            && self.snapshot.active_url != b"__merged__"
        {
            e.c = 1;
            e.text = self
                .snapshot
                .entries
                .iter()
                .find(|v| v.url == self.snapshot.active_url && !v.label.is_empty())
                .map(|v| v.label.clone())
                .unwrap_or_else(|| "当前订阅".as_bytes().to_vec());
        }
        self.emit(e, 0);
    }
    pub fn next(&self) -> Effect {
        if let Some(f) = &self.acquisition {
            return f.next();
        }
        if let Some(f) = &self.acquisition_merge {
            return f.next();
        }
        self.queue
            .front()
            .map(|(e, _)| e.clone())
            .unwrap_or_default()
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if let Some(f) = &mut self.acquisition {
            return f.advance(r);
        }
        if let Some(f) = &mut self.acquisition_merge {
            return f.advance(r);
        }
        let Some((_, next)) = self.queue.front().cloned() else {
            return Err("complete");
        };
        if r.names.len() > 100_000 || r.tokens.len() > 100_000 {
            return Err("reply limit");
        }
        self.queue.pop_front();
        match next {
            0 => {}
            1 => {
                if r.ok {
                    self.file(3, 1, self.body, self.request.id.clone(), vec![], 2);
                } else {
                    self.result(0);
                }
            }
            2 => {
                if r.ok {
                    self.file(
                        3,
                        2,
                        0,
                        self.request.id.clone(),
                        self.request.format.clone(),
                        0,
                    );
                }
                self.result(0);
            }
            10 => {
                if r.ok {
                    self.body = r.token;
                    self.parse(11);
                } else {
                    self.fail(1, r.error);
                }
            }
            11 => {
                if r.count == 0 {
                    self.result(2);
                } else if let Some(i) = self
                    .snapshot
                    .entries
                    .iter()
                    .position(|e| e.id == self.request.id)
                {
                    self.entry = i;
                    self.file(1, 2, 0, vec![], vec![], 12);
                } else {
                    self.result(3);
                }
            }
            12 => {
                if r.ok {
                    self.file(3, 4, self.body, vec![], vec![], 13);
                } else {
                    self.fail(4, r.error);
                }
            }
            13 => {
                if r.ok {
                    self.emit(effect(4, 1, 0), 14);
                } else {
                    self.fail(5, r.error);
                }
            }
            14 => {
                if r.ok {
                    let en = &self.snapshot.entries[self.entry];
                    let source = if en.url == b"__merged__" {
                        b"__merged__".to_vec()
                    } else {
                        en.hash.clone()
                    };
                    self.file(3, 5, 0, vec![], source, 15);
                } else {
                    self.file(5, 1, 0, vec![], vec![], 0);
                    self.fail(6, r.error);
                }
            }
            15 => {
                if r.ok {
                    self.file(2, 2, 0, self.request.id.clone(), vec![], 16);
                } else {
                    self.fail(7, r.error);
                }
            }
            16 => {
                if r.ok {
                    self.file(3, 6, r.token, vec![], vec![], 17);
                } else {
                    self.parse(18);
                }
            }
            17 => self.parse(18),
            18 => {
                self.nodes = r.token;
                self.count = r.count;
                self.apply_nodes(true);
                self.emit(effect(12, 0, self.nodes), 19);
            }
            19 => {
                if r.ok {
                    self.result(0);
                } else {
                    self.fail(8, r.error);
                }
            }
            20 => {
                if r.ok {
                    self.body = r.token;
                    self.parse(21);
                } else {
                    self.next_source();
                }
            }
            21 => {
                if r.count > 0 {
                    self.emit(effect(13, self.entry as i64, self.body), 22);
                } else {
                    self.next_source();
                }
            }
            22 => {
                self.sources.push(r.token);
                self.next_source();
            }
            30 => {
                if r.ok {
                    self.body = r.token;
                    self.nodes = r.info;
                    self.count = r.count;
                    self.names = r.names;
                    self.file(1, 2, 0, vec![], vec![], 31);
                } else {
                    self.fail(10, r.error);
                }
            }
            31 => {
                if r.ok {
                    self.emit(effect(8, 0, 0), 32);
                } else {
                    self.fail(4, r.error);
                }
            }
            32 => {
                self.running = r.flag;
                if self.running {
                    let selected = &self.snapshot.selected;
                    let special = [
                        b"".as_slice(),
                        b"SmartVPNAuto",
                        b"SmartVPNFallback",
                        b"SmartVPNChain",
                        b"REJECT",
                    ];
                    if !special.contains(&selected.as_slice())
                        && !self.names.contains(selected)
                        && !self.snapshot.manual_names.contains(selected)
                    {
                        self.result(11);
                    } else {
                        self.file(2, 3, 0, vec![], vec![], 33);
                    }
                } else {
                    self.replace(false);
                }
            }
            33 => {
                if r.ok || r.err_kind == 1 {
                    self.previous = r.token;
                    self.previous_nil = r.nil;
                    self.emit(effect(9, 0, self.body), 34);
                } else {
                    self.fail(10, r.error);
                }
            }
            34 => {
                if r.ok {
                    self.config = r.token;
                    self.replace(false);
                } else {
                    self.fail(10, r.error);
                }
            }
            40 => {
                if r.ok {
                    self.emit(effect(4, 1, 0), 41);
                } else {
                    self.fail(10, r.error);
                }
            }
            41 => {
                if !r.ok {
                    self.file(5, 1, 0, vec![], vec![], 0);
                    self.fail(10, r.error);
                } else if self.request.op == 4 {
                    self.result(0);
                } else if self.running {
                    self.emit(effect(10, 0, self.config), 42);
                } else {
                    self.merged_commit();
                }
            }
            42 => {
                if r.ok {
                    self.merged_commit();
                } else {
                    self.original_error = r.error;
                    if self.previous_nil {
                        self.file(5, 2, 0, vec![], vec![], 44);
                    } else {
                        self.replace(true);
                    }
                }
            }
            44 => {
                if !r.ok {
                    self.rename_error = r.error;
                }
                self.result(12);
            }
            45 => {
                if r.ok {
                    self.emit(effect(4, 1, 0), 46);
                } else {
                    self.rename_error = r.error;
                    self.result(12);
                }
            }
            46 => {
                if !r.ok {
                    self.rename_error = r.error;
                    self.file(5, 1, 0, vec![], vec![], 0);
                }
                self.result(12);
            }
            _ => return Err("invalid continuation"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn snapshot() -> Snapshot {
        Snapshot {
            entries: vec![Entry {
                id: b"a".to_vec(),
                url: b"url".to_vec(),
                label: b"label".to_vec(),
                hash: b"hash".to_vec(),
                enabled: true,
                url_nonempty: true,
                ..Entry::default()
            }],
            active_url: b"url".to_vec(),
            active_hash: b"hash".to_vec(),
            selected: b"node".to_vec(),
            selected_node: b"stored".to_vec(),
            ..Snapshot::default()
        }
    }
    fn request(op: u8) -> Request {
        Request {
            op,
            id: b"a".to_vec(),
            body: 1,
            format: b"yaml".to_vec(),
            sources: vec![8],
            ..Request::default()
        }
    }
    fn ok(e: &Effect) -> Reply {
        Reply {
            ok: true,
            token: if e.kind == 6 { 2 } else { 1 },
            info: 2,
            count: 1,
            names: vec![b"node".to_vec()],
            ..Reply::default()
        }
    }
    fn run(s: Snapshot, r: Request, mut answer: impl FnMut(&Effect) -> Reply) -> Vec<Effect> {
        let mut flow = Flow::new(s, r).unwrap();
        let mut trace = vec![];
        for _ in 0..100 {
            let e = flow.next();
            if e.kind == 0 {
                return trace;
            }
            let r = answer(&e);
            trace.push(e);
            flow.advance(r).unwrap();
        }
        panic!("loop")
    }
    fn ks(t: &[Effect]) -> Vec<u8> {
        t.iter().map(|e| e.kind).collect()
    }
    #[test]
    fn cache_optional_failure_is_best_effort_and_nil_differs_empty() {
        for fail in [0, 1, 3] {
            let t = run(snapshot(), request(0), |e| Reply {
                ok: e.kind != fail,
                ..ok(e)
            });
            assert_eq!(t.last().unwrap().a, 0);
            assert!(!ks(&t).contains(&4));
        }
        let mut r = request(0);
        r.body_nil = true;
        assert_eq!(ks(&run(snapshot(), r, ok)), vec![63]);
        let mut s = snapshot();
        s.active_url.clear();
        assert_eq!(ks(&run(s, request(1), ok)), vec![63]);
        let t = run(snapshot(), request(0), ok);
        assert_eq!(ks(&t), vec![1, 3, 3, 63]);
        assert_eq!(t[2].text, b"yaml");
    }
    #[test]
    fn use_cache_success_reads_parses_installs_and_reparses() {
        let t = run(snapshot(), request(2), ok);
        assert_eq!(ks(&t), vec![2, 6, 1, 3, 4, 3, 2, 3, 6, 33, 12, 63]);
        assert_eq!(t[5].a, 5);
        assert_eq!(t[5].text, b"hash");
        let commit = &t[9];
        assert_eq!(commit.data, b"stored");
        assert_eq!(commit.text, b"label");
        assert_eq!(commit.c, 1);
    }
    #[test]
    fn use_cache_error_boundaries_and_ignored_format() {
        for (kind, a, code) in [
            (2, 1, 1),
            (1, 2, 4),
            (3, 4, 5),
            (4, 1, 6),
            (3, 5, 7),
            (12, 0, 8),
        ] {
            let t = run(snapshot(), request(2), |e| Reply {
                ok: !(e.kind == kind && e.a == a),
                ..ok(e)
            });
            assert_eq!(t.last().unwrap().a, code);
            if code == 6 {
                assert_eq!(t[t.len() - 2].kind, 5);
            }
        }
        let t = run(snapshot(), request(2), |e| Reply {
            ok: !(e.kind == 2 && e.a == 2),
            ..ok(e)
        });
        assert_eq!(t.last().unwrap().a, 0);
        assert_eq!(ks(&t).iter().filter(|&&k| k == 6).count(), 2);
    }
    #[test]
    fn cache_parse_failure_precedes_missing_registry_entry() {
        let mut r = request(2);
        r.id = b"missing".to_vec();
        let t = run(snapshot(), r.clone(), |e| Reply { count: 0, ..ok(e) });
        assert_eq!(t.last().unwrap().a, 2);
        let t = run(snapshot(), r, ok);
        assert_eq!(t.last().unwrap().a, 3);
        assert!(!ks(&t).contains(&1));
    }
    #[test]
    fn cached_sources_filter_order_and_empty_merge() {
        let mut s = snapshot();
        s.entries.push(Entry {
            enabled: false,
            ..Entry::default()
        });
        s.entries.push(Entry {
            enabled: true,
            url_nonempty: false,
            ..Entry::default()
        });
        let mut r = request(3);
        r.sources.clear();
        let t = run(s, r, ok);
        assert_eq!(ks(&t), vec![2, 6, 13, 63]);
        assert_eq!(t[2].a, 0);
        assert_eq!(t.last().unwrap().items, vec![1]);
        let mut r = request(6);
        r.sources.clear();
        let t = run(Snapshot::default(), r, ok);
        assert_eq!(t[0].a, 9);
    }
    #[test]
    fn standalone_replace_failure_removes_temp_once() {
        let t = run(snapshot(), request(4), |e| Reply {
            ok: e.kind != 4,
            error: b"rename".to_vec(),
            ..ok(e)
        });
        assert_eq!(ks(&t), vec![3, 4, 5, 63]);
        assert_eq!(t.last().unwrap().a, 10);
        assert_eq!(t.last().unwrap().text, b"rename");
    }
    #[test]
    fn merged_disconnected_commits_after_replace_and_ignores_late_saves() {
        let t = run(snapshot(), request(5), |e| Reply {
            ok: !matches!(e.kind, 11 | 12),
            ..ok(e)
        });
        assert_eq!(ks(&t), vec![7, 1, 8, 3, 4, 3, 3, 32, 11, 33, 12, 63]);
        assert_eq!(t.last().unwrap().a, 0);
        assert_eq!(t[9].c, 0);
    }
    #[test]
    fn running_selection_protection_and_manual_exception() {
        let mut s = snapshot();
        s.selected = b"missing".to_vec();
        let t = run(s.clone(), request(5), |e| Reply {
            flag: e.kind == 8,
            ..ok(e)
        });
        assert_eq!(ks(&t), vec![7, 1, 8, 63]);
        assert_eq!(t.last().unwrap().a, 11);
        s.manual_names.push(b"missing".to_vec());
        let t = run(s, request(5), |e| Reply {
            flag: e.kind == 8,
            ..ok(e)
        });
        assert!(ks(&t).contains(&10));
        assert_eq!(t.last().unwrap().a, 0);
        for name in [
            b"SmartVPNAuto".to_vec(),
            b"SmartVPNFallback".to_vec(),
            b"SmartVPNChain".to_vec(),
            b"REJECT".to_vec(),
        ] {
            let mut s = snapshot();
            s.selected = name;
            assert_eq!(
                run(s, request(5), |e| Reply {
                    flag: e.kind == 8,
                    ..ok(e)
                })
                .last()
                .unwrap()
                .a,
                0
            );
        }
    }
    #[test]
    fn apply_failure_restores_empty_existing_file_but_deletes_absent() {
        for absent in [false, true] {
            let t = run(snapshot(), request(5), |e| Reply {
                ok: e.kind != 10,
                flag: e.kind == 8,
                nil: absent,
                token: if e.kind == 2 { 77 } else { 1 },
                error: if e.kind == 10 {
                    b"apply".to_vec()
                } else {
                    vec![]
                },
                ..ok(e)
            });
            let pos = t.iter().position(|e| e.kind == 10).unwrap();
            if absent {
                assert_eq!(t[pos + 1].kind, 5);
                assert_eq!(t[pos + 1].a, 2);
            } else {
                assert_eq!(t[pos + 1].kind, 3);
                assert_eq!(t[pos + 1].b, 77);
                assert_eq!(t[pos + 2].kind, 4);
            }
            assert_eq!(t.last().unwrap().a, 12);
            assert_eq!(t.last().unwrap().text, b"apply");
            assert!(!ks(&t).contains(&32));
        }
    }
    #[test]
    fn apply_and_restore_failure_retains_both_errors() {
        let mut apply = false;
        let t = run(snapshot(), request(5), |e| {
            if e.kind == 10 {
                apply = true;
            }
            Reply {
                ok: !(e.kind == 10 || (apply && e.kind == 4)),
                flag: e.kind == 8,
                error: if e.kind == 10 {
                    b"apply".to_vec()
                } else if apply {
                    b"restore".to_vec()
                } else {
                    vec![]
                },
                ..ok(e)
            }
        });
        assert_eq!(t.last().unwrap().a, 12);
        assert_eq!(t.last().unwrap().text, b"apply");
        assert_eq!(t.last().unwrap().data, b"restore");
        assert_eq!(t[t.len() - 2].kind, 5);
    }
}
