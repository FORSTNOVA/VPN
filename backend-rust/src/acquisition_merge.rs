//! Subscription acquisition/merge continuations. All I/O is explicit and caller-owned.
use crate::profile_flow::{Effect, Reply, Request, Snapshot};

#[derive(Clone, Debug)]
pub struct Flow {
    snapshot: Snapshot,
    request: Request,
    pending: Effect,
    stage: u8,
    child: Option<Box<crate::profile_flow::Flow>>,
    child_stage: u8,
    enabled: Vec<usize>,
    handles: Vec<i64>,
    cursor: usize,
    sources: Vec<i64>,
    body: i64,
    nodes: i64,
    count: i64,
    quota: i64,
    warning: Vec<u8>,
    urls: Vec<Vec<u8>>,
    parts: Vec<i64>,
    inline: i64,
    inline_count: i64,
    single: bool,
    result: Effect,
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
        if !matches!(request.op, 11 | 13 | 14 | 15) || snapshot.entries.len() > 100_000 {
            return Err("invalid merge request");
        }
        if request.op == 15
            && (request.entry < 0 || request.entry as usize >= snapshot.entries.len())
        {
            return Err("invalid acquisition entry");
        }
        let enabled = snapshot
            .entries
            .iter()
            .enumerate()
            .filter(|(_, e)| e.enabled && e.url_nonempty)
            .map(|(i, _)| i)
            .collect();
        let mut f = Self {
            snapshot,
            request,
            pending: Effect::default(),
            stage: 0,
            child: None,
            child_stage: 0,
            enabled,
            handles: vec![],
            cursor: 0,
            sources: vec![],
            body: 0,
            nodes: 0,
            count: 0,
            quota: 0,
            warning: vec![],
            urls: vec![],
            parts: vec![],
            inline: 0,
            inline_count: 0,
            single: false,
            result: Effect::default(),
        };
        match f.request.op {
            15 => {
                let mut e = effect(15, 0, 0);
                e.data = f.snapshot.entries[f.request.entry as usize].url.clone();
                f.emit(e, 20);
            }
            14 => {
                if f.snapshot.auto_hours <= 0 || f.enabled.is_empty() {
                    f.done(Effect {
                        kind: 63,
                        ..Effect::default()
                    });
                } else if f.latest() > 0 {
                    f.emit(effect(22, 0, 0), 10);
                } else {
                    f.auto_start();
                }
            }
            _ => f.batch(),
        }
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.child
            .as_ref()
            .map(|c| c.next())
            .unwrap_or_else(|| self.pending.clone())
    }
    fn emit(&mut self, e: Effect, stage: u8) {
        self.pending = e;
        self.stage = stage;
    }
    fn done(&mut self, e: Effect) {
        self.emit(e, 255);
    }
    fn raw(&mut self, text: &str) {
        self.finish(Effect {
            kind: 63,
            a: 10,
            text: text.as_bytes().to_vec(),
            ..Effect::default()
        });
    }
    fn latest(&self) -> i64 {
        self.enabled
            .iter()
            .map(|i| self.snapshot.entries[*i].updated)
            .fold(0, i64::max)
    }
    fn auto_start(&mut self) {
        let mut e = effect(26, 0, 0);
        e.text = format!(
            "auto-merge: triggering scheduled subscription merge (interval: {}h)",
            self.snapshot.auto_hours
        )
        .into_bytes();
        self.emit(e, 11);
    }
    fn batch(&mut self) {
        if self.enabled.is_empty() {
            self.raw("没有已启用的订阅源，请至少勾选启用一个订阅");
            return;
        }
        let mut e = effect(24, 0, 0);
        e.items = self.enabled.iter().map(|i| *i as i64).collect();
        self.emit(e, 1);
    }
    fn inspect(&mut self) -> Result<(), &'static str> {
        if self.cursor < self.handles.len() {
            self.emit(effect(25, 0, self.handles[self.cursor]), 2);
        } else if self.sources.is_empty() {
            self.raw("所有已启用的订阅均拉取失败且无可用本地缓存");
        } else {
            self.start_child(
                Request {
                    op: 5,
                    sources: self.sources.clone(),
                    ..Request::default()
                },
                2,
            )?;
        }
        Ok(())
    }
    fn registry(&mut self, usable: bool) {
        let index = self.enabled[self.cursor];
        let mut e = effect(21, if usable { 1 } else { 2 }, index as i64);
        e.c = self.count;
        e.data = self.snapshot.entries[index].id.clone();
        e.text = self.warning.clone();
        if !usable && e.text.is_empty() {
            e.text = "未能获取到可用节点".as_bytes().to_vec();
        }
        e.items = vec![self.quota];
        self.emit(e, 4);
    }
    fn start_child(&mut self, r: Request, stage: u8) -> Result<(), &'static str> {
        self.child = Some(Box::new(crate::profile_flow::Flow::new(
            self.snapshot.clone(),
            r,
        )?));
        self.child_stage = stage;
        self.pump()
    }
    fn pump(&mut self) -> Result<(), &'static str> {
        loop {
            let Some(child) = &self.child else {
                return Ok(());
            };
            let e = child.next();
            if e.kind != 63 {
                return Ok(());
            }
            self.child = None;
            match self.child_stage {
                1 => self.registry(true),
                2 => self.finish(e),
                3 => self.download_done(e)?,
                _ => return Err("invalid child stage"),
            }
        }
    }
    fn finish(&mut self, e: Effect) {
        if self.request.op == 14 {
            let mut log = effect(26, e.a, 0);
            if e.a == 0 {
                log.text = format!("auto-merge completed: merged {} nodes", e.c).into_bytes();
            } else {
                log.c = 1;
                log.text = e.text;
                log.data = e.data;
            }
            self.emit(log, 12);
        } else if self.request.op == 13 && e.a == 0 {
            self.result = e;
            self.emit(effect(34, 0, 0), 13);
        } else {
            self.done(e);
        }
    }
    fn download(&mut self) -> Result<(), &'static str> {
        let url = self.urls[self.cursor].clone();
        self.start_child(
            Request {
                op: 8,
                url,
                ..Request::default()
            },
            3,
        )
    }
    fn download_done(&mut self, e: Effect) -> Result<(), &'static str> {
        let body = e.items.first().copied().unwrap_or(0);
        let quota = e.items.get(1).copied().unwrap_or(0);
        // download results use data[0] to distinguish a nonempty body from a nonzero arena handle.
        let nonempty = e.data.first() == Some(&1);
        if self.single {
            if e.a == 0 {
                self.done(e);
            } else {
                self.result = e.clone();
                self.warning = e.text;
                self.quota = quota;
                self.cache();
            }
        } else {
            if e.a == 0 && nonempty {
                self.parts.push(body);
                if self.quota == 0 {
                    self.quota = quota;
                }
            }
            self.cursor += 1;
            if self.cursor < self.urls.len() {
                self.download()?;
            } else {
                self.merge_parts();
            }
        }
        Ok(())
    }
    fn merge_parts(&mut self) {
        if self.inline_count > 0 {
            self.parts.push(self.inline);
        }
        if self.parts.is_empty() {
            self.warning = "未能获取到节点".as_bytes().to_vec();
            self.cache();
        } else {
            let mut e = effect(19, 0, 0);
            e.items = self.parts.clone();
            self.emit(e, 22);
        }
    }
    fn cache(&mut self) {
        let mut e = effect(2, 1, 0);
        e.data = self.snapshot.entries[self.request.entry as usize]
            .id
            .clone();
        self.emit(e, 23);
    }
    fn cache_failed(&mut self) {
        if self.single && self.result.a != 0 {
            let mut out = self.result.clone();
            out.b = 0;
            out.c = 0;
            out.items = vec![0, 0];
            if out.data.is_empty() {
                out.data.push(0);
            } else {
                out.data[0] = 0;
            }
            self.done(out);
        } else {
            self.acquired(false, self.warning.clone(), false);
        }
    }
    fn acquired(&mut self, ok: bool, error: Vec<u8>, nonempty: bool) {
        let mut e = effect(63, if ok { 0 } else { 10 }, self.nodes);
        e.c = self.count;
        e.items = vec![self.body, self.quota];
        e.text = error;
        e.data = vec![u8::from(nonempty)];
        self.done(e);
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if r.names.len() > 100_000 || r.tokens.len() > 100_000 {
            return Err("reply limit");
        }
        if let Some(c) = &mut self.child {
            c.advance(r)?;
            return self.pump();
        }
        match self.stage {
            1 => {
                if r.tokens.len() != self.enabled.len() {
                    return Err("acquisition batch cardinality");
                }
                self.handles = r.tokens;
                self.cursor = 0;
                self.inspect()?;
            }
            2 => {
                self.body = r.token;
                self.nodes = r.info;
                self.count = r.count;
                self.quota = r.tokens.first().copied().unwrap_or(0);
                self.warning = if r.ok { vec![] } else { r.error };
                if r.flag && r.count > 0 {
                    self.emit(effect(13, self.enabled[self.cursor] as i64, self.body), 3);
                } else {
                    self.registry(false);
                }
            }
            3 => {
                self.sources.push(r.token);
                let id = self.snapshot.entries[self.enabled[self.cursor]].id.clone();
                self.start_child(
                    Request {
                        op: 0,
                        id,
                        body: self.body,
                        format: b"yaml".to_vec(),
                        ..Request::default()
                    },
                    1,
                )?;
            }
            4 => {
                self.cursor += 1;
                self.inspect()?;
            }
            10 => {
                if r.count.wrapping_sub(self.latest()) < self.snapshot.auto_hours.wrapping_mul(3600)
                {
                    self.done(effect(63, 0, 0));
                } else {
                    self.auto_start();
                }
            }
            11 => self.batch(),
            12 => self.done(effect(63, 0, 0)),
            13 => self.done(self.result.clone()),
            20 => {
                self.urls = r.names;
                self.inline = r.token;
                self.inline_count = r.count;
                self.cursor = 0;
                self.single = self.urls.len() == 1 && self.inline_count == 0;
                if self.urls.is_empty() && self.inline_count > 0 {
                    self.emit(effect(18, 0, self.inline), 21);
                } else if self.urls.is_empty() {
                    self.merge_parts();
                } else {
                    self.download()?;
                }
            }
            21 | 22 => {
                self.body = r.token;
                self.nodes = r.info;
                self.count = r.count;
                self.acquired(r.ok, r.error, r.flag);
                self.pending.data.push(r.err_kind);
            }
            23 => {
                if r.ok && r.flag {
                    self.body = r.token;
                    self.emit(effect(6, 0, self.body), 24);
                } else {
                    self.body = 0;
                    self.nodes = 0;
                    self.count = 0;
                    self.quota = 0;
                    self.cache_failed();
                }
            }
            24 => {
                if r.count > 0 {
                    self.nodes = r.token;
                    self.count = r.count;
                    self.acquired(
                        false,
                        "网络获取失败，使用历史缓存".as_bytes().to_vec(),
                        true,
                    );
                } else {
                    self.body = 0;
                    self.nodes = 0;
                    self.count = 0;
                    self.quota = 0;
                    self.cache_failed();
                }
            }
            _ => return Err("complete"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::profile_flow::Entry;
    fn snapshot() -> Snapshot {
        Snapshot {
            entries: vec![
                Entry {
                    id: b"a".to_vec(),
                    url: b"u".to_vec(),
                    enabled: true,
                    url_nonempty: true,
                    ..Entry::default()
                },
                Entry {
                    id: b"disabled".to_vec(),
                    enabled: false,
                    url_nonempty: true,
                    ..Entry::default()
                },
                Entry {
                    id: b"b".to_vec(),
                    url: b"v".to_vec(),
                    enabled: true,
                    url_nonempty: true,
                    ..Entry::default()
                },
            ],
            ..Snapshot::default()
        }
    }
    fn yes() -> Reply {
        Reply {
            ok: true,
            ..Reply::default()
        }
    }
    #[test]
    fn empty_enabled_fails_without_effects() {
        let f = Flow::new(
            Snapshot::default(),
            Request {
                op: 11,
                ..Request::default()
            },
        )
        .unwrap();
        assert_eq!(f.next().kind, 63);
        assert_eq!(f.next().a, 10);
    }
    #[test]
    fn ordered_batch_and_errors() {
        let mut f = Flow::new(
            snapshot(),
            Request {
                op: 11,
                ..Request::default()
            },
        )
        .unwrap();
        assert_eq!(f.next().kind, 24);
        assert_eq!(f.next().items, vec![0, 2]);
        f.advance(Reply {
            tokens: vec![71, 72],
            ..yes()
        })
        .unwrap();
        assert_eq!(f.next().b, 71);
        f.advance(Reply {
            error: b"first failed".to_vec(),
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, 21);
        assert_eq!(f.next().data, b"a");
        assert_eq!(f.next().text, b"first failed");
        f.advance(yes()).unwrap();
        assert_eq!(f.next().b, 72);
        f.advance(Reply::default()).unwrap();
        assert_eq!(f.next().data, b"b");
        assert_eq!(f.next().text, "未能获取到可用节点".as_bytes());
        f.advance(yes()).unwrap();
        assert_eq!(f.next().kind, 63);
        assert_eq!(f.next().a, 10);
    }
    #[test]
    fn malformed_batch_fails_closed() {
        let mut f = Flow::new(
            snapshot(),
            Request {
                op: 11,
                ..Request::default()
            },
        )
        .unwrap();
        assert!(f
            .advance(Reply {
                tokens: vec![1],
                ..yes()
            })
            .is_err());
        assert_eq!(f.next().kind, 24);
    }
    #[test]
    fn cached_warning_success_is_recorded_in_order() {
        let mut f = Flow::new(
            snapshot(),
            Request {
                op: 11,
                ..Request::default()
            },
        )
        .unwrap();
        f.advance(Reply {
            tokens: vec![71, 72],
            ..yes()
        })
        .unwrap();
        f.advance(Reply {
            token: 50,
            info: 51,
            count: 2,
            flag: true,
            error: b"cache warning".to_vec(),
            tokens: vec![52],
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, 13);
        assert_eq!(f.next().a, 0);
        f.advance(Reply { token: 90, ..yes() }).unwrap();
        assert_eq!(f.next().kind, 1); // child cache mkdir
        f.advance(Reply::default()).unwrap(); // cache failures intentionally ignored by original
        assert_eq!(f.next().kind, 21);
        assert_eq!(f.next().a, 1);
        assert_eq!(f.next().c, 2);
        assert_eq!(f.next().text, b"cache warning");
        assert_eq!(f.next().items, vec![52]);
        f.advance(yes()).unwrap();
        assert_eq!(f.next().b, 72);
    }
    #[test]
    fn auto_tick_disabled_and_empty_are_noops() {
        for s in [
            snapshot(),
            Snapshot {
                auto_hours: 1,
                ..Snapshot::default()
            },
        ] {
            let f = Flow::new(
                s,
                Request {
                    op: 14,
                    ..Request::default()
                },
            )
            .unwrap();
            assert_eq!(f.next().kind, 63);
        }
    }
    #[test]
    fn auto_tick_uses_max_timestamp_and_exact_boundary() {
        for (now, run) in [(4599, false), (4600, true), (900, false)] {
            let mut s = snapshot();
            s.auto_hours = 1;
            s.entries[0].updated = 100;
            s.entries[2].updated = 1000;
            let mut f = Flow::new(
                s,
                Request {
                    op: 14,
                    ..Request::default()
                },
            )
            .unwrap();
            assert_eq!(f.next().kind, 22);
            f.advance(Reply {
                count: now,
                ..yes()
            })
            .unwrap();
            assert_eq!(f.next().kind, if run { 26 } else { 63 });
            if run {
                f.advance(yes()).unwrap();
                assert_eq!(f.next().kind, 24);
            }
        }
    }
    #[test]
    fn zero_latest_runs_without_clock_and_failed_tick_logs_then_succeeds() {
        let mut s = snapshot();
        s.auto_hours = 1;
        let mut f = Flow::new(
            s,
            Request {
                op: 14,
                ..Request::default()
            },
        )
        .unwrap();
        assert_eq!(f.next().kind, 26);
        f.advance(yes()).unwrap();
        f.advance(Reply {
            tokens: vec![1, 2],
            ..yes()
        })
        .unwrap();
        for _ in 0..2 {
            f.advance(Reply::default()).unwrap();
            f.advance(yes()).unwrap();
        }
        assert_eq!(f.next().kind, 26);
        assert_eq!(f.next().c, 1);
        assert_eq!(f.next().a, 10);
        f.advance(yes()).unwrap();
        assert_eq!(f.next().a, 0);
        assert_eq!(f.next().kind, 63);
    }
    #[test]
    fn inline_only_returns_partial_failure_fields() {
        let mut f = Flow::new(
            snapshot(),
            Request {
                op: 15,
                entry: 2,
                ..Request::default()
            },
        )
        .unwrap();
        assert_eq!(f.next().data, b"v");
        f.advance(Reply {
            token: 11,
            count: 2,
            ..yes()
        })
        .unwrap();
        assert_eq!(f.next().kind, 18);
        f.advance(Reply {
            token: 12,
            info: 13,
            count: 1,
            flag: true,
            error: b"parse warning".to_vec(),
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().a, 10);
        assert_eq!(f.next().items, vec![12, 0]);
        assert_eq!(f.next().b, 13);
        assert_eq!(f.next().data, vec![1, 0]);
    }
    #[test]
    fn empty_input_uses_cache_without_network() {
        let mut f = Flow::new(
            snapshot(),
            Request {
                op: 15,
                entry: 0,
                ..Request::default()
            },
        )
        .unwrap();
        f.advance(yes()).unwrap();
        assert_eq!(f.next().kind, 2);
        f.advance(Reply {
            token: 20,
            flag: true,
            ..yes()
        })
        .unwrap();
        assert_eq!(f.next().kind, 6);
        f.advance(Reply {
            token: 21,
            count: 3,
            ..yes()
        })
        .unwrap();
        assert_eq!(f.next().kind, 63);
        assert_eq!(f.next().items, vec![20, 0]);
        assert_eq!(f.next().b, 21);
        assert_eq!(f.next().a, 10);
        assert_eq!(f.next().text, "网络获取失败，使用历史缓存".as_bytes());
    }
    #[test]
    fn empty_cache_returns_generic_acquisition_failure() {
        for reply in [
            Reply::default(),
            Reply {
                ok: true,
                token: 7,
                flag: false,
                ..Reply::default()
            },
        ] {
            let mut f = Flow::new(
                snapshot(),
                Request {
                    op: 15,
                    ..Request::default()
                },
            )
            .unwrap();
            f.advance(yes()).unwrap();
            f.advance(reply).unwrap();
            assert_eq!(f.next().kind, 63);
            assert_eq!(f.next().items, vec![0, 0]);
            assert_eq!(f.next().text, "未能获取到节点".as_bytes());
        }
    }
    #[test]
    fn invalid_worker_entry_rejected() {
        assert!(Flow::new(
            snapshot(),
            Request {
                op: 15,
                entry: -1,
                ..Request::default()
            }
        )
        .is_err());
        assert!(Flow::new(
            snapshot(),
            Request {
                op: 15,
                entry: 3,
                ..Request::default()
            }
        )
        .is_err());
    }
}
#[cfg(test)]
mod acquisition_decision_tests {
    use super::*;
    use crate::profile_flow::Entry;
    fn worker() -> Flow {
        Flow::new(
            Snapshot {
                entries: vec![Entry {
                    id: b"entry".to_vec(),
                    ..Entry::default()
                }],
                ..Snapshot::default()
            },
            Request {
                op: 15,
                ..Request::default()
            },
        )
        .unwrap()
    }
    fn downloaded(body: i64, quota: i64, nonempty: bool, ok: bool) -> Effect {
        Effect {
            kind: 63,
            a: if ok { 0 } else { 10 },
            b: 44,
            c: 2,
            items: vec![body, quota],
            data: vec![u8::from(nonempty)],
            text: if ok {
                vec![]
            } else {
                b"original failure".to_vec()
            },
        }
    }
    #[test]
    fn mixed_ignores_error_and_empty_and_keeps_first_quota() {
        let mut f = worker();
        f.urls = vec![b"u".to_vec()];
        f.inline = 8;
        f.inline_count = 1;
        f.download_done(downloaded(10, 11, false, true)).unwrap();
        assert_eq!(f.next().items, vec![8]);
        assert_eq!(f.quota, 0);
        let mut f = worker();
        f.urls = vec![b"u".to_vec()];
        f.parts = vec![1];
        f.quota = 2;
        f.inline = 8;
        f.inline_count = 1;
        f.download_done(downloaded(10, 11, true, true)).unwrap();
        assert_eq!(f.next().kind, 19);
        assert_eq!(f.next().items, vec![1, 10, 8]);
        assert_eq!(f.quota, 2);
        let mut f = worker();
        f.urls = vec![b"u".to_vec()];
        f.download_done(downloaded(10, 11, true, false)).unwrap();
        assert_eq!(f.next().kind, 2);
        assert_eq!(f.quota, 0);
    }
    #[test]
    fn single_error_keeps_original_only_unless_cache_usable() {
        for usable in [false, true] {
            let mut f = worker();
            f.single = true;
            f.download_done(downloaded(10, 11, true, false)).unwrap();
            assert_eq!(f.next().kind, 2);
            f.advance(Reply {
                ok: true,
                flag: true,
                token: 21,
                ..Reply::default()
            })
            .unwrap();
            f.advance(Reply {
                token: 22,
                count: if usable { 3 } else { 0 },
                ..Reply::default()
            })
            .unwrap();
            assert_eq!(
                f.next().items,
                if usable { vec![21, 11] } else { vec![0, 0] }
            );
            assert_eq!(
                f.next().text,
                if usable {
                    "网络获取失败，使用历史缓存".as_bytes()
                } else {
                    b"original failure"
                }
            );
        }
    }
    #[test]
    fn merge_parser_failure_preserves_partial_fields_and_quota() {
        let mut f = worker();
        f.quota = 19;
        f.inline_count = 1;
        f.inline = 3;
        f.merge_parts();
        f.advance(Reply {
            token: 4,
            info: 5,
            count: 2,
            flag: true,
            error: b"partial".to_vec(),
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().items, vec![4, 19]);
        assert_eq!(f.next().a, 10);
        assert_eq!(f.next().b, 5);
        assert_eq!(f.next().c, 2);
    }
    #[test]
    fn endpoint_sets_from_cache_only_on_success() {
        let mut f = Flow::new(
            Snapshot::default(),
            Request {
                op: 13,
                ..Request::default()
            },
        )
        .unwrap();
        assert_eq!(f.next().kind, 63);
        assert_eq!(f.next().a, 10);
        f.finish(Effect {
            kind: 63,
            b: 10,
            c: 11,
            ..Effect::default()
        });
        assert_eq!(f.next().kind, 34);
        assert_eq!(f.next().a, 0);
        f.advance(Reply::default()).unwrap();
        assert_eq!(f.next().b, 10);
        assert_eq!(f.next().c, 11);
    }
}
#[cfg(test)]
mod child_integration_tests {
    use super::*;
    use crate::profile_flow::Entry;
    fn worker() -> crate::profile_flow::Flow {
        crate::profile_flow::Flow::new(
            Snapshot {
                entries: vec![Entry {
                    id: b"x".to_vec(),
                    url: b"synthetic".to_vec(),
                    ..Entry::default()
                }],
                ..Snapshot::default()
            },
            Request {
                op: 15,
                ..Request::default()
            },
        )
        .unwrap()
    }
    #[test]
    fn mixed_child_downloads_are_sequential_and_merge_preserves_order() {
        let mut f = worker();
        let mut downloads = 0;
        for _ in 0..100 {
            let e = f.next();
            if e.kind == 63 {
                assert_eq!(downloads, 2);
                assert_eq!(e.items, vec![99, 12]);
                assert_eq!(e.b, 100);
                return;
            }
            let r = match e.kind {
                15 => Reply {
                    names: vec![b"url-a".to_vec(), b"url-b".to_vec()],
                    token: 9,
                    count: 1,
                    ..Reply::default()
                },
                8 => Reply {
                    ok: true,
                    flag: false,
                    ..Reply::default()
                },
                14 => Reply {
                    ok: true,
                    flag: false,
                    ..Reply::default()
                },
                16 => Reply {
                    names: vec![e.data],
                    ..Reply::default()
                },
                17 => {
                    downloads += 1;
                    assert_eq!(e.data, if downloads == 1 { b"url-a" } else { b"url-b" });
                    Reply {
                        ok: true,
                        flag: true,
                        token: 10 + downloads,
                        info: 20 + downloads,
                        count: 1,
                        tokens: vec![11 + downloads],
                        ..Reply::default()
                    }
                }
                19 => {
                    assert_eq!(e.items, vec![11, 12, 9]);
                    Reply {
                        ok: true,
                        flag: true,
                        token: 99,
                        info: 100,
                        count: 3,
                        ..Reply::default()
                    }
                }
                _ => panic!("unexpected effect {:?}", e),
            };
            f.advance(r).unwrap();
        }
        panic!("continuation did not terminate");
    }
    #[test]
    fn special_download_error_survives_cache_failure() {
        let mut f = Flow::new(
            Snapshot {
                entries: vec![Entry::default()],
                ..Snapshot::default()
            },
            Request {
                op: 15,
                ..Request::default()
            },
        )
        .unwrap();
        f.single = true;
        f.download_done(Effect {
            kind: 63,
            a: 21,
            text: b"original".to_vec(),
            data: vec![0, 1],
            ..Effect::default()
        })
        .unwrap();
        f.advance(Reply::default()).unwrap();
        assert_eq!(f.next().a, 21);
        assert_eq!(f.next().data, vec![0, 1]);
        assert_eq!(f.next().items, vec![0, 0]);
    }
}
