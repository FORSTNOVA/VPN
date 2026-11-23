//! Download and active/single-subscription acquisition. All IO is one narrow effect.
use crate::profile_flow::{Effect, Reply, Request, Snapshot};
use std::collections::VecDeque;
#[derive(Clone, Debug)]
pub struct Flow {
    snapshot: Snapshot,
    request: Request,
    q: VecDeque<(Effect, u8)>,
    child: Option<Box<crate::profile_flow::Flow>>,
    child_after: u8,
    proxies: Vec<Vec<u8>>,
    urls: Vec<Vec<u8>>,
    alternates: Vec<Vec<u8>>,
    inline: i64,
    inline_count: i64,
    proxy: usize,
    candidate: usize,
    remote: usize,
    parts: Vec<i64>,
    body: i64,
    nodes: i64,
    count: i64,
    info: i64,
    format: Vec<u8>,
    last_error: Vec<u8>,
    last_kind: u8,
    had_empty: bool,
    selected: usize,
    names: Vec<Vec<u8>>,
    body_nonempty: bool,
    body_nil: bool,
}
fn e(kind: u8, a: i64, b: i64) -> Effect {
    Effect {
        kind,
        a,
        b,
        ..Effect::default()
    }
}
impl Flow {
    pub fn new(snapshot: Snapshot, request: Request) -> Result<Self, &'static str> {
        let mut f = Self {
            snapshot,
            request,
            q: VecDeque::new(),
            child: None,
            child_after: 0,
            proxies: vec![],
            urls: vec![],
            alternates: vec![],
            inline: 0,
            inline_count: 0,
            proxy: 0,
            candidate: 0,
            remote: 0,
            parts: vec![],
            body: 0,
            nodes: 0,
            count: 0,
            info: 0,
            format: vec![],
            last_error: vec![],
            last_kind: 0,
            had_empty: false,
            selected: 0,
            names: vec![],
            body_nonempty: false,
            body_nil: true,
        };
        match f.request.op {
            7 => f.emit(e(8, 0, 0), 1),
            8 => f.sub(7, vec![], 0, vec![], vec![], 10)?,
            9 => {
                if f.snapshot.active_url.is_empty() {
                    f.result(23);
                } else if f.snapshot.active_url == b"__merged__" {
                    f.sub(11, vec![], 0, vec![], vec![], 90)?;
                } else {
                    f.categorize(f.snapshot.active_url.clone(), 20);
                }
            }
            10 | 12 => {
                if f.request.op == 12 && f.request.id.is_empty() {
                    f.last_error = b"missing subscription id".to_vec();
                    f.result(30);
                    return Ok(f);
                }
                if let Some(i) = f.snapshot.entries.iter().position(|v| v.id == f.request.id) {
                    f.selected = i;
                    f.categorize(f.snapshot.entries[i].url.clone(), 20);
                } else {
                    f.result(3);
                }
            }
            _ => return Err("unsupported acquisition operation"),
        };
        Ok(f)
    }
    fn emit(&mut self, e: Effect, n: u8) {
        self.q.push_back((e, n));
    }
    fn file(&mut self, k: u8, path: i64, body: i64, id: Vec<u8>, text: Vec<u8>, n: u8) {
        let mut out = e(k, path, body);
        out.data = id;
        out.text = text;
        self.emit(out, n);
    }
    fn categorize(&mut self, url: Vec<u8>, n: u8) {
        let mut out = e(15, 0, 0);
        out.data = url;
        self.emit(out, n);
    }
    fn result(&mut self, code: i64) {
        let mut out = e(63, code, self.nodes);
        out.c = self.count;
        out.items = vec![self.body, self.info];
        if matches!(self.request.op, 10 | 12) {
            out.items.push(self.selected as i64);
        }
        out.text = self.last_error.clone();
        if out.text.is_empty() {
            match code {
                21 => out.text = b"subscription response contained no usable nodes".to_vec(),
                22 => {
                    out.text =
                        b"subscription network request failed; check connectivity or firewall"
                            .to_vec()
                }
                _ => {}
            }
        }
        out.data = vec![
            self.body_nonempty as u8,
            self.last_kind,
            self.body_nil as u8,
        ];
        out.b = self.nodes;
        if self.request.op == 7 {
            out.data = pack(&self.proxies);
        }
        self.emit(out, 0);
    }
    fn sub(
        &mut self,
        op: u8,
        id: Vec<u8>,
        body: i64,
        format: Vec<u8>,
        sources: Vec<i64>,
        n: u8,
    ) -> Result<(), &'static str> {
        let r = Request {
            op,
            url: if op == 8 { id.clone() } else { vec![] },
            id,
            body,
            body_nil: matches!(op, 0 | 1) && self.body_nil,
            format,
            sources,
            ..Request::default()
        };
        self.child = Some(Box::new(crate::profile_flow::Flow::new(
            self.snapshot.clone(),
            r,
        )?));
        self.child_after = n;
        self.settle()
    }
    fn settle(&mut self) -> Result<(), &'static str> {
        loop {
            let Some(child) = self.child.as_ref() else {
                return Ok(());
            };
            let out = child.next();
            if out.kind != 63 {
                return Ok(());
            }
            self.child = None;
            let code = self.child_after;
            self.child_done(code, out)?;
        }
    }
    fn child_done(&mut self, n: u8, out: Effect) -> Result<(), &'static str> {
        match n {
            10 => {
                self.proxies = unpack(&out.data)?;
                self.alt(self.request.url.clone(), 11);
            }
            21 => {
                self.proxies = unpack(&out.data)?;
                self.alt(self.urls[0].clone(), 22);
            }
            25 => {
                if out.a == 0 && out.data.first().copied().unwrap_or(0) == 1 {
                    self.parts.push(out.items[0]);
                    if self.info == 0 {
                        self.info = out.items.get(1).copied().unwrap_or(0);
                    }
                } else if out.a == 21 || out.data.get(1).copied().unwrap_or(0) == 2 {
                    self.had_empty = true;
                }
                self.remote += 1;
                self.next_remote()?;
            }
            31 => {
                if out.a == 0 {
                    self.body = out.items[0];
                    self.body_nil = out.data.get(2).copied().unwrap_or(0) == 1;
                    self.body_nonempty = out.data.first().copied().unwrap_or(0) == 1;
                    self.nodes = out.b;
                    self.count = out.c;
                    self.info = out.items.get(1).copied().unwrap_or(0);
                    self.single_fetched(true)?;
                } else {
                    self.last_error = out.text;
                    self.single_fetched(false)?;
                }
            }
            45 => {
                self.set_cache(true);
                self.result(0);
            }
            57 => {
                let mut record = e(21, 1, -1);
                record.c = self.count;
                record.data = self.snapshot.active_hash.clone();
                self.emit(record, 58);
            }
            66 => {
                self.record_single(false);
            }
            69 => {
                if out.a == 0 {
                    self.result(0);
                } else {
                    self.q.push_back((out, 0));
                }
            }
            90 => self.q.push_back((out, 0)),
            _ => return Err("invalid child continuation"),
        }
        Ok(())
    }
    fn alt(&mut self, url: Vec<u8>, n: u8) {
        self.alternates = vec![url.clone()];
        let mut out = e(16, 0, 0);
        out.data = url;
        self.emit(out, n);
    }
    fn attempt(&mut self, n: u8, with_info: bool) {
        if self.proxy >= self.proxies.len() {
            if self.request.op == 8 {
                self.result(if self.last_error.is_empty() {
                    22
                } else if self.last_kind == 2 {
                    21
                } else {
                    10
                });
            } else {
                self.fallback();
            }
            return;
        }
        let mut out = e(17, 0, 0);
        out.data = self.alternates[self.candidate].clone();
        out.text = self.proxies[self.proxy].clone();
        out.c = with_info as i64;
        self.emit(out, n);
    }
    fn next_attempt(&mut self, n: u8, with_info: bool) {
        self.candidate += 1;
        if self.candidate >= self.alternates.len() {
            self.candidate = 0;
            self.proxy += 1;
        }
        self.attempt(n, with_info);
    }
    fn next_remote(&mut self) -> Result<(), &'static str> {
        if self.remote < self.urls.len() {
            self.sub(8, self.urls[self.remote].clone(), 0, vec![], vec![], 25)
        } else {
            if self.inline_count > 0 {
                self.parts.push(self.inline);
            }
            if self.parts.is_empty() {
                if self.request.op == 9 {
                    self.fallback();
                } else {
                    self.last_error = "无法拉取该订阅节点".as_bytes().to_vec();
                    self.single_fetched(false)?;
                }
            } else {
                let mut out = e(19, 0, 0);
                out.items = self.parts.clone();
                self.emit(out, 26);
            }
            Ok(())
        }
    }
    fn use_reply(&mut self, r: &Reply) {
        self.body = r.token;
        self.body_nonempty = r.flag;
        self.body_nil = r.nil;
        self.nodes = r.info;
        self.count = r.count;
        self.names = r.names.clone();
        self.info = r.tokens.first().copied().unwrap_or(0);
    }
    fn fetched(&mut self) -> Result<(), &'static str> {
        if self.request.op == 9 {
            self.file(1, 2, 0, vec![], vec![], 50);
        } else {
            self.single_fetched(true)?;
        }
        Ok(())
    }
    fn fallback(&mut self) {
        if self.request.op == 8 {
            self.result(if self.last_error.is_empty() {
                22
            } else if self.last_kind == 2 {
                21
            } else {
                10
            });
            return;
        }
        self.file(2, 3, 0, vec![], vec![], 35);
    }
    fn fallback_entry(&mut self) {
        if let Some(i) =
            self.snapshot.entries.iter().position(|en| {
                en.url == self.snapshot.active_url || en.id == self.snapshot.active_hash
            })
        {
            self.selected = i;
            self.file(2, 1, 0, self.snapshot.entries[i].id.clone(), vec![], 42);
        } else {
            self.fallback_arbitrary();
        }
    }
    fn fallback_arbitrary(&mut self) {
        self.file(2, 3, 0, vec![], vec![], 46);
    }
    fn fallback_memory(&mut self) {
        if self.snapshot.cached_count > 0 {
            self.emit(e(23, 0, 0), 48);
        } else {
            self.result(if self.had_empty { 21 } else { 22 });
        }
    }
    fn apply_nodes(&mut self, label: bool) {
        let mut out = e(33, 0, self.nodes);
        out.data = self.snapshot.selected_node.clone();
        if label
            && !self.snapshot.active_url.is_empty()
            && self.snapshot.active_url != b"__merged__"
        {
            out.c = 1;
            out.text = self
                .snapshot
                .entries
                .iter()
                .find(|v| v.url == self.snapshot.active_url && !v.label.is_empty())
                .map(|v| v.label.clone())
                .unwrap_or_else(|| "当前订阅".as_bytes().to_vec());
        }
        self.emit(out, 0);
    }
    fn set_cache(&mut self, v: bool) {
        self.emit(e(34, v as i64, 0), 0);
    }
    fn single_fetched(&mut self, ok: bool) -> Result<(), &'static str> {
        if ok && self.count > 0 {
            self.sub(
                0,
                self.snapshot.entries[self.selected].id.clone(),
                self.body,
                b"yaml".to_vec(),
                vec![],
                66,
            )?;
        } else {
            self.file(
                2,
                1,
                0,
                self.snapshot.entries[self.selected].id.clone(),
                vec![],
                62,
            );
        }
        Ok(())
    }
    fn record_single(&mut self, cache: bool) {
        let mut out = e(21, 1, self.selected as i64);
        out.data = self.snapshot.entries[self.selected].id.clone();
        out.c = self.count;
        out.items = vec![self.info];
        if cache {
            out.text = "网络连接失败，已使用本地历史缓存".as_bytes().to_vec();
        }
        self.emit(out, 67);
    }
    pub fn next(&self) -> Effect {
        if self.q.is_empty() {
            if let Some(child) = &self.child {
                return child.next();
            }
        }
        self.q.front().map(|(e, _)| e.clone()).unwrap_or_default()
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.q.is_empty() {
            if let Some(child) = &mut self.child {
                child.advance(r)?;
                return self.settle();
            }
        }
        let Some((_, n)) = self.q.front() else {
            return Err("complete");
        };
        let n = *n;
        if r.names.len() > 100_000 || r.tokens.len() > 100_000 {
            return Err("reply limit");
        }
        self.q.pop_front();
        match n {
            0 => {}
            1 => {
                if r.flag && !self.snapshot.mixed_proxy.is_empty() {
                    self.proxies = vec![self.snapshot.mixed_proxy.clone(), b"direct".to_vec()];
                    self.result(0);
                } else {
                    self.emit(e(14, 0, 0), 2);
                }
            }
            2 => {
                self.proxies = vec![b"direct".to_vec()];
                if r.ok && r.flag {
                    if let Some(v) = r.names.first() {
                        if !v.is_empty() {
                            self.proxies.push(v.clone());
                        }
                    }
                }
                self.proxies.push(vec![]);
                self.result(0);
            }
            11 | 22 => {
                self.alternates.extend(r.names);
                self.proxy = 0;
                self.candidate = 0;
                self.attempt(if n == 11 { 12 } else { 23 }, n == 11);
            }
            12 => {
                if r.ok && r.count > 0 {
                    self.last_error.clear();
                    self.last_kind = 0;
                    self.use_reply(&r);
                    self.result(0);
                } else {
                    if !r.ok {
                        self.last_error = r.error;
                        self.last_kind = r.err_kind;
                    }
                    self.next_attempt(12, true);
                }
            }
            20 => {
                self.urls = r.names;
                self.inline = r.token;
                self.inline_count = r.count;
                if self.urls.is_empty() && self.inline_count == 0 {
                    if self.request.op == 9 {
                        self.result(23);
                    } else {
                        self.last_error = "无法拉取该订阅节点".as_bytes().to_vec();
                        self.single_fetched(false)?;
                    }
                } else if self.urls.is_empty() {
                    self.emit(e(18, 0, self.inline), 24);
                } else if self.urls.len() == 1 && self.inline_count == 0 {
                    if self.request.op == 9 {
                        self.sub(7, vec![], 0, vec![], vec![], 21)?;
                    } else {
                        self.sub(8, self.urls[0].clone(), 0, vec![], vec![], 31)?;
                    }
                } else {
                    self.next_remote()?;
                }
            }
            23 => {
                if r.ok {
                    self.use_reply(&r);
                    self.format = if self.candidate == 0 {
                        b"original".to_vec()
                    } else {
                        b"cla=1".to_vec()
                    };
                    self.emit(e(18, 0, self.body), 27);
                } else {
                    if r.err_kind == 2 {
                        self.had_empty = true;
                    }
                    self.next_attempt(23, false);
                }
            }
            24 => {
                if r.ok && r.count > 0 {
                    self.use_reply(&r);
                    self.format = b"inline".to_vec();
                    self.fetched()?;
                } else {
                    self.had_empty = true;
                    if self.request.op == 9 {
                        self.fallback();
                    } else {
                        self.last_error = r.error;
                        self.single_fetched(false)?;
                    }
                }
            }
            26 => {
                if r.ok && r.count > 0 {
                    let info = self.info;
                    self.use_reply(&r);
                    self.info = info;
                    self.format = format!("merged-{}", self.parts.len()).into_bytes();
                    self.fetched()?;
                } else if self.request.op == 9 {
                    self.fallback();
                } else {
                    self.last_error = r.error;
                    self.single_fetched(false)?;
                }
            }
            27 => {
                if r.ok && r.count > 0 {
                    let prior_info = self.info;
                    self.use_reply(&r);
                    self.info = prior_info;
                }
                self.fetched()?;
            }
            35 => {
                if r.ok {
                    self.body = r.token;
                    self.emit(e(6, 0, self.body), 36);
                } else {
                    self.fallback_entry();
                }
            }
            36 => {
                if r.count > 0 {
                    self.file(2, 4, 0, vec![], vec![], 37);
                } else {
                    self.fallback_entry();
                }
            }
            37 => {
                if r.ok
                    && r.names
                        .first()
                        .is_some_and(|v| *v == self.snapshot.active_hash)
                {
                    self.file(2, 3, 0, vec![], vec![], 41);
                } else {
                    self.fallback_entry();
                }
            }
            40 => {
                if r.flag {
                    self.file(2, 3, 0, vec![], vec![], 41);
                } else {
                    self.fallback_entry();
                }
            }
            41 => {
                if r.ok {
                    self.body = r.token;
                    self.emit(e(6, 0, self.body), 44);
                } else {
                    self.fallback_entry();
                }
            }
            44 => {
                self.nodes = r.token;
                self.count = r.count;
                self.apply_nodes(true);
                self.set_cache(true);
                self.result(0);
            }
            42 => {
                if r.ok && r.flag {
                    self.body = r.token;
                    self.emit(e(6, 0, self.body), 43);
                } else {
                    self.fallback_arbitrary();
                }
            }
            43 => {
                if r.count > 0 {
                    self.nodes = r.token;
                    self.count = r.count;
                    self.sub(
                        2,
                        self.snapshot.entries[self.selected].id.clone(),
                        0,
                        vec![],
                        vec![],
                        45,
                    )?;
                } else {
                    self.fallback_arbitrary();
                }
            }
            46 => {
                if r.ok && r.flag {
                    self.body = r.token;
                    self.emit(e(6, 0, self.body), 47);
                } else {
                    self.fallback_memory();
                }
            }
            47 => {
                if r.count > 0 {
                    self.nodes = r.token;
                    self.count = r.count;
                    self.apply_nodes(false);
                    self.set_cache(true);
                    self.result(0);
                } else {
                    self.fallback_memory();
                }
            }
            48 => {
                self.nodes = r.token;
                self.count = r.count;
                self.set_cache(true);
                self.result(0);
            }
            50 => {
                if r.ok {
                    self.file(3, 4, self.body, vec![], vec![], 51);
                } else {
                    self.result(24);
                }
            }
            51 => {
                if r.ok {
                    self.emit(e(4, 1, 0), 52);
                } else {
                    self.result(25);
                }
            }
            52 => {
                if r.ok {
                    self.file(3, 5, 0, vec![], self.snapshot.active_hash.clone(), 53);
                } else {
                    self.file(5, 1, 0, vec![], vec![], 0);
                    self.result(26);
                }
            }
            53 => {
                if r.ok {
                    self.file(3, 6, 0, vec![], self.format.clone(), 54);
                } else {
                    self.result(27);
                }
            }
            54 => {
                if r.ok {
                    self.set_cache(false);
                    self.sub(1, vec![], self.body, self.format.clone(), vec![], 57)?;
                } else {
                    self.result(28);
                }
            }
            58 => {
                self.apply_nodes(true);
                self.emit(e(12, 0, self.nodes), 59);
            }
            59 => {
                self.result(if r.ok { 0 } else { 29 });
            }
            62 => {
                if r.ok && r.flag {
                    self.body = r.token;
                    self.emit(e(6, 0, self.body), 63);
                } else {
                    self.single_error();
                }
            }
            63 => {
                if r.count > 0 {
                    self.nodes = r.token;
                    self.count = r.count;
                    self.record_single(true);
                } else {
                    self.single_error();
                }
            }
            67 => {
                if self.request.op == 12
                    && self.snapshot.active_url == b"__merged__"
                    && self.snapshot.entries[self.selected].enabled
                {
                    self.sub(6, vec![], 0, vec![], vec![], 69)?;
                } else {
                    self.result(0);
                }
            }
            68 => self.result(10),
            _ => return Err("invalid acquisition continuation"),
        }
        Ok(())
    }
    fn single_error(&mut self) {
        if self.last_error.is_empty() {
            self.last_error = "拉取失败".as_bytes().to_vec();
        }
        let mut out = e(21, 2, self.selected as i64);
        out.data = self.snapshot.entries[self.selected].id.clone();
        out.text = self.last_error.clone();
        self.emit(out, 68);
    }
}
fn pack(v: &[Vec<u8>]) -> Vec<u8> {
    let mut b = vec![];
    for s in v {
        b.extend_from_slice(&(s.len() as u64).to_le_bytes());
        b.extend_from_slice(s);
    }
    b
}
fn unpack(b: &[u8]) -> Result<Vec<Vec<u8>>, &'static str> {
    let mut out = vec![];
    let mut p = 0;
    while p < b.len() {
        let n =
            u64::from_le_bytes(b.get(p..p + 8).ok_or("proxy frame")?.try_into().unwrap()) as usize;
        p += 8;
        let end = p.checked_add(n).ok_or("proxy length")?;
        out.push(b.get(p..end).ok_or("proxy frame")?.to_vec());
        p = end;
    }
    Ok(out)
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::profile_flow::Entry;
    fn snapshot() -> Snapshot {
        Snapshot {
            entries: vec![Entry {
                id: b"a".to_vec(),
                url: b"url".to_vec(),
                hash: b"hash".to_vec(),
                label: b"label".to_vec(),
                enabled: true,
                url_nonempty: true,
                ..Entry::default()
            }],
            active_url: b"url".to_vec(),
            active_hash: b"hash".to_vec(),
            selected_node: b"node".to_vec(),
            ..Snapshot::default()
        }
    }
    fn request(op: u8) -> Request {
        Request {
            op,
            id: b"a".to_vec(),
            url: b"url".to_vec(),
            ..Request::default()
        }
    }
    fn reply(e: &Effect) -> Reply {
        let mut r = Reply {
            ok: true,
            token: 10,
            info: 20,
            count: 1,
            flag: true,
            names: vec![b"node".to_vec()],
            ..Reply::default()
        };
        match e.kind {
            8 => r.flag = false,
            14 => {
                r.flag = false;
                r.names.clear();
            }
            15 => {
                r.names = vec![b"remote".to_vec()];
                r.count = 0;
            }
            16 => r.names.clear(),
            6 => {
                r.token = 20;
            }
            17 => r.tokens = vec![30],
            _ => {}
        }
        r
    }
    fn run(s: Snapshot, r: Request, mut answer: impl FnMut(&Effect) -> Reply) -> Vec<Effect> {
        let mut f = crate::profile_flow::Flow::new(s, r).unwrap();
        let mut t = vec![];
        for _ in 0..300 {
            let e = f.next();
            if e.kind == 0 {
                return t;
            }
            let out = answer(&e);
            t.push(e);
            f.advance(out).unwrap();
        }
        panic!("loop")
    }
    #[test]
    fn proxy_policy_and_download_attempt_order() {
        let t = run(snapshot(), request(7), reply);
        assert_eq!(
            unpack(&t.last().unwrap().data).unwrap(),
            vec![b"direct".to_vec(), vec![]]
        );
        let mut s = snapshot();
        s.mixed_proxy = b"127.0.0.1:99".to_vec();
        let t = run(s, request(7), |e| Reply {
            flag: true,
            ..reply(e)
        });
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![8, 63]);
        let t = run(snapshot(), request(8), |e| {
            let mut r = reply(e);
            if e.kind == 16 {
                r.names = vec![b"alt".to_vec()];
            }
            if e.kind == 17 {
                r.ok = false;
                r.error = b"offline".to_vec();
            }
            r
        });
        let attempts: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 17)
            .map(|e| (e.data.clone(), e.text.clone()))
            .collect();
        assert_eq!(
            attempts,
            vec![
                (b"url".to_vec(), b"direct".to_vec()),
                (b"alt".to_vec(), b"direct".to_vec()),
                (b"url".to_vec(), vec![]),
                (b"alt".to_vec(), vec![])
            ]
        );
        assert_eq!(t.last().unwrap().a, 10);
    }
    #[test]
    fn download_zero_nodes_continues_but_fetch_nil_error_stops() {
        let t = run(snapshot(), request(8), |e| Reply {
            count: if e.kind == 17 { 0 } else { reply(e).count },
            ..reply(e)
        });
        assert_eq!(t.iter().filter(|e| e.kind == 17).count(), 2);
        assert_eq!(t.last().unwrap().a, 22);
        let t = run(snapshot(), request(9), |e| {
            let mut r = reply(e);
            if e.kind == 17 {
                r.count = 0;
            }
            if e.kind == 18 {
                r.ok = false;
                r.count = 0;
            }
            r
        });
        assert_eq!(t.iter().filter(|e| e.kind == 17).count(), 1);
        assert_eq!(t.last().unwrap().a, 0);
        assert_eq!(t.last().unwrap().c, 0);
    }
    #[test]
    fn fresh_install_order_and_source_format_failures() {
        let t = run(snapshot(), request(9), reply);
        let pos = t.iter().position(|e| e.kind == 34).unwrap();
        assert_eq!(t[pos].a, 0);
        assert_eq!(t[pos + 1].kind, 1);
        assert_eq!(t[pos + 1].a, 1);
        assert!(t.iter().any(|e| e.kind == 21));
        assert_eq!(t.last().unwrap().a, 0);
        for (path, code) in [(4, 25), (5, 27), (6, 28)] {
            let t = run(snapshot(), request(9), |e| Reply {
                ok: !(e.kind == 3 && e.a == path),
                ..reply(e)
            });
            assert_eq!(t.last().unwrap().a, code);
        }
    }
    #[test]
    fn matching_cache_fallback_reloads_after_match_and_accepts_empty_reparse() {
        let mut parses = 0;
        let t = run(snapshot(), request(9), |e| {
            let mut r = reply(e);
            if e.kind == 17 {
                r.ok = false;
                r.error = b"offline".to_vec();
            }
            if e.kind == 6 {
                parses += 1;
                r.count = if parses == 2 { 0 } else { 1 };
            }
            if e.kind == 2 && e.a == 4 {
                r.names = vec![b"hash".to_vec()];
            }
            r
        });
        assert_eq!(parses, 2);
        assert_eq!(t.iter().filter(|e| e.kind == 2 && e.a == 3).count(), 2);
        assert_eq!(t.last().unwrap().a, 0);
        assert_eq!(t.last().unwrap().c, 0);
    }
    #[test]
    fn saved_cache_install_error_is_ignored_by_active_fetch() {
        let mut reads = 0;
        let t = run(snapshot(), request(9), |e| {
            let mut r = reply(e);
            if e.kind == 17 {
                r.ok = false;
            }
            if e.kind == 2 && e.a == 3 {
                r.ok = false;
            }
            if e.kind == 2 && e.a == 1 {
                reads += 1;
                if reads == 2 {
                    r.ok = false;
                }
            }
            r
        });
        assert_eq!(reads, 2);
        assert_eq!(t.last().unwrap().a, 0);
        assert!(t.iter().any(|e| e.kind == 34 && e.a == 1));
    }
    #[test]
    fn single_fetch_cache_warning_and_info_preserved() {
        let t = run(snapshot(), request(10), |e| {
            let mut r = reply(e);
            if e.kind == 17 {
                r.ok = false;
                r.error = b"offline".to_vec();
            }
            r
        });
        let record = t.iter().find(|e| e.kind == 21).unwrap();
        assert_eq!(record.text, "网络连接失败，已使用本地历史缓存".as_bytes());
        assert_eq!(record.c, 1);
        assert_eq!(t.last().unwrap().a, 0);
        let t = run(snapshot(), request(10), reply);
        let record = t.iter().find(|e| e.kind == 21).unwrap();
        assert_eq!(record.items, vec![30]);
    }
    #[test]
    fn multi_source_order_and_first_quota_survive_pure_merge() {
        let t = run(snapshot(), request(10), |e| {
            let mut r = reply(e);
            if e.kind == 15 {
                r.names = vec![b"one".to_vec(), b"two".to_vec()];
                r.count = 1;
                r.token = 77;
            }
            if e.kind == 17 {
                r.token = if e.data == b"one" { 11 } else { 12 };
                r.tokens = vec![if e.data == b"one" { 31 } else { 32 }];
            }
            r
        });
        let merge = t.iter().find(|e| e.kind == 19).unwrap();
        assert_eq!(merge.items, vec![11, 12, 77]);
        let record = t.iter().find(|e| e.kind == 21).unwrap();
        assert_eq!(record.items, vec![31]);
    }
    #[test]
    fn active_fallback_to_memory_only_after_all_files_fail() {
        let mut s = snapshot();
        s.cached_count = 3;
        let t = run(s, request(9), |e| {
            let mut r = reply(e);
            if e.kind == 17 || e.kind == 2 {
                r.ok = false;
            }
            if e.kind == 23 {
                r.token = 88;
                r.count = 3;
            }
            r
        });
        assert!(t.iter().any(|e| e.kind == 23));
        assert_eq!(t.last().unwrap().b, 88);
        assert_eq!(t.last().unwrap().c, 3);
    }
}
#[cfg(test)]
mod failure_regressions {
    use super::*;
    use crate::profile_flow::Entry;
    #[test]
    fn empty_single_input_records_exact_error_for_original_id() {
        let s = Snapshot {
            entries: vec![Entry {
                id: b"target".to_vec(),
                ..Entry::default()
            }],
            ..Snapshot::default()
        };
        let r = Request {
            op: 10,
            id: b"target".to_vec(),
            ..Request::default()
        };
        let mut f = crate::profile_flow::Flow::new(s, r).unwrap();
        let mut recorded = false;
        for _ in 0..20 {
            let e = f.next();
            if e.kind == 0 {
                break;
            }
            let mut r = Reply {
                ok: true,
                ..Reply::default()
            };
            if e.kind == 2 {
                r.ok = false;
            }
            if e.kind == 21 {
                recorded = true;
                assert_eq!(e.data, b"target");
                assert_eq!(e.text, "无法拉取该订阅节点".as_bytes());
            }
            if e.kind == 63 {
                assert_eq!(e.a, 10);
                assert_eq!(e.text, "无法拉取该订阅节点".as_bytes());
            }
            f.advance(r).unwrap();
        }
        assert!(recorded);
    }
    #[test]
    fn no_usable_sentinel_survives_all_download_attempts() {
        let mut f = crate::profile_flow::Flow::new(
            Snapshot::default(),
            Request {
                op: 8,
                url: b"url".to_vec(),
                ..Request::default()
            },
        )
        .unwrap();
        for _ in 0..20 {
            let e = f.next();
            if e.kind == 0 {
                break;
            }
            let mut r = Reply {
                ok: true,
                ..Reply::default()
            };
            if e.kind == 17 {
                r.ok = false;
                r.err_kind = 2;
                r.error = b"no usable".to_vec();
            }
            if e.kind == 63 {
                assert_eq!(e.a, 21);
                assert_eq!(e.data.get(1), Some(&2));
            }
            f.advance(r).unwrap();
        }
    }
}
#[cfg(test)]
mod boundary_regressions {
    use super::*;
    use crate::profile_flow::Entry;
    #[test]
    fn refresh_empty_id_differs_from_fetch_single_empty_id() {
        for (op, code) in [(12, 30), (10, 3)] {
            let f = crate::profile_flow::Flow::new(
                Snapshot::default(),
                Request {
                    op,
                    ..Request::default()
                },
            )
            .unwrap();
            assert_eq!(f.next().kind, 63);
            assert_eq!(f.next().a, code);
        }
    }
    #[test]
    fn nil_successful_download_does_not_write_entry_cache() {
        let s = Snapshot {
            entries: vec![Entry {
                id: b"a".to_vec(),
                url: b"url".to_vec(),
                ..Entry::default()
            }],
            ..Snapshot::default()
        };
        let mut f = crate::profile_flow::Flow::new(
            s,
            Request {
                op: 10,
                id: b"a".to_vec(),
                ..Request::default()
            },
        )
        .unwrap();
        let mut record = false;
        for _ in 0..40 {
            let e = f.next();
            if e.kind == 0 {
                break;
            }
            assert_ne!(e.kind, 3, "nil cache body must not be written");
            let mut r = Reply {
                ok: true,
                ..Reply::default()
            };
            match e.kind {
                15 => r.names = vec![b"remote".to_vec()],
                17 => {
                    r.token = 12;
                    r.info = 13;
                    r.count = 1;
                    r.nil = true;
                }
                21 => record = true,
                63 => assert_eq!(e.a, 0),
                _ => {}
            }
            f.advance(r).unwrap();
        }
        assert!(record);
    }
}
