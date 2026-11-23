//! Registry lifecycle policy. Byte strings preserve Go identity semantics; IO remains effects.
use std::collections::VecDeque;
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Entry {
    pub id: Vec<u8>,
    pub url: Vec<u8>,
    pub label: Vec<u8>,
    pub enabled: bool,
    pub has_enabled: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub entries: Vec<Entry>,
    pub active_url: Vec<u8>,
    pub from_cache: bool,
    pub store: bool,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
    pub id: Vec<u8>,
    pub url: Vec<u8>,
    pub label: Vec<u8>,
    pub prefix: Vec<u8>,
    pub hash: Vec<u8>,
    pub default_label: Vec<u8>,
    pub presence: u16,
    pub enabled: bool,
    pub valid: bool,
    pub has_info: bool,
    pub hours: i64,
    pub node_count: i64,
    pub label_runes: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub index: i64,
    pub aux: i64,
    pub data: Vec<u8>,
    pub entries: Vec<Entry>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub ok: bool,
    pub value: i64,
    pub flag: bool,
    pub data: Vec<u8>,
    pub entries: Vec<Entry>,
    pub snapshot: Option<Snapshot>,
}
#[derive(Clone, Debug)]
enum Task {
    Emit(Effect, u8),
    Save(u8),
    Seal(usize),
    Write,
    SaveEnd(u8),
    LoadNext,
    Ensure(u8),
    StartDelete,
    StartActivate,
    Record(u8),
    UseNext(usize, u8),
    Clear(u8),
}
#[derive(Clone, Debug)]
pub struct Flow {
    pub snapshot: Snapshot,
    request: Request,
    queue: VecDeque<Task>,
    original: Vec<Entry>,
    loaded: Vec<Entry>,
    load_index: usize,
    save_failed: bool,
    selected: usize,
    selected_entry: Option<Entry>,
    ensure_after: u8,
    fetch_count: i64,
    fetch_error: Vec<u8>,
}
fn effect(kind: u8) -> Effect {
    Effect {
        kind,
        index: -1,
        ..Effect::default()
    }
}
impl Flow {
    pub fn new(snapshot: Snapshot, request: Request) -> Result<Self, &'static str> {
        if request.op > 13 || request.presence & !3 != 0 || snapshot.entries.len() > 100_000 {
            return Err("invalid request");
        }
        if request.op == 5
            && snapshot
                .entries
                .iter()
                .filter(|e| e.url == snapshot.active_url)
                .count()
                > 1
        {
            return Err("duplicate active comparator requires Go");
        }
        let mut f = Self {
            original: snapshot.entries.clone(),
            snapshot,
            request,
            queue: VecDeque::new(),
            loaded: vec![],
            load_index: 0,
            save_failed: false,
            selected: 0,
            selected_entry: None,
            ensure_after: 0,
            fetch_count: 0,
            fetch_error: vec![],
        };
        f.start();
        f.pump();
        Ok(f)
    }
    fn emit(&mut self, e: Effect, after: u8) {
        self.queue.push_back(Task::Emit(e, after));
    }
    fn simple(&mut self, k: u8) {
        self.emit(effect(k), 0);
    }
    fn result(&mut self, status: i64, code: i64) {
        let mut e = effect(63);
        e.aux = status;
        e.index = code;
        self.emit(e, 0);
    }
    fn found(&self) -> Option<usize> {
        self.snapshot
            .entries
            .iter()
            .position(|e| e.id == self.request.id)
    }
    fn save(&mut self, cont: u8) {
        self.queue.push_back(Task::Save(cont));
    }
    fn start(&mut self) {
        match self.request.op {
            0 => {
                self.snapshot.entries.clear();
                self.simple(32);
                if self.snapshot.store {
                    self.emit(effect(1), 1);
                } else {
                    self.queue.push_back(Task::Ensure(2));
                }
            }
            1 => self.save(0),
            2 => self.queue.push_back(Task::Ensure(1)),
            3 | 4 => {
                let i = if self.request.op == 3 {
                    self.found()
                } else {
                    self.snapshot
                        .entries
                        .iter()
                        .position(|e| e.url == self.request.url || e.id == self.request.hash)
                };
                let mut e = effect(63);
                e.aux = 200;
                e.index = 20;
                if let Some(i) = i {
                    e.entries.push(self.snapshot.entries[i].clone());
                    e.data = (i as u64).to_le_bytes().to_vec();
                }
                self.emit(e, 0);
            }
            5 => {
                let mut order: Vec<_> = (0..self.snapshot.entries.len()).collect();
                order.sort_by(|&i, &j| {
                    let a = &self.snapshot.entries[i];
                    let b = &self.snapshot.entries[j];
                    (b.url == self.snapshot.active_url)
                        .cmp(&(a.url == self.snapshot.active_url))
                        .then_with(|| a.label.cmp(&b.label))
                });
                let mut e = effect(63);
                e.aux = 200;
                e.index = 21;
                for i in order {
                    e.entries.push(self.snapshot.entries[i].clone());
                    e.data.extend_from_slice(&(i as u64).to_le_bytes());
                }
                self.emit(effect(6), 31);
                self.emit(e, 0);
            }
            6 => {
                if self.request.url.is_empty() {
                    self.result(400, 1);
                } else if !self.request.valid {
                    self.result(400, 2);
                } else if self
                    .snapshot
                    .entries
                    .iter()
                    .any(|e| e.id == self.request.hash || e.url == self.request.url)
                {
                    self.result(409, 3);
                } else {
                    let en = Entry {
                        id: self.request.hash.clone(),
                        url: self.request.url.clone(),
                        label: if self.request.label.is_empty() {
                            self.request.default_label.clone()
                        } else {
                            self.request.label.clone()
                        },
                        enabled: self.request.presence & 1 != 0 && self.request.enabled,
                        has_enabled: true,
                    };
                    self.append(en, -1);
                    self.save(3);
                }
            }
            7 => {
                if self.request.id.is_empty() {
                    self.result(400, 4);
                } else if let Some(i) = self.found() {
                    let mut mask = 0;
                    let en = &mut self.snapshot.entries[i];
                    if !self.request.label.is_empty() {
                        en.label = self.request.label.clone();
                        mask |= 2;
                    }
                    if self.request.presence & 2 != 0 {
                        mask |= 4;
                    }
                    if self.request.presence & 1 != 0 {
                        en.enabled = self.request.enabled;
                        mask |= 8;
                    }
                    if !self.request.url.is_empty() && self.request.valid {
                        en.url = self.request.url.clone();
                        mask |= 1;
                    }
                    let mut e = effect(34);
                    e.index = i as i64;
                    e.aux = mask;
                    e.entries.push(en.clone());
                    self.emit(e, 0);
                    self.selected = i;
                    self.save(4);
                } else {
                    self.result(404, 5);
                }
            }
            8 => {
                if self.request.label.is_empty() || self.request.label_runes > 40 {
                    self.result(400, 6);
                } else {
                    let indices: Vec<_> = self
                        .snapshot
                        .entries
                        .iter()
                        .enumerate()
                        .filter_map(|(i, e)| (e.id == self.request.id).then_some(i))
                        .collect();
                    if indices.is_empty() {
                        self.result(404, 5);
                    } else {
                        for i in indices {
                            self.snapshot.entries[i].label = self.request.label.clone();
                            let mut e = effect(34);
                            e.index = i as i64;
                            e.aux = 2;
                            e.entries.push(self.snapshot.entries[i].clone());
                            self.emit(e, 0);
                        }
                        self.save(5);
                    }
                }
            }
            9 => self.queue.push_back(Task::Record(6)),
            10 => {
                self.record_error();
                self.save(6);
            }
            11 => {
                if !(0..=720).contains(&self.request.hours) {
                    self.result(400, 15);
                } else {
                    let mut e = effect(42);
                    e.aux = self.request.hours;
                    self.emit(e, 0);
                    self.emit(effect(12), 30);
                }
            }
            12 => {
                if self.request.id.is_empty() {
                    self.result(400, 4);
                } else {
                    self.emit(effect(6), 12);
                }
            }
            13 => self.emit(effect(6), 13),
            _ => unreachable!(),
        }
    }
    fn append(&mut self, en: Entry, source: i64) {
        self.snapshot.entries.push(en.clone());
        let mut e = effect(33);
        e.index = source;
        e.entries.push(en);
        self.emit(e, 0);
    }
    fn record_error(&mut self) {
        let indices: Vec<_> = self
            .snapshot
            .entries
            .iter()
            .enumerate()
            .filter_map(|(i, e)| (e.id == self.request.id).then_some(i))
            .collect();
        for i in indices {
            let mut e = effect(41);
            e.index = i as i64;
            e.data = self.fetch_error.clone();
            self.emit(e, 0);
        }
    }
    fn finish_save(&mut self, c: u8) {
        let ok = !self.save_failed;
        match c {
            0 => self.result(if ok { 200 } else { 500 }, if ok { 0 } else { 22 }),
            1 | 6 => self.result(200, 0),
            2 => {
                self.simple(20);
                self.result(200, 0);
            }
            3 => {
                if ok {
                    let mut e = effect(63);
                    e.aux = 200;
                    e.index = 23;
                    e.entries
                        .push(self.snapshot.entries.last().unwrap().clone());
                    self.emit(e, 0);
                } else {
                    self.snapshot.entries.pop();
                    self.simple(44);
                    self.result(500, 9);
                }
            }
            4 | 5 => {
                if ok {
                    let mut e = effect(63);
                    e.aux = 200;
                    if c == 4 {
                        e.index = 24;
                        e.entries.push(self.snapshot.entries[self.selected].clone());
                    } else {
                        e.index = 0;
                    }
                    self.emit(e, 0);
                } else {
                    self.result(500, if c == 4 { 10 } else { 11 });
                }
            }
            7 => {
                if ok {
                    for aux in 0..2 {
                        let mut e = effect(18);
                        e.data = self.request.id.clone();
                        e.aux = aux;
                        self.emit(e, 0);
                    }
                    self.result(200, 25);
                } else {
                    self.restore();
                    self.save(9);
                }
            }
            8 => self.result(502, 13),
            9 => self.result(500, 12),
            10 => {
                let mut e = effect(63);
                e.aux = 502;
                e.index = 14;
                e.data = self.fetch_error.clone();
                self.emit(e, 0);
            }
            11 => {
                let mut e = effect(63);
                e.aux = 200;
                e.index = 26;
                e.data = self.fetch_count.to_le_bytes().to_vec();
                if let Some(en) = &self.selected_entry {
                    e.entries.push(en.clone());
                }
                self.emit(e, 0);
            }
            _ => unreachable!(),
        }
    }
    fn restore(&mut self) {
        self.snapshot.entries = self.original.clone();
        self.simple(35);
    }
    fn clear(&mut self, cont: u8) {
        self.simple(38);
        self.simple(13);
        if self.snapshot.store {
            self.simple(14);
            self.simple(15);
        }
        self.emit(effect(12), 0);
        self.queue.push_back(Task::Clear(cont));
    }
    fn remove_profile(&mut self) {
        for suffix in 0..3 {
            let mut e = effect(17);
            e.aux = suffix;
            self.emit(e, 0);
        }
    }
    fn set_cache(&mut self, v: bool) {
        self.snapshot.from_cache = v;
        let mut e = effect(39);
        e.aux = v as i64;
        self.emit(e, 0);
    }
    fn set_active(&mut self, url: Vec<u8>) {
        self.snapshot.active_url = url.clone();
        let mut e = effect(37);
        e.data = url;
        self.emit(e, 0);
    }
    fn pump(&mut self) {
        while let Some(t) = self.queue.front().cloned() {
            if matches!(t, Task::Emit(..)) {
                break;
            }
            self.queue.pop_front();
            match t {
                Task::Save(c) => {
                    self.save_failed = false;
                    if self.snapshot.store {
                        for i in 0..self.snapshot.entries.len() {
                            self.queue.push_back(Task::Seal(i));
                        }
                        self.queue.push_back(Task::Write);
                    }
                    self.queue.push_back(Task::SaveEnd(c));
                }
                Task::Seal(i) => {
                    if !self.save_failed {
                        let mut e = effect(4);
                        e.index = i as i64;
                        e.data = self.snapshot.entries[i].url.clone();
                        self.queue.push_front(Task::Emit(e, 4));
                    }
                }
                Task::Write => {
                    if !self.save_failed {
                        self.queue.push_front(Task::Emit(effect(5), 5));
                    }
                }
                Task::SaveEnd(c) => self.finish_save(c),
                Task::LoadNext => {
                    if self.load_index >= self.loaded.len() {
                        self.queue.push_back(Task::Ensure(2));
                    } else {
                        let i = self.load_index;
                        self.load_index += 1;
                        if self.loaded[i].url.is_empty() {
                            self.queue.push_back(Task::LoadNext);
                        } else {
                            let mut e = effect(2);
                            e.index = i as i64;
                            e.data = self.loaded[i].url.clone();
                            self.emit(e, 2);
                        }
                    }
                }
                Task::Ensure(c) => {
                    let url = self.request.url.clone();
                    if url.is_empty()
                        || url == b"__merged__"
                        || self.snapshot.entries.iter().any(|e| e.url == url)
                    {
                        self.save_failed = false;
                        self.finish_save(c);
                    } else {
                        self.ensure_after = c;
                        let mut e = effect(3);
                        e.index = -1;
                        e.data = url;
                        self.emit(e, 3);
                    }
                }
                Task::StartDelete => {
                    if let Some(i) = self.found() {
                        let active = self.snapshot.entries[i].url == self.snapshot.active_url;
                        self.snapshot.entries.retain(|e| e.id != self.request.id);
                        let mut e = effect(36);
                        e.data = self.request.id.clone();
                        self.emit(e, 0);
                        if self.snapshot.active_url == b"__merged__"
                            && self.snapshot.entries.iter().any(|e| e.enabled)
                        {
                            self.emit(effect(11), 11);
                        } else if active || self.snapshot.active_url == b"__merged__" {
                            self.set_active(vec![]);
                            self.set_cache(false);
                            self.clear(1);
                        } else {
                            self.save(7);
                        }
                    } else {
                        self.result(404, 5);
                    }
                }
                Task::StartActivate => {
                    if self.request.id == b"__merged__" {
                        self.emit(effect(10), 10);
                    } else if let Some(i) = self.found() {
                        self.selected = i;
                        self.selected_entry = Some(self.snapshot.entries[i].clone());
                        if self.snapshot.entries[i].url == self.snapshot.active_url {
                            self.emit(effect(7), 7);
                        } else {
                            self.activate();
                        }
                    } else {
                        self.result(404, 5);
                    }
                }
                Task::Record(c) => self.queue.push_back(Task::UseNext(0, c)),
                Task::UseNext(mut i, c) => {
                    while i < self.snapshot.entries.len()
                        && self.snapshot.entries[i].id != self.request.id
                    {
                        i += 1;
                    }
                    if i == self.snapshot.entries.len() {
                        self.save(c);
                    } else {
                        let mut e = effect(19);
                        e.index = i as i64;
                        e.aux = c as i64;
                        self.emit(e, 19);
                    }
                }
                Task::Clear(c) => {
                    if c == 1 {
                        self.simple(16);
                        self.remove_profile();
                        self.save(7);
                    } else {
                        self.set_cache(false);
                        self.emit(effect(8), 8);
                    }
                }
                Task::Emit(..) => unreachable!(),
            }
        }
    }
    fn activate(&mut self) {
        self.set_active(self.snapshot.entries[self.selected].url.clone());
        self.clear(2);
    }
    pub fn next(&self) -> Effect {
        match self.queue.front() {
            Some(Task::Emit(e, _)) => e.clone(),
            None => effect(0),
            _ => unreachable!(),
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        let Some(Task::Emit(e, after)) = self.queue.front().cloned() else {
            return Err("complete");
        };
        if r.entries.len() > 100_000
            || r.snapshot
                .as_ref()
                .is_some_and(|s| s.entries.len() > 100_000)
        {
            return Err("reply limit");
        }
        if after == 3 && (!r.ok || r.entries.len() != 1) {
            return Err("describe requires one entry");
        }
        if matches!(after, 8..=11) && r.snapshot.is_none() {
            return Err("composite snapshot required");
        }
        self.queue.pop_front();
        if matches!(after, 8..=11) {
            self.snapshot = r.snapshot.clone().unwrap();
        }
        match after {
            1 => {
                self.loaded = if r.ok { r.entries } else { vec![] };
                self.queue.push_back(Task::LoadNext);
            }
            2 => {
                if r.ok {
                    let i = e.index as usize;
                    self.loaded[i].url = r.data.clone();
                    if self.loaded[i].id.is_empty() || self.loaded[i].label.is_empty() {
                        let mut out = effect(3);
                        out.index = e.index;
                        out.data = r.data;
                        self.emit(out, 3);
                    } else {
                        let mut en = self.loaded[i].clone();
                        if !en.has_enabled {
                            en.enabled = true;
                        }
                        self.append(en, e.index);
                        self.queue.push_back(Task::LoadNext);
                    }
                } else {
                    self.queue.push_back(Task::LoadNext);
                }
            }
            3 => {
                if !r.ok {
                    return Err("describe facts unavailable");
                }
                let facts = &r.entries[0];
                if e.index >= 0 {
                    let i = e.index as usize;
                    let mut en = self.loaded[i].clone();
                    if en.id.is_empty() {
                        en.id = facts.id.clone();
                    }
                    if en.label.is_empty() {
                        en.label = facts.label.clone();
                    }
                    if !en.has_enabled {
                        en.enabled = true;
                    }
                    self.append(en, e.index);
                    self.queue.push_back(Task::LoadNext);
                } else {
                    let en = Entry {
                        id: facts.id.clone(),
                        url: e.data,
                        label: facts.label.clone(),
                        enabled: true,
                        has_enabled: true,
                    };
                    self.append(en, -1);
                    self.save(self.ensure_after);
                }
            }
            4 | 5 if !r.ok => {
                self.save_failed = true;
            }
            12 => {
                if r.flag {
                    self.result(409, 7);
                } else {
                    self.queue.push_back(Task::StartDelete);
                }
            }
            13 => {
                if r.flag {
                    self.result(409, 8);
                } else {
                    self.queue.push_back(Task::StartActivate);
                }
            }
            7 => {
                if r.flag {
                    self.result(200, 27);
                } else {
                    self.activate();
                }
            }
            8 => {
                self.fetch_count = r.value;
                if r.ok {
                    if self.snapshot.from_cache {
                        self.fetch_count = r.value;
                    }
                    self.request.node_count = self.fetch_count;
                    self.queue.push_back(Task::Record(11));
                } else {
                    self.fetch_error = r.data;
                    let mut out = effect(9);
                    out.index = self.selected as i64;
                    out.data = self.request.id.clone();
                    self.emit(out, 9);
                }
            }
            9 => {
                if r.ok {
                    self.set_cache(true);
                    self.fetch_count = r.value;
                    self.request.node_count = r.value;
                    self.queue.push_back(Task::Record(11));
                } else {
                    self.record_error();
                    self.save(10);
                }
            }
            10 => {
                let mut out = effect(63);
                out.aux = if r.ok { 200 } else { 502 };
                out.index = if r.ok { 28 } else { 17 };
                out.data = if r.ok {
                    r.value.to_le_bytes().to_vec()
                } else {
                    r.data
                };
                self.emit(out, 0);
            }
            11 => {
                if r.ok {
                    self.save(7);
                } else {
                    self.fetch_error = r.data;
                    self.restore();
                    self.save(8);
                }
            }
            19 => {
                let mut out = effect(40);
                out.index = e.index;
                out.aux = r.value;
                out.data = vec![0];
                self.emit(out, 0);
                if self.request.op == 9 && self.request.has_info {
                    let mut time = effect(19);
                    time.index = e.index;
                    time.aux = e.aux;
                    self.emit(time, 20);
                } else {
                    self.queue
                        .push_back(Task::UseNext(e.index as usize + 1, e.aux as u8));
                }
            }
            20 => {
                let mut out = effect(40);
                out.index = e.index;
                out.aux = r.value;
                out.data = vec![1];
                self.emit(out, 0);
                self.queue
                    .push_back(Task::UseNext(e.index as usize + 1, e.aux as u8));
            }
            30 => self.result(if r.ok { 200 } else { 500 }, if r.ok { 29 } else { 16 }),
            _ => {}
        }
        self.pump();
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn en(id: &str, url: &str) -> Entry {
        Entry {
            id: id.as_bytes().to_vec(),
            url: url.as_bytes().to_vec(),
            label: id.as_bytes().to_vec(),
            enabled: true,
            has_enabled: true,
        }
    }
    fn snap() -> Snapshot {
        Snapshot {
            entries: vec![en("a", "url-a"), en("b", "url-b")],
            active_url: b"url-a".to_vec(),
            store: true,
            ..Snapshot::default()
        }
    }
    fn req(op: u8) -> Request {
        Request {
            op,
            id: b"a".to_vec(),
            url: b"url-c".to_vec(),
            hash: b"c".to_vec(),
            default_label: b"C".to_vec(),
            valid: true,
            ..Request::default()
        }
    }
    fn run(
        s: Snapshot,
        r: Request,
        mut respond: impl FnMut(&Effect, &Snapshot) -> Reply,
    ) -> (Flow, Vec<Effect>) {
        let mut f = Flow::new(s, r).unwrap();
        let mut trace = vec![];
        for _ in 0..200 {
            let e = f.next();
            if e.kind == 0 {
                return (f, trace);
            }
            let reply = respond(&e, &f.snapshot);
            trace.push(e);
            f.advance(reply).unwrap();
        }
        panic!("loop")
    }
    fn ok(e: &Effect, s: &Snapshot) -> Reply {
        Reply {
            ok: true,
            value: 123,
            data: e.data.clone(),
            entries: if e.kind == 3 {
                vec![en("generated", "unused")]
            } else {
                vec![]
            },
            snapshot: matches!(e.kind, 8..=11).then(|| s.clone()),
            ..Reply::default()
        }
    }
    fn kinds(t: &[Effect]) -> Vec<u8> {
        t.iter().map(|e| e.kind).collect()
    }
    fn code(t: &[Effect]) -> i64 {
        t.last().unwrap().index
    }
    #[test]
    fn add_rollback_and_default_disabled() {
        let (f, t) = run(snap(), req(6), ok);
        assert!(!f.snapshot.entries[2].enabled);
        assert_eq!(kinds(&t), vec![33, 4, 4, 4, 5, 63]);
        let (f, t) = run(snap(), req(6), |e, s| {
            let mut r = ok(e, s);
            if e.kind == 5 {
                r.ok = false;
            }
            r
        });
        assert_eq!(f.snapshot.entries, snap().entries);
        assert_eq!(code(&t), 9);
        assert_eq!(t[t.len() - 2].kind, 44);
    }
    #[test]
    fn update_first_duplicate_ignores_bad_url_and_keeps_failed_changes() {
        let mut s = snap();
        s.entries.push(en("a", "url-x"));
        let mut r = req(7);
        r.label = b"new".to_vec();
        r.valid = false;
        let (f, t) = run(s, r, |e, s| {
            let mut r = ok(e, s);
            if e.kind == 4 {
                r.ok = false;
            }
            r
        });
        assert_eq!(f.snapshot.entries[0].label, b"new");
        assert_eq!(f.snapshot.entries[0].url, b"url-a");
        assert_eq!(f.snapshot.entries[2].label, b"a");
        assert_eq!(code(&t), 10);
        assert!(!kinds(&t).contains(&5));
    }
    #[test]
    fn rename_all_duplicates_and_validation() {
        let mut s = snap();
        s.entries.push(en("a", "x"));
        let mut r = req(8);
        r.label = b"X".to_vec();
        r.label_runes = 1;
        let (f, t) = run(s, r, ok);
        assert_eq!(kinds(&t).iter().filter(|&&k| k == 34).count(), 2);
        assert_eq!(f.snapshot.entries[2].label, b"X");
        let mut r = req(8);
        r.label = b"x".to_vec();
        r.label_runes = 41;
        assert_eq!(code(&run(snap(), r, ok).1), 6);
    }
    #[test]
    fn ensure_existing_blank_and_merged_do_not_write() {
        for url in [b"url-a".to_vec(), vec![], b"__merged__".to_vec()] {
            let mut r = req(2);
            r.url = url;
            assert_eq!(kinds(&run(snap(), r, ok).1), vec![63]);
        }
        let (f, t) = run(snap(), req(2), ok);
        assert_eq!(t[0].kind, 3);
        assert!(f.snapshot.entries.last().unwrap().enabled);
    }
    #[test]
    fn load_skips_empty_bad_secret_and_legacy_enabled() {
        let mut entries = vec![
            en("empty", ""),
            en("bad", "bad"),
            en("", "cipher"),
            en("full", "full"),
        ];
        entries[2].has_enabled = false;
        entries[2].enabled = false;
        entries[3].enabled = false;
        let mut r = req(0);
        r.url = vec![];
        let (f, t) = run(snap(), r, |e, s| {
            let mut r = ok(e, s);
            match e.kind {
                1 => r.entries = entries.clone(),
                2 => {
                    r.ok = e.data != b"bad";
                    r.data = b"plain".to_vec();
                }
                _ => {}
            }
            r
        });
        assert_eq!(f.snapshot.entries.len(), 2);
        assert!(f.snapshot.entries[0].enabled);
        assert!(!f.snapshot.entries[1].enabled);
        assert_eq!(kinds(&t).iter().filter(|&&k| k == 3).count(), 1);
        assert_eq!(&kinds(&t)[kinds(&t).len() - 2..], &[20, 63]);
    }
    #[test]
    fn save_stops_sealing_after_failure_and_no_store_is_noop() {
        let (_, t) = run(snap(), req(1), |e, s| {
            let mut r = ok(e, s);
            if e.kind == 4 {
                r.ok = false;
            }
            r
        });
        assert_eq!(kinds(&t), vec![4, 63]);
        assert_eq!(code(&t), 22);
        let mut s = snap();
        s.store = false;
        assert_eq!(kinds(&run(s, req(1), ok).1), vec![63]);
    }
    #[test]
    fn queries_sort_and_duplicate_active_preflight() {
        let (_, t) = run(snap(), req(3), ok);
        assert_eq!(t[0].entries[0].id, b"a");
        let mut r = req(4);
        r.url = b"none".to_vec();
        r.hash = b"b".to_vec();
        assert_eq!(run(snap(), r, ok).1[0].entries[0].id, b"b");
        let mut s = snap();
        s.active_url = b"url-b".to_vec();
        let (_, t) = run(s.clone(), req(5), ok);
        assert_eq!(kinds(&t), vec![6, 63]);
        assert_eq!(t[1].entries[0].id, b"b");
        s.entries.push(en("x", "url-b"));
        assert!(Flow::new(s, req(5)).is_err());
    }
    #[test]
    fn record_use_all_matches_two_timestamps_and_no_match_still_saves() {
        let mut s = snap();
        s.entries.push(en("a", "x"));
        let mut r = req(9);
        r.has_info = true;
        let (_, t) = run(s, r, ok);
        assert_eq!(kinds(&t).iter().filter(|&&k| k == 19).count(), 4);
        assert_eq!(kinds(&t).iter().filter(|&&k| k == 40).count(), 4);
        let mut r = req(10);
        r.id = b"missing".to_vec();
        assert_eq!(kinds(&run(snap(), r, ok).1), vec![4, 4, 5, 63]);
    }
    #[test]
    fn hours_save_failure_keeps_assignment_and_bounds() {
        let mut r = req(11);
        r.hours = 720;
        let (_, t) = run(snap(), r, |e, s| {
            let mut r = ok(e, s);
            if e.kind == 12 {
                r.ok = false;
            }
            r
        });
        assert_eq!(kinds(&t), vec![42, 12, 63]);
        assert_eq!(code(&t), 16);
        let mut r = req(11);
        r.hours = 721;
        assert_eq!(code(&run(snap(), r, ok).1), 15);
    }
    #[test]
    fn delete_active_exact_cleanup_and_save_failure_restores_only_registry() {
        let (f, t) = run(snap(), req(12), ok);
        assert!(f.snapshot.active_url.is_empty());
        assert_eq!(
            kinds(&t),
            vec![6, 36, 37, 39, 38, 13, 14, 15, 12, 16, 17, 17, 17, 4, 5, 18, 18, 63]
        );
        let (f, t) = run(snap(), req(12), |e, s| {
            let mut r = ok(e, s);
            if e.kind == 5 {
                r.ok = false;
            }
            r
        });
        assert_eq!(f.snapshot.entries, snap().entries);
        assert!(f.snapshot.active_url.is_empty());
        assert!(kinds(&t).contains(&35));
        assert_eq!(code(&t), 12);
        assert!(!kinds(&t).contains(&18));
    }
    #[test]
    fn delete_merged_failure_keeps_composite_partial_state() {
        let mut s = snap();
        s.active_url = b"__merged__".to_vec();
        let (f, t) = run(s, req(12), |e, s| {
            let mut r = ok(e, s);
            if e.kind == 11 {
                r.ok = false;
                let mut partial = s.clone();
                partial.from_cache = true;
                r.snapshot = Some(partial);
            }
            r
        });
        assert_eq!(f.snapshot.entries, snap().entries);
        assert!(f.snapshot.from_cache);
        assert_eq!(code(&t), 13);
    }
    #[test]
    fn activate_shortcut_and_running_do_no_mutations() {
        let (_, t) = run(snap(), req(13), |e, s| {
            let mut r = ok(e, s);
            if e.kind == 7 {
                r.flag = true;
            }
            r
        });
        assert_eq!(kinds(&t), vec![6, 7, 63]);
        assert_eq!(code(&t), 27);
        let (_, t) = run(snap(), req(13), |e, s| {
            let mut r = ok(e, s);
            if e.kind == 6 {
                r.flag = true;
            }
            r
        });
        assert_eq!(kinds(&t), vec![6, 63]);
        assert_eq!(code(&t), 8);
    }
    #[test]
    fn activate_cache_success_and_double_failure_preserve_original_error() {
        for cache_ok in [false, true] {
            let mut r = req(13);
            r.id = b"b".to_vec();
            let (f, t) = run(snap(), r, |e, s| {
                let mut r = ok(e, s);
                if e.kind == 8 {
                    r.ok = false;
                    r.data = b"original fetch failure".to_vec();
                }
                if e.kind == 9 {
                    r.ok = cache_ok;
                    r.value = 7;
                    r.data = b"cache failed".to_vec();
                }
                r
            });
            assert_eq!(f.snapshot.active_url, b"url-b");
            if cache_ok {
                assert!(f.snapshot.from_cache);
                assert_eq!(code(&t), 26);
                assert_eq!(t.last().unwrap().data, 7i64.to_le_bytes());
            } else {
                assert_eq!(code(&t), 14);
                assert_eq!(t.last().unwrap().data, b"original fetch failure");
                assert!(kinds(&t).contains(&41));
            }
        }
    }
    #[test]
    fn composite_reply_must_supply_readback_without_consumption() {
        let mut r = req(13);
        r.id = b"__merged__".to_vec();
        let mut f = Flow::new(snap(), r).unwrap();
        f.advance(Reply {
            ok: true,
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, 10);
        let before = f.next();
        assert!(f
            .advance(Reply {
                ok: true,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), before);
    }
}
#[cfg(test)]
mod activate_request_regression {
    use super::*;
    #[test]
    fn activate_ignores_caller_quota_info_when_recording_use() {
        let entry = Entry {
            id: b"a".to_vec(),
            url: b"url".to_vec(),
            label: b"A".to_vec(),
            enabled: true,
            has_enabled: true,
        };
        let snapshot = Snapshot {
            entries: vec![entry],
            store: false,
            ..Snapshot::default()
        };
        let request = Request {
            op: 13,
            id: b"a".to_vec(),
            has_info: true,
            node_count: 999,
            ..Request::default()
        };
        let mut flow = Flow::new(snapshot.clone(), request).unwrap();
        let mut times = 0;
        let mut records = 0;
        for _ in 0..40 {
            let e = flow.next();
            if e.kind == 0 {
                break;
            }
            let mut reply = Reply {
                ok: true,
                ..Reply::default()
            };
            if e.kind == 8 {
                let mut actual = snapshot.clone();
                actual.active_url = b"url".to_vec();
                reply.snapshot = Some(actual);
                reply.value = 2;
            }
            if e.kind == 19 {
                times += 1;
                reply.value = 123;
            }
            if e.kind == 40 {
                records += 1;
                assert_eq!(e.data, [0]);
            }
            if e.kind == 63 {
                assert_eq!(e.index, 26);
                assert_eq!(e.data, 2i64.to_le_bytes());
            }
            flow.advance(reply).unwrap();
        }
        assert_eq!(flow.next().kind, 0);
        assert_eq!(times, 1);
        assert_eq!(records, 1);
    }
}
