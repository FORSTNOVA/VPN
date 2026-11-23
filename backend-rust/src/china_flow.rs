//! Complete China Direct workflows over narrow, synchronous facts/effects.
use crate::china_state::{ChinaState, Status};
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub mode: i64,
    pub body: i64,
    pub ranges: Vec<Vec<u8>>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub value: i64,
    pub token: i64,
    pub text: Vec<u8>,
    pub error: Vec<u8>,
    pub names: Vec<Vec<u8>>,
    pub ranges: Vec<Vec<u8>>,
    pub state: ChinaState,
    pub tun: bool,
    pub domestic: bool,
    pub import: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub flag: bool,
    pub name: Vec<u8>,
    pub text: Vec<u8>,
    pub ranges: Vec<Vec<u8>>,
    pub status: Status,
    pub refreshing: bool,
    pub active: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
    Limit,
}
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    child: Option<Box<Flow>>,
    resume: u16,
    lines: Vec<Vec<u8>>,
    i: usize,
    seen: std::collections::BTreeSet<Vec<u8>>,
    seen_bytes: usize,
    range_bytes: usize,
    ranges: Vec<Vec<u8>>,
    mirrors: Vec<Vec<u8>>,
    files: Vec<Vec<u8>>,
    mi: usize,
    fi: usize,
    last: Vec<u8>,
    last_set: bool,
    source: Vec<u8>,
    cached: Option<Status>,
    status: Status,
    time: i64,
    flag: bool,
    tun: bool,
    domestic: bool,
    import: bool,
    result: Effect,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 10 {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            child: None,
            resume: 0,
            lines: vec![],
            i: 0,
            seen: Default::default(),
            seen_bytes: 0,
            range_bytes: 0,
            ranges: vec![],
            mirrors: vec![],
            files: vec![],
            mi: 0,
            fi: 0,
            last: vec![],
            last_set: false,
            source: vec![],
            cached: None,
            status: Status::default(),
            time: 0,
            flag: false,
            tun: false,
            domestic: false,
            import: false,
            result: Effect::default(),
        };
        match f.q.op {
            0 => {
                f.emit(7, 1);
                f.e.a = f.q.body
            }
            1 => f.emit(10, 10),
            2 | 3 => f.emit(5, 20),
            4 => {
                f.ranges = f.q.ranges.clone();
                f.emit(12, 40)
            }
            5 => f.emit(16, 50),
            6 => f.emit(3, 60),
            7 => f.emit(1, 70),
            8 => f.emit(1, 86),
            9 => f.emit(3, 90),
            10 => f.call(3, 0, 0, vec![], 100)?,
            _ => return Err(Error::Invalid),
        };
        f.pump()?;
        Ok(f)
    }
    pub fn next(&self) -> &Effect {
        if let Some(c) = &self.child {
            c.next()
        } else {
            &self.e
        }
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = Effect {
            kind: k,
            ..Default::default()
        };
        self.pc = pc
    }
    fn done(&mut self) {
        self.e = self.result.clone();
        self.e.kind = 63
    }
    fn fail(&mut self, error: Vec<u8>) {
        self.result.b = 1;
        self.result.text = error;
        self.done()
    }
    fn fmt(&mut self, code: i64, name: Vec<u8>, text: Vec<u8>, value: i64, pc: u16) {
        self.emit(31, pc);
        self.e.a = code;
        self.e.b = value;
        self.e.name = name;
        self.e.text = text
    }
    fn command(&mut self, action: i64, flag: bool, pc: u16) {
        self.emit(4, pc);
        self.e.a = action;
        self.e.flag = flag;
        self.e.status = if action == 1 {
            self.status.clone()
        } else {
            Status::default()
        }
    }
    fn call(
        &mut self,
        op: u8,
        mode: i64,
        body: i64,
        ranges: Vec<Vec<u8>>,
        resume: u16,
    ) -> Result<(), Error> {
        self.child = Some(Box::new(Self::new(Request {
            op,
            mode,
            body,
            ranges,
        })?));
        self.resume = resume;
        Ok(())
    }
    fn pump(&mut self) -> Result<(), Error> {
        loop {
            let completed = self.child.as_ref().is_some_and(|c| c.next().kind == 63);
            if !completed {
                break;
            }
            let c = self.child.take().unwrap();
            let result = c.next().clone();
            self.child_result(self.resume, result)?;
        }
        Ok(())
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if r.kind != self.next().kind {
            return Err(Error::Invalid);
        }
        if self.next().kind == 64 {
            return Err(Error::Done);
        }
        if let Some(c) = &mut self.child {
            c.advance(r)?;
            return self.pump();
        }
        if self.e.kind == 63 {
            self.emit(64, 0);
            return Ok(());
        }
        self.reply(r)?;
        self.pump()
    }
    fn parse_next(&mut self) {
        while self.i < self.lines.len() {
            let mut line = self.lines[self.i].clone();
            self.i += 1;
            if line.is_empty() || line[0] == b'#' {
                continue;
            }
            if let Some(cut) = line.iter().position(|v| matches!(v, b' ' | b'\t' | b'#')) {
                line.truncate(cut)
            }
            self.emit(8, 2);
            self.e.name = line;
            return;
        }
        if self.seen.len() < 1000 {
            self.fmt(2, vec![], vec![], self.seen.len() as i64, 3)
        } else {
            self.result.ranges = std::mem::take(&mut self.seen).into_iter().collect();
            self.done()
        }
    }
    fn download_next(&mut self) {
        // Empty source-file plans skip all mirrors without recursive stack growth.
        if self.files.is_empty() {
            self.mi = self.mirrors.len();
        }
        if self.mi >= self.mirrors.len() {
            if !self.last_set {
                self.fmt(4, vec![], vec![], 0, 14)
            } else {
                self.fail(self.last.clone())
            }
            return;
        }
        if self.fi >= self.files.len() {
            if self.ranges.is_empty() {
                self.mi += 1;
                self.fi = 0;
                self.emit(9, 11);
                if self.mi < self.mirrors.len() {
                    self.e.name = self.mirrors[self.mi]
                        .iter()
                        .chain(&self.files[0])
                        .copied()
                        .collect();
                } else if self.last_set {
                    self.fail(self.last.clone());
                } else {
                    self.fmt(4, vec![], vec![], 0, 14);
                }
            } else {
                self.ranges.sort();
                self.result.ranges = std::mem::take(&mut self.ranges);
                self.result.name = self.mirrors[self.mi].clone();
                self.result.flag = true;
                self.done()
            }
            return;
        }
        self.emit(9, 11);
        self.e.name = self.mirrors[self.mi]
            .iter()
            .chain(&self.files[self.fi])
            .copied()
            .collect()
    }
    fn failed_mirror(&mut self, error: Vec<u8>) {
        self.last = error;
        self.last_set = true;
        self.mi += 1;
        self.fi = 0;
        self.ranges.clear();
        self.range_bytes = 0;
        self.download_next()
    }
    fn cache_ready(&mut self, error: Vec<u8>) {
        if self.q.op == 2 {
            if let Some(s) = &self.cached {
                self.result.status.entries = s.entries;
                self.result.a = self.time;
                self.done();
            } else {
                self.fail(error)
            }
            return;
        }
        if self.q.mode == 0 {
            if let Some(s) = &self.cached {
                self.result.status = s.clone()
            } else {
                self.result.status.error = error
            }
            self.done()
        } else if self.cached.is_some() && self.q.mode == 1 {
            self.emit(11, 24);
            self.e.a = self.time
        } else {
            self.emit(32, 25)
        }
    }
    fn ensure_failure(&mut self, error: Vec<u8>, prefix: i64) {
        if let Some(s) = &self.cached {
            self.status = s.clone();
            if prefix == 0 {
                self.result.status = self.status.clone();
                self.done()
            } else {
                self.fmt(prefix, vec![], error, 0, 29)
            }
        } else {
            self.result.status.error = error;
            self.done()
        }
    }
    fn probe_cleanup(&mut self, ok: bool) {
        self.flag = ok;
        self.emit(21, 56)
    }
    fn after_options_supported(&mut self, ok: bool) {
        self.flag = ok;
        self.emit(29, 104);
        self.e.flag = ok
    }
    fn after_import(&mut self) {
        if self.tun && self.domestic && !self.flag {
            self.fmt(7, vec![], vec![], 0, 107)
        } else {
            self.emit(27, 106)
        }
    }
    fn reply(&mut self, r: Reply) -> Result<(), Error> {
        match self.pc {
            1 => {
                self.lines = r.names;
                self.parse_next()
            }
            2 => {
                if r.ok {
                    if !self.seen.contains(&r.text) {
                        let size = self
                            .seen_bytes
                            .checked_add(r.text.len() + 8)
                            .ok_or(Error::Limit)?;
                        if size > 8 << 20 || self.seen.len() >= 100000 {
                            return Err(Error::Limit);
                        }
                        self.seen_bytes = size;
                        self.seen.insert(r.text);
                    }
                    self.parse_next()
                } else {
                    self.fmt(1, self.e.name.clone(), vec![], 0, 3)
                }
            }
            3 => self.fail(r.text),
            10 => {
                self.mirrors = r.names;
                self.files = r.ranges;
                self.download_next()
            }
            11 => {
                if !r.ok {
                    self.failed_mirror(r.error)
                } else {
                    self.source = self.e.name.clone();
                    self.call(0, 0, r.token, vec![], 12)?
                }
            }
            13 => self.failed_mirror(r.text),
            14 => self.fail(r.text),
            260 => self.call(0, 0, r.token, vec![], 27)?,
            20 => {
                if !r.ok {
                    self.cache_ready(r.error)
                } else {
                    self.time = r.value;
                    self.status.updated_at = r.text;
                    self.emit(6, 21)
                }
            }
            21 => {
                if !r.ok {
                    self.cache_ready(r.error)
                } else {
                    self.call(0, 0, r.token, vec![], 22)?
                }
            }
            24 => {
                if r.flag {
                    self.result.status = self.cached.clone().unwrap();
                    self.done()
                } else {
                    self.emit(32, 25)
                }
            }
            25 => {
                if r.flag {
                    if !r.ok {
                        self.ensure_failure(r.error, 5)
                    } else {
                        self.source = r.text;
                        self.call(0, 0, r.token, vec![], 27)?
                    }
                } else {
                    self.call(1, 0, 0, vec![], 26)?
                }
            }
            29 => {
                self.status.error = r.text;
                self.result.status = self.status.clone();
                self.done()
            }
            30 => {
                self.status.updated_at = r.text;
                self.result.status = self.status.clone();
                self.done()
            }
            40 => {
                if !r.ok {
                    self.fail(r.error)
                } else {
                    self.emit(13, 41);
                    self.e.ranges = self.ranges.clone()
                }
            }
            41 => {
                if !r.ok {
                    self.fail(r.error)
                } else {
                    self.emit(14, 42)
                }
            }
            42 => {
                if !r.ok {
                    self.fail(r.error)
                } else {
                    self.done()
                }
            }
            50 => {
                if !r.ok {
                    self.done()
                } else {
                    self.emit(17, 51)
                }
            }
            51 => {
                if !r.ok {
                    self.done()
                } else {
                    self.emit(18, 52)
                }
            }
            52 => {
                if !r.ok {
                    self.probe_cleanup(false)
                } else {
                    self.emit(19, 53)
                }
            }
            53 => {
                if !r.ok {
                    self.probe_cleanup(false)
                } else {
                    self.emit(20, 54)
                }
            }
            54 => {
                if !r.ok {
                    self.emit(24, 55);
                    self.e.name = b"probe".to_vec();
                    self.e.text = r.error
                } else {
                    self.probe_cleanup(true)
                }
            }
            55 => self.probe_cleanup(false),
            56 => self.emit(22, 57),
            57 => {
                self.result.flag = self.flag;
                self.done()
            }
            60 => {
                if r.state.probed {
                    self.result.flag = r.state.rule_ok;
                    self.done()
                } else {
                    self.command(3, true, 61)
                }
            }
            61 => self.call(5, 0, 0, vec![], 62)?,
            63 => {
                self.result.flag = self.flag;
                self.done()
            }
            70 => self.emit(3, 71),
            71 => {
                if r.state.fetching {
                    self.flag = false;
                    self.emit(2, 73)
                } else {
                    self.command(2, true, 72)
                }
            }
            72 => {
                self.flag = true;
                self.emit(2, 73)
            }
            73 => {
                if self.flag {
                    self.emit(23, 74);
                    self.e.a = self.q.mode
                } else {
                    self.done()
                }
            }
            74 => {
                self.result.flag = true;
                self.done()
            }
            86 => self.emit(3, 87),
            87 => self.emit(2, 88),
            88 => self.call(3, self.q.mode, 0, vec![], 80)?,
            81 => self.command(2, false, 82),
            82 => self.command(1, false, 83),
            83 => self.emit(2, 84),
            84 => {
                if !self.status.source.is_empty() || !self.status.available {
                    self.emit(24, 85);
                    self.e.name = if self.status.source.is_empty() {
                        b"unavailable".to_vec()
                    } else {
                        b"source".to_vec()
                    };
                    self.e.status = self.status.clone()
                } else {
                    self.done()
                }
            }
            85 => self.done(),
            90 => {
                self.result.status = r.state.status;
                self.result.refreshing = r.state.fetching;
                self.result.active = r.state.active;
                self.done()
            }
            101 => self.emit(25, 102),
            102 => {
                self.tun = r.tun;
                self.domestic = r.domestic;
                self.import = r.import;
                if self.status.available {
                    self.call(6, 0, 0, vec![], 103)?
                } else {
                    self.after_options_supported(false)
                }
            }
            104 => {
                if self.import {
                    self.emit(26, 105)
                } else {
                    self.after_import()
                }
            }
            105 => {
                if !r.ok {
                    self.fail(r.error)
                } else {
                    self.tun = r.tun;
                    self.domestic = r.domestic;
                    self.after_import()
                }
            }
            106 => {
                if !r.ok {
                    self.fail(r.error)
                } else if r.tun {
                    self.emit(28, 108)
                } else {
                    self.done()
                }
            }
            107 => self.fail(r.text),
            108 => self.done(),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
    fn child_result(&mut self, pc: u16, r: Effect) -> Result<(), Error> {
        match pc {
            12 => {
                if r.b != 0 {
                    self.fmt(3, self.source.clone(), r.text, 0, 13)
                } else {
                    let add = r
                        .ranges
                        .iter()
                        .try_fold(0usize, |n, v| n.checked_add(v.len() + 8))
                        .ok_or(Error::Limit)?;
                    let size = self.range_bytes.checked_add(add).ok_or(Error::Limit)?;
                    if size > 8 << 20 || self.ranges.len() + r.ranges.len() > 100000 {
                        return Err(Error::Limit);
                    }
                    self.range_bytes = size;
                    self.ranges.extend(r.ranges);
                    self.fi += 1;
                    self.download_next()
                }
            }
            22 => {
                if r.b == 0 {
                    self.status.available = true;
                    self.status.entries = r.ranges.len() as i64;
                    self.cached = Some(self.status.clone())
                }
                self.cache_ready(r.text)
            }
            26 => {
                if r.b != 0 {
                    self.ensure_failure(r.text, 5)
                } else {
                    self.source = r.name; /* combined mirror body must be reparsed globally */
                    self.emit(33, 260);
                    self.e.ranges = r.ranges;
                }
            }
            27 => {
                if r.b != 0 {
                    self.ensure_failure(r.text, 0)
                } else {
                    self.status = Status {
                        available: true,
                        entries: r.ranges.len() as i64,
                        source: self.source.clone(),
                        ..Default::default()
                    };
                    self.call(4, 0, 0, r.ranges, 28)?
                }
            }
            28 => {
                if r.b != 0 {
                    self.ensure_failure(r.text, 6)
                } else {
                    self.emit(15, 30)
                }
            }
            62 => {
                self.flag = r.flag;
                self.command(4, r.flag, 63)
            }
            80 => {
                self.status = r.status;
                self.result.status = self.status.clone();
                self.emit(1, 81)
            }
            100 => {
                self.status = r.status;
                self.command(1, false, 101)
            }
            103 => self.after_options_supported(r.flag),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn run(q: Request, mut reply: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(q).unwrap();
        let mut trace = vec![];
        for _ in 0..100000 {
            let e = f.next().clone();
            if e.kind == 63 {
                return (e, trace);
            }
            trace.push(e.clone());
            let mut r = reply(&e);
            r.kind = e.kind;
            f.advance(r).unwrap();
        }
        panic!("workflow did not terminate")
    }
    fn ok() -> Reply {
        Reply {
            ok: true,
            ..Default::default()
        }
    }
    fn lines() -> Vec<Vec<u8>> {
        (0..1000)
            .map(|i| format!("range-{i:04}").into_bytes())
            .collect()
    }
    fn fixture(e: &Effect) -> Reply {
        match e.kind {
            7 => Reply {
                names: lines(),
                ..ok()
            },
            8 => Reply {
                text: e.name.clone(),
                ..ok()
            },
            10 => Reply {
                names: vec![b"mirror/".to_vec()],
                ranges: vec![b"v4".to_vec(), b"v6".to_vec()],
                ..ok()
            },
            9 | 6 | 33 => Reply { token: 1, ..ok() },
            5 => Reply {
                value: 9,
                text: b"oldtime".to_vec(),
                ..ok()
            },
            15 => Reply {
                text: b"newtime".to_vec(),
                ..ok()
            },
            31 => Reply {
                text: format!("format:{}:{}", e.a, e.b).into_bytes(),
                ..ok()
            },
            _ => ok(),
        }
    }
    #[test]
    fn parse_deduplicates_sorts_skips_and_cuts_comments() {
        let (r, t) = run(Request::default(), |e| {
            if e.kind == 7 {
                let mut n = lines();
                n.reverse();
                n.extend([
                    b"".to_vec(),
                    b"#comment".to_vec(),
                    b"range-0001 # tail".to_vec(),
                ]);
                Reply { names: n, ..ok() }
            } else {
                fixture(e)
            }
        });
        assert_eq!(r.b, 0);
        assert_eq!(r.ranges, lines());
        assert_eq!(t.iter().filter(|e| e.kind == 8).count(), 1001);
    }
    #[test]
    fn parse_invalid_stops_and_minimum_is_after_dedup() {
        for invalid in [false, true] {
            let (r, t) = run(Request::default(), |e| {
                if e.kind == 7 {
                    Reply {
                        names: vec![b"one".to_vec(); 1000],
                        ..ok()
                    }
                } else if e.kind == 8 && invalid {
                    Reply::default()
                } else {
                    fixture(e)
                }
            });
            assert_eq!(r.b, 1);
            let fmt = t.last().unwrap();
            assert_eq!(fmt.kind, 31);
            assert_eq!(fmt.a, if invalid { 1 } else { 2 });
        }
    }
    #[test]
    fn download_keeps_cross_file_duplicates_and_mirror_order() {
        let (r, t) = run(
            Request {
                op: 1,
                ..Default::default()
            },
            fixture,
        );
        assert_eq!(r.ranges.len(), 2000);
        assert_eq!(r.ranges[0], r.ranges[1]);
        assert_eq!(
            t.iter()
                .filter(|e| e.kind == 9)
                .map(|e| e.name.clone())
                .collect::<Vec<_>>(),
            vec![b"mirror/v4".to_vec(), b"mirror/v6".to_vec()]
        );
    }
    #[test]
    fn download_failed_part_discards_whole_mirror() {
        let (r, t) = run(
            Request {
                op: 1,
                ..Default::default()
            },
            |e| match e.kind {
                10 => Reply {
                    names: vec![b"a/".to_vec(), b"b/".to_vec()],
                    ranges: vec![b"4".to_vec(), b"6".to_vec()],
                    ..ok()
                },
                9 if e.name == b"a/6" => Reply {
                    error: b"failed".to_vec(),
                    ..Default::default()
                },
                _ => fixture(e),
            },
        );
        assert_eq!(r.name, b"b/");
        assert_eq!(r.ranges.len(), 2000);
        assert_eq!(t.iter().filter(|e| e.kind == 9).count(), 4);
    }
    #[test]
    fn ensure_cached_never_downloads_and_overdue_boundary_is_fact() {
        for mode in [0, 1] {
            let (r, t) = run(
                Request {
                    op: 3,
                    mode,
                    ..Default::default()
                },
                |e| {
                    if e.kind == 11 {
                        Reply { flag: true, ..ok() }
                    } else {
                        fixture(e)
                    }
                },
            );
            assert!(r.status.available);
            assert_eq!(r.status.updated_at, b"oldtime");
            assert!(!t.iter().any(|e| matches!(e.kind, 9 | 10 | 32)));
        }
    }
    #[test]
    fn ensure_fresh_reparses_combined_and_writes_then_clock() {
        let (r, t) = run(
            Request {
                op: 3,
                mode: 2,
                ..Default::default()
            },
            fixture,
        );
        assert_eq!(r.status.entries, 1000);
        assert_eq!(r.status.source, b"mirror/");
        assert_eq!(r.status.updated_at, b"newtime");
        let kinds = t.iter().map(|e| e.kind).collect::<Vec<_>>();
        assert!(kinds.ends_with(&[12, 13, 14, 15]));
        assert!(kinds.contains(&33));
    }
    #[test]
    fn ensure_fetch_failure_preserves_cache_with_error() {
        let (r, _) = run(
            Request {
                op: 3,
                mode: 2,
                ..Default::default()
            },
            |e| {
                if e.kind == 32 {
                    Reply {
                        flag: true,
                        error: b"offline".to_vec(),
                        ..Default::default()
                    }
                } else {
                    fixture(e)
                }
            },
        );
        assert!(r.status.available);
        assert_eq!(r.status.updated_at, b"oldtime");
        assert_eq!(r.status.error, b"format:5:0");
    }
    #[test]
    fn ensure_bad_fresh_parse_keeps_old_without_new_error() {
        let (r, t) = run(
            Request {
                op: 3,
                mode: 2,
                ..Default::default()
            },
            |e| {
                if e.kind == 32 {
                    Reply {
                        flag: true,
                        token: 2,
                        ..ok()
                    }
                } else if e.kind == 7 && e.a == 2 {
                    Reply {
                        names: vec![],
                        ..ok()
                    }
                } else {
                    fixture(e)
                }
            },
        );
        assert!(r.status.available);
        assert!(r.status.error.is_empty());
        assert!(!t.iter().any(|e| e.kind == 12));
    }
    #[test]
    fn write_failure_has_no_cleanup() {
        for fail in [12, 13, 14] {
            let (r, t) = run(
                Request {
                    op: 4,
                    ranges: lines(),
                    ..Default::default()
                },
                |e| {
                    if e.kind == fail {
                        Reply {
                            error: b"io".to_vec(),
                            ..Default::default()
                        }
                    } else {
                        ok()
                    }
                },
            );
            assert_eq!(r.b, 1);
            assert_eq!(t.last().unwrap().kind, fail);
        }
    }
    #[test]
    fn probe_cleanup_begins_after_successful_mkdir_and_is_ordered() {
        for fail in [0, 16, 17, 18, 19, 20] {
            let (r, t) = run(
                Request {
                    op: 5,
                    ..Default::default()
                },
                |e| {
                    if e.kind == fail {
                        Reply {
                            error: b"probe".to_vec(),
                            ..Default::default()
                        }
                    } else {
                        ok()
                    }
                },
            );
            assert_eq!(r.flag, fail == 0);
            let k = t.iter().map(|e| e.kind).collect::<Vec<_>>();
            if matches!(fail, 16 | 17) {
                assert!(!k.contains(&21))
            } else {
                assert!(k.ends_with(&[21, 22]))
            }
        }
    }
    #[test]
    fn supported_publishes_probed_before_probe_and_preserves_failure() {
        let (r, t) = run(
            Request {
                op: 6,
                ..Default::default()
            },
            |e| if e.kind == 16 { Reply::default() } else { ok() },
        );
        assert!(!r.flag);
        assert_eq!(
            t.iter().map(|e| (e.kind, e.a, e.flag)).collect::<Vec<_>>(),
            vec![(3, 0, false), (4, 3, true), (16, 0, false), (4, 4, false)]
        );
    }
    #[test]
    fn refresh_unlocks_before_spawn_and_does_not_spawn_twice() {
        for busy in [false, true] {
            let (r, t) = run(
                Request {
                    op: 7,
                    mode: 2,
                    ..Default::default()
                },
                |_| Reply {
                    state: ChinaState {
                        fetching: busy,
                        ..Default::default()
                    },
                    ..ok()
                },
            );
            assert_eq!(r.flag, !busy);
            let k = t.iter().map(|e| e.kind).collect::<Vec<_>>();
            assert_eq!(
                k,
                if busy {
                    vec![1, 3, 2]
                } else {
                    vec![1, 3, 4, 2, 23]
                }
            );
        }
    }
    #[test]
    fn worker_commits_only_fetching_and_status_then_logs_after_unlock() {
        let (_, t) = run(
            Request {
                op: 8,
                mode: 0,
                ..Default::default()
            },
            fixture,
        );
        let tail = t
            .iter()
            .filter(|e| matches!(e.kind, 1 | 2 | 4 | 24))
            .map(|e| (e.kind, e.a))
            .collect::<Vec<_>>();
        assert_eq!(tail, vec![(1, 0), (2, 0), (1, 0), (4, 2), (4, 1), (2, 0)]);
    }
    #[test]
    fn options_missing_domestic_rule_errors_before_chain() {
        let (r, t) = run(
            Request {
                op: 10,
                ..Default::default()
            },
            |e| match e.kind {
                25 => Reply {
                    tun: true,
                    domestic: true,
                    ..ok()
                },
                16 => Reply::default(),
                _ => fixture(e),
            },
        );
        assert_eq!(r.b, 1);
        assert_eq!(r.text, b"format:7:0");
        assert!(!t.iter().any(|e| e.kind == 27));
    }
    #[test]
    fn rejected_reply_does_not_consume_or_change_effect() {
        let mut f = Flow::new(Request {
            op: 5,
            ..Default::default()
        })
        .unwrap();
        let old = f.next().clone();
        assert_eq!(f.advance(Reply { kind: 99, ..ok() }), Err(Error::Invalid));
        assert_eq!(f.next(), &old);
    }
    #[test]
    fn hundred_thousand_mirrors_with_empty_files_do_not_recurse() {
        let (r, t) = run(
            Request {
                op: 1,
                ..Default::default()
            },
            |e| {
                if e.kind == 10 {
                    Reply {
                        names: vec![b"mirror".to_vec(); 100000],
                        ..ok()
                    }
                } else {
                    fixture(e)
                }
            },
        );
        assert_eq!(r.b, 1);
        assert_eq!(t.len(), 2);
    }
    #[test]
    fn empty_error_message_is_still_last_error() {
        let (r, t) = run(
            Request {
                op: 1,
                ..Default::default()
            },
            |e| {
                if e.kind == 9 {
                    Reply::default()
                } else {
                    fixture(e)
                }
            },
        );
        assert_eq!(r.b, 1);
        assert!(r.text.is_empty());
        assert!(!t.iter().any(|e| e.kind == 31));
    }
    #[test]
    fn read_preserves_original_time_token_but_only_count_status() {
        let (r, _) = run(
            Request {
                op: 2,
                ..Default::default()
            },
            fixture,
        );
        assert_eq!(r.a, 9);
        assert_eq!(
            r.status,
            Status {
                entries: 1000,
                ..Default::default()
            }
        );
        let (r, _) = run(
            Request {
                op: 2,
                ..Default::default()
            },
            |e| {
                if e.kind == 6 {
                    Reply {
                        error: b"read failure".to_vec(),
                        ..Default::default()
                    }
                } else {
                    fixture(e)
                }
            },
        );
        assert_eq!(r.b, 1);
        assert_eq!(r.a, 0);
        assert_eq!(r.status, Status::default());
    }
    #[test]
    fn accumulated_normalized_output_has_a_budget() {
        let mut f = Flow::new(Request::default()).unwrap();
        f.advance(Reply {
            kind: 7,
            names: vec![b"one".to_vec(), b"two".to_vec()],
            ..ok()
        })
        .unwrap();
        f.advance(Reply {
            kind: 8,
            text: vec![b'a'; (4 << 20) - 8],
            ..ok()
        })
        .unwrap();
        assert_eq!(
            f.advance(Reply {
                kind: 8,
                text: vec![b'b'; (4 << 20) + 1],
                ..ok()
            }),
            Err(Error::Limit)
        );
    }
}
