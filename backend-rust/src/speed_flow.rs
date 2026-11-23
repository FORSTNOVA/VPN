//! Speed-test orchestration. Real concurrent batches, transport and atomics are resources outside this planner.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Speed {
    pub mega_bytes: i64,
    pub down_mbps: f64,
    pub up_mbps: f64,
    pub down_bytes: i64,
    pub up_bytes: i64,
    pub down_ms: i64,
    pub up_ms: i64,
    pub error: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    pub op: u8,
    pub mega_bytes: i64,
    pub allowance: i64,
    pub budget: i64,
    pub remaining: i64,
    pub length: i64,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub text: Vec<u8>,
    pub bytes: i64,
    pub started: i64,
    pub elapsed: i64,
    pub rate: f64,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Effect {
    pub kind: u8,
    pub upload: bool,
    pub count: i64,
    pub bytes: i64,
    pub budget: i64,
    pub status: i64,
    pub text: Vec<u8>,
    pub node: Vec<u8>,
    pub temporary: bool,
    pub speed: Speed,
    pub eof: bool,
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
    result: Effect,
    started: i64,
    read_error: bool,
    read_n: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 5 {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            child: None,
            resume: 0,
            result: Effect::default(),
            started: 0,
            read_error: false,
            read_n: 0,
        };
        match f.q.op {
            0 => {
                let mb = if f.q.mega_bytes == 0 {
                    30
                } else {
                    f.q.mega_bytes
                }
                .clamp(10, 100);
                f.q.mega_bytes = mb;
                f.emit(1, 1)
            }
            1 | 2 => {
                f.emit(5, 20);
                f.e.upload = f.q.op == 2;
                f.e.bytes = f.q.allowance;
                f.e.budget = f.q.budget
            }
            3 | 4 => {
                f.emit(11, 40);
                f.e.upload = f.q.op == 4;
                f.e.bytes = f.q.allowance
            }
            5 => {
                if f.q.remaining <= 0 {
                    f.result.eof = true;
                    f.done()
                } else {
                    f.read_n = f.q.length.min(f.q.remaining);
                    f.emit(21, 60);
                    f.e.bytes = f.read_n
                }
            }
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
        self.e = self.result.clone();
        self.e.kind = 63
    }
    fn measure(&mut self, upload: bool, resume: u16) -> Result<(), Error> {
        self.child = Some(Box::new(Self::new(Request {
            op: if upload { 2 } else { 1 },
            allowance: if upload {
                (self.q.mega_bytes / 3) << 20
            } else {
                self.q.mega_bytes << 20
            },
            budget: 10_000_000_000,
            ..Default::default()
        })?));
        self.resume = resume;
        Ok(())
    }
    fn after_response(&mut self) {
        if self.result.temporary {
            self.emit(19, 14)
        } else {
            self.emit(20, 15)
        }
    }
    fn response(&mut self) {
        self.emit(18, 13);
        self.e.status = self.result.status;
        if self.result.status == 200 {
            self.e.node = self.result.node.clone();
            self.e.temporary = self.result.temporary;
            self.e.speed = self.result.speed.clone()
        } else {
            self.e.text = self.result.text.clone()
        }
    }
    fn format(&mut self, code: i64, pc: u16) {
        self.emit(25, pc);
        self.e.count = code
    }
    fn after_read(&mut self) {
        if self.read_error {
            self.emit(16, 46)
        } else {
            self.emit(15, 45)
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
                let out = self.child.take().unwrap().next().clone();
                self.child_result(out)?
            }
            return Ok(());
        }
        if r.kind == 63 {
            self.emit(64, 0);
            return Ok(());
        }
        match self.pc {
            1 => self.emit(2, 2),
            2 => {
                if !r.ok {
                    self.result.status = 502;
                    self.result.text = r.text;
                    self.response()
                } else {
                    self.result.temporary = r.flag;
                    self.emit(3, 3)
                }
            }
            3 => {
                self.result.node = r.text;
                if r.flag
                    || self.result.node.is_empty()
                    || self.result.node.eq_ignore_ascii_case(b"REJECT")
                {
                    self.result.status = 409;
                    self.format(3, 4)
                } else {
                    self.emit(4, 5)
                }
            }
            4 => {
                self.result.text = r.text;
                self.response()
            }
            5 => {
                self.result.speed.mega_bytes = self.q.mega_bytes;
                self.measure(false, 6)?
            }
            7 => {
                self.result.speed.down_mbps = r.rate;
                self.measure(true, 8)?
            }
            9 => {
                self.result.speed.up_mbps = r.rate;
                self.result.status = 200;
                self.response()
            }
            13 => self.after_response(),
            14 => self.emit(20, 15),
            15 => self.done(),
            20 => {
                self.emit(6, 21);
                self.e.upload = self.q.op == 2;
                self.e.count = if self.e.upload { 2 } else { 4 };
                self.e.bytes = if self.e.upload {
                    self.q.allowance / 2
                } else {
                    self.q.allowance
                };
                self.e.budget = self.q.budget
            }
            21 => self.emit(7, 22),
            22 => self.emit(8, 23),
            23 => {
                if r.started == 0 || r.bytes == 0 {
                    self.format(if self.q.op == 2 { 2 } else { 1 }, 26)
                } else {
                    self.started = r.started;
                    self.emit(15, 24)
                }
            }
            24 => {
                self.result.bytes = r.bytes;
                self.emit(9, 25);
                self.e.bytes = self.started
            }
            25 => {
                self.result.budget = r.elapsed;
                self.emit(10, 27)
            }
            26 => {
                self.result.text = r.text;
                self.result.status = 1;
                self.emit(10, 27)
            }
            27 => self.done(),
            40 => {
                if !r.ok {
                    self.done()
                } else {
                    self.emit(12, 41)
                }
            }
            41 => {
                if !r.ok {
                    self.done()
                } else if self.q.op == 4 {
                    self.emit(17, 47)
                } else {
                    self.emit(13, 42)
                }
            }
            42 => {
                self.read_error = !r.ok;
                self.read_n = r.bytes;
                if r.bytes > 0 {
                    self.emit(14, 43);
                    self.e.bytes = r.bytes
                } else {
                    self.after_read()
                }
            }
            43 => self.after_read(),
            45 => {
                if r.bytes >= self.q.allowance {
                    self.emit(16, 46)
                } else {
                    self.emit(13, 42)
                }
            }
            46 => self.done(),
            47 => self.emit(16, 46),
            60 => {
                self.emit(22, 61);
                self.e.bytes = self.q.remaining.wrapping_sub(self.read_n)
            }
            61 => {
                self.emit(23, 62);
                self.e.bytes = self.read_n
            }
            62 => {
                self.result.bytes = self.read_n;
                self.done()
            }
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
    fn child_result(&mut self, r: Effect) -> Result<(), Error> {
        match self.resume {
            6 => {
                if r.status != 0 {
                    self.result.speed.error = r.text;
                    self.measure(true, 8)?
                } else {
                    self.result.speed.down_bytes = r.bytes;
                    self.result.speed.down_ms = r.budget / 1_000_000;
                    self.emit(24, 7);
                    self.e.bytes = r.bytes;
                    self.e.budget = r.budget
                }
            }
            8 => {
                if r.status != 0 {
                    if self.result.speed.error.is_empty() {
                        self.result.speed.error = r.text
                    }
                    self.result.status = 200;
                    self.response()
                } else {
                    self.result.speed.up_bytes = r.bytes;
                    self.result.speed.up_ms = r.budget / 1_000_000;
                    self.emit(24, 9);
                    self.e.bytes = r.bytes;
                    self.e.budget = r.budget
                }
            }
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
        let mut t = vec![];
        for _ in 0..10000 {
            let e = f.next().clone();
            if e.kind == 63 {
                return (e, t);
            }
            t.push(e.clone());
            let mut r = fx(&e);
            r.kind = e.kind;
            f.advance(r).unwrap()
        }
        panic!("nonterminating")
    }
    fn facts(e: &Effect) -> Reply {
        match e.kind {
            3 => Reply {
                text: b"node".to_vec(),
                ..ok()
            },
            8 => Reply {
                started: 123,
                bytes: 100,
                ..ok()
            },
            15 => Reply { bytes: 100, ..ok() },
            9 => Reply {
                elapsed: 1_000_000_000,
                ..ok()
            },
            24 => Reply {
                rate: crate::throughput_mbps(e.bytes, e.budget),
                ..ok()
            },
            25 => Reply {
                text: format!("error{}", e.count).into_bytes(),
                ..ok()
            },
            _ => ok(),
        }
    }
    #[test]
    fn handler_clamps_and_runs_download_then_upload() {
        for (input, expected) in [(0, 30), (-1, 10), (1, 10), (99, 99), (101, 100)] {
            let (r, t) = run(
                Request {
                    op: 0,
                    mega_bytes: input,
                    ..Default::default()
                },
                facts,
            );
            assert_eq!(r.status, 200);
            assert_eq!(r.speed.mega_bytes, expected);
            let batches = t.iter().filter(|e| e.kind == 6).collect::<Vec<_>>();
            assert_eq!(batches.len(), 2);
            assert_eq!(batches[0].count, 4);
            assert_eq!(batches[0].bytes, expected << 20);
            assert_eq!(batches[1].count, 2);
            assert_eq!(batches[1].bytes, ((expected / 3) << 20) / 2);
            assert_eq!(r.speed.down_ms, 1000);
        }
    }
    #[test]
    fn download_failure_still_uploads_and_first_nonempty_error_wins() {
        let mut totals = 0;
        let (r, t) = run(Request::default(), |e| {
            if e.kind == 8 {
                totals += 1;
                Reply::default()
            } else {
                facts(e)
            }
        });
        assert_eq!(totals, 2);
        assert_eq!(r.status, 200);
        assert_eq!(r.speed.error, b"error1");
        assert_eq!(t.iter().filter(|e| e.kind == 10).count(), 2);
    }
    #[test]
    fn empty_first_error_text_allows_second_error_original_semantics() {
        let (r, _) = run(Request::default(), |e| {
            if e.kind == 8 {
                Reply::default()
            } else if e.kind == 25 && e.count == 1 {
                ok()
            } else {
                facts(e)
            }
        });
        assert_eq!(r.speed.error, b"error2");
    }
    #[test]
    fn handler_response_precedes_temporary_stop_and_unlock() {
        let (_, t) = run(Request::default(), |e| {
            if e.kind == 2 {
                Reply { flag: true, ..ok() }
            } else {
                facts(e)
            }
        });
        assert!(t
            .iter()
            .map(|e| e.kind)
            .collect::<Vec<_>>()
            .ends_with(&[18, 19, 20]));
    }
    #[test]
    fn ensure_failure_never_stops_and_response_has_no_metadata() {
        let (r, t) = run(Request::default(), |e| {
            if e.kind == 2 {
                Reply {
                    text: b"startfailed".to_vec(),
                    ..Default::default()
                }
            } else {
                facts(e)
            }
        });
        assert_eq!(r.status, 502);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![1, 2, 18, 20]
        );
        let response = &t[2];
        assert!(response.node.is_empty());
        assert!(!response.temporary);
    }
    #[test]
    fn selection_failure_preserves_returned_node_and_temporary() {
        let (r, t) = run(Request::default(), |e| match e.kind {
            2 => Reply { flag: true, ..ok() },
            3 => Reply {
                text: b"rEjEcT".to_vec(),
                ..ok()
            },
            _ => facts(e),
        });
        assert_eq!(r.status, 409);
        assert_eq!(r.node, b"rEjEcT");
        assert!(r.temporary);
        assert!(!t.iter().any(|e| e.kind == 5));
        assert!(t
            .iter()
            .map(|e| e.kind)
            .collect::<Vec<_>>()
            .ends_with(&[18, 19, 20]));
    }
    #[test]
    fn measured_totals_second_load_precedes_elapsed_and_cancel() {
        let (r, t) = run(
            Request {
                op: 1,
                allowance: 123,
                budget: 456,
                ..Default::default()
            },
            facts,
        );
        assert_eq!(r.bytes, 100);
        assert_eq!(r.budget, 1_000_000_000);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![5, 6, 7, 8, 15, 9, 10]
        );
    }
    #[test]
    fn measure_zero_begin_or_total_returns_zero_and_cancels() {
        for zero_begin in [true, false] {
            let (r, t) = run(
                Request {
                    op: 2,
                    ..Default::default()
                },
                |e| {
                    if e.kind == 8 {
                        Reply {
                            started: if zero_begin { 0 } else { 9 },
                            bytes: if zero_begin { 9 } else { 0 },
                            ..ok()
                        }
                    } else {
                        facts(e)
                    }
                },
            );
            assert_eq!(r.status, 1);
            assert_eq!(r.bytes, 0);
            assert_eq!(r.budget, 0);
            assert!(!t.iter().any(|e| e.kind == 9));
            assert_eq!(t.last().unwrap().kind, 10);
        }
    }
    #[test]
    fn download_read_positive_eof_counts_before_closing_without_load() {
        let (_, t) = run(
            Request {
                op: 3,
                allowance: 1,
                ..Default::default()
            },
            |e| {
                if e.kind == 13 {
                    Reply {
                        bytes: 65536,
                        ..Default::default()
                    }
                } else {
                    facts(e)
                }
            },
        );
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![11, 12, 13, 14, 16]
        );
        assert_eq!(t[3].bytes, 65536);
    }
    #[test]
    fn download_overshoot_and_zero_read_follow_shared_load() {
        let mut reads = 0;
        let (_, t) = run(
            Request {
                op: 3,
                allowance: 20,
                ..Default::default()
            },
            |e| match e.kind {
                13 => {
                    reads += 1;
                    Reply {
                        bytes: if reads == 1 { 0 } else { 65536 },
                        ..ok()
                    }
                }
                15 => Reply {
                    bytes: if reads == 1 { 0 } else { 65536 },
                    ..ok()
                },
                _ => facts(e),
            },
        );
        assert_eq!(reads, 2);
        assert_eq!(t.iter().filter(|e| e.kind == 14).count(), 1);
        assert_eq!(t.last().unwrap().kind, 16);
    }
    #[test]
    fn stream_request_errors_are_swallowed_without_body_close() {
        for op in [3, 4] {
            for fail in [11, 12] {
                let (r, t) = run(
                    Request {
                        op,
                        ..Default::default()
                    },
                    |e| {
                        if e.kind == fail {
                            Reply {
                                text: b"ignored".to_vec(),
                                ..Default::default()
                            }
                        } else {
                            facts(e)
                        }
                    },
                );
                assert_eq!(r.status, 0);
                assert!(r.text.is_empty());
                assert!(!t.iter().any(|e| e.kind == 16));
            }
        }
    }
    #[test]
    fn upload_drain_then_close_ignores_status_and_drain_error() {
        let (r, t) = run(
            Request {
                op: 4,
                allowance: 17,
                ..Default::default()
            },
            |e| {
                if e.kind == 17 {
                    Reply::default()
                } else {
                    facts(e)
                }
            },
        );
        assert_eq!(r.status, 0);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            vec![11, 12, 17, 16]
        );
    }
    #[test]
    fn zero_reader_eof_and_zero_byte_read_are_distinct() {
        for remaining in [-1, 0] {
            let (r, t) = run(
                Request {
                    op: 5,
                    remaining,
                    length: 4,
                    ..Default::default()
                },
                facts,
            );
            assert!(r.eof);
            assert!(t.is_empty());
        }
        let (r, t) = run(
            Request {
                op: 5,
                remaining: 4,
                length: 0,
                ..Default::default()
            },
            facts,
        );
        assert!(!r.eof);
        assert_eq!(r.bytes, 0);
        assert_eq!(
            t.iter().map(|e| (e.kind, e.bytes)).collect::<Vec<_>>(),
            vec![(21, 0), (22, 4), (23, 0)]
        );
    }
    #[test]
    fn zero_reader_minimum_and_signed_cursor_wrap() {
        let (r, t) = run(
            Request {
                op: 5,
                remaining: 7,
                length: 100,
                ..Default::default()
            },
            facts,
        );
        assert_eq!(r.bytes, 7);
        assert_eq!(t[1].bytes, 0);
        let (_, t) = run(
            Request {
                op: 5,
                remaining: i64::MAX,
                length: -1,
                ..Default::default()
            },
            facts,
        );
        assert_eq!(t[1].bytes, i64::MIN);
    }
    #[test]
    fn wrong_reply_never_consumes_effect() {
        let mut f = Flow::new(Request {
            op: 3,
            ..Default::default()
        })
        .unwrap();
        let before = f.next().clone();
        assert_eq!(f.advance(Reply { kind: 12, ..ok() }), Err(Error::Invalid));
        assert_eq!(f.next(), &before);
    }
}
