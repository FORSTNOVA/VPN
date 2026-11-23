//! Adaptive DNS and independent health confirmation. All resources remain in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub index: i64,
    pub from: i64,
    pub to: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub other: bool,
    pub value: i64,
    pub index: i64,
    pub token: i64,
    pub event: u8,
    pub elapsed: i64,
    pub body_len: i64,
    pub response: bool,
    pub id_match: bool,
    pub question_match: bool,
    pub questions: i64,
    pub answer_questions: i64,
    pub rcode: i64,
    pub state: Vec<u8>,
    pub bits: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub index: i64,
    pub value: i64,
    pub duration: i64,
    pub token: i64,
    pub flag: bool,
    pub error: u8,
    pub result_bool: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}

fn valid_answer(r: &Reply) -> bool {
    r.body_len <= 65535
        && r.bits == 7
        && r.response
        && r.id_match
        && r.questions == 1
        && r.answer_questions == 1
        && r.question_match
        && r.rcode != 2
        && r.rcode != 5
}
fn should_switch(active_healthy: bool, backup_healthy: bool, active: i64, backup: i64) -> bool {
    backup_healthy && (!active_healthy || (active > 500_000_000 && backup < active))
}
fn path_passed(state: &[u8]) -> bool {
    !state.is_empty() && state != b"error" && state != b"skipped"
}

// ABI ports exactly follow adaptive_service_go.go. No resource is retained here.
const PACK: u8 = 1;
const REQUEST: u8 = 2;
const DO: u8 = 3;
const BODY_CLOSE: u8 = 4;
const FAILURE: u8 = 5;
const BODY_READ: u8 = 6;
const UNPACK: u8 = 7;
const ANSWER_ACCEPT: u8 = 8;
const CAS: u8 = 9;
const SWITCH_LOG: u8 = 10;
const CONTEXT: u8 = 11;
const CONTEXT_CANCEL: u8 = 12;
const COMPLETION_NEW: u8 = 13;
const PREFERRED: u8 = 14;
const RESOLVE_START: u8 = 15;
const TIMER_NEW: u8 = 16;
const TIMER_STOP: u8 = 17;
const HEAD_WAIT: u8 = 18;
const RESOLVE_WAIT: u8 = 19;
const PROBE_NAME: u8 = 20;
const CHECK_START: u8 = 21;
const CHECK_WAIT: u8 = 22;
const CUSTOM_PRESENT: u8 = 23;
const CUSTOM_CONFIRM: u8 = 24;
const BLOCKED: u8 = 25;
const RUNNING: u8 = 26;
const PROBE_CONFIG: u8 = 27;
const SITE: u8 = 28;
const NOTE_PRESENT: u8 = 29;
const ANOMALY_LOG: u8 = 30;
const NOTE_ANOMALY: u8 = 31;
const ANSWER_FAILURE: u8 = 32;
const ANSWER_WRITE: u8 = 33;
const TICKER_NEW: u8 = 34;
const TICKER_STOP: u8 = 35;
const MONITOR_WAIT: u8 = 36;
const CHECK_CHILD: u8 = 37;
const CLOCK_START: u8 = 38;
const PROBE_QUESTION: u8 = 39;
const EXCHANGE_CHILD: u8 = 40;
const ELAPSED: u8 = 41;
const REJECT: u8 = 42;
const REJECT_LOG: u8 = 43;
const SET_BLOCKED: u8 = 44;
const SET_REASON: u8 = 45;
const SET_SWITCH_TIME: u8 = 46;
const CLEAR_NOTE: u8 = 47;
const EVENT: u8 = 48;
const LISTEN: u8 = 49;
const RELAY_CONTEXT: u8 = 50;
const CLIENT: u8 = 51;
const SERVER: u8 = 52;
const SERVER_START: u8 = 53;
const SERVER_READY: u8 = 54;
const RELAY_CANCEL: u8 = 55;
const SERVER_SHUTDOWN: u8 = 56;
const IDLE_CLOSE: u8 = 57;
const RESULT: u8 = 254;
const DONE: u8 = 255;

pub struct Flow {
    q: Request,
    e: Effect,
    first: i64,
    pending: u8,
    started: u8,
    healthy: [bool; 2],
    elapsed: [i64; 2],
    seen: u8,
    count: i64,
    site: i64,
    timeout: i64,
    error: u8,
    token: i64,
    boolean: bool,
    value: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 11 {
            return Err(Error::Invalid);
        }
        // Production indices are fixed by the two-upstream array.
        if (q.op == 0 && !(0..=1).contains(&q.index))
            || (q.op == 2 && (!(0..=1).contains(&q.from) || !(0..=1).contains(&q.to)))
        {
            return Err(Error::Invalid);
        }
        let mut s = Self {
            q,
            e: Effect::default(),
            first: 0,
            pending: 0,
            started: 0,
            healthy: [false; 2],
            elapsed: [0; 2],
            seen: 0,
            count: 0,
            site: 0,
            timeout: 0,
            error: 0,
            token: 0,
            boolean: false,
            value: 0,
        };
        match s.q.op {
            0 => s.emit(PACK),
            1 | 3 | 5 => s.duration(CONTEXT, 5_000_000_000),
            2 => s.change(s.q.from, s.q.to),
            4 => s.duration(TICKER_NEW, 60_000_000_000),
            6 => s.emit(CLOCK_START),
            7 | 9 => s.emit(CUSTOM_PRESENT),
            8 => s.emit(NOTE_PRESENT),
            10 => s.emit(LISTEN),
            11 => s.emit(RELAY_CANCEL),
            _ => unreachable!(),
        }
        Ok(s)
    }
    pub fn next(&self) -> &Effect {
        &self.e
    }
    fn emit(&mut self, k: u8) {
        self.e = Effect {
            kind: k,
            ..Effect::default()
        }
    }
    fn indexed(&mut self, k: u8, i: i64) {
        self.emit(k);
        self.e.index = i;
    }
    fn duration(&mut self, k: u8, d: i64) {
        self.emit(k);
        self.e.duration = d;
    }
    fn result(&mut self) {
        self.e = Effect {
            kind: RESULT,
            error: self.error,
            token: self.token,
            result_bool: self.boolean,
            value: self.value,
            ..Effect::default()
        };
    }
    fn change(&mut self, from: i64, to: i64) {
        if from == to {
            self.changed()
        } else {
            self.indexed(CAS, from);
            self.e.value = to;
        }
    }
    fn changed(&mut self) {
        match self.q.op {
            1 | 3 => self.emit(TIMER_STOP),
            5 => self.emit(CONTEXT_CANCEL),
            _ => self.result(),
        }
    }
    fn resolved(&mut self, b: bool) {
        self.boolean = b;
        self.emit(TIMER_STOP)
    }
    fn confirmed(&mut self, b: bool) {
        if self.q.op == 9 {
            if b {
                self.emit(NOTE_PRESENT)
            } else {
                self.emit(REJECT)
            }
        } else {
            self.boolean = b;
            self.result()
        }
    }
    fn next_site(&mut self) {
        if self.site >= self.count {
            self.confirmed(false)
        } else {
            self.indexed(SITE, self.site);
            self.e.duration = self.timeout;
        }
    }
    fn raw_error(&mut self, t: i64) {
        self.error = 1;
        self.token = t;
        self.result()
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        let k = self.e.kind;
        if k == DONE {
            return Err(Error::Done);
        }
        if r.kind != k {
            return Err(Error::Invalid);
        }
        // Validate enum/index facts before consuming a reply. Rejected replies never advance.
        if (k == PREFERRED
            || k == CHECK_WAIT
            || ((k == HEAD_WAIT || k == RESOLVE_WAIT) && r.event == 1))
            && !(0..=1).contains(&r.index)
        {
            return Err(Error::Invalid);
        }
        if k == HEAD_WAIT && !(1..=3).contains(&r.event) {
            return Err(Error::Invalid);
        }
        if k == RESOLVE_WAIT && r.event != 1 && r.event != 3 {
            return Err(Error::Invalid);
        }
        if k == CHECK_WAIT && self.seen & (1 << r.index) != 0 {
            return Err(Error::Invalid);
        }
        if k == PROBE_CONFIG && !(0..=100_000).contains(&r.value) {
            return Err(Error::Invalid);
        }
        // State/authority and ordinary resource errors are not a negative health verdict.
        // Only the original error-bearing business ports below may follow their normal branches.
        if !r.ok
            && !matches!(
                k,
                PACK | REQUEST
                    | DO
                    | BODY_READ
                    | UNPACK
                    | FAILURE
                    | BODY_CLOSE
                    | EXCHANGE_CHILD
                    | REJECT
                    | SERVER_SHUTDOWN
                    | LISTEN
                    | RESULT
            )
        {
            return Err(Error::Invalid);
        }
        match k {
            PACK => {
                if r.ok {
                    self.indexed(REQUEST, self.q.index)
                } else {
                    self.raw_error(r.token)
                }
            }
            REQUEST => {
                if r.ok {
                    self.emit(DO)
                } else {
                    self.raw_error(r.token)
                }
            }
            DO => {
                if !r.ok {
                    self.raw_error(r.token)
                } else if r.rcode != 200 {
                    self.indexed(FAILURE, r.rcode)
                } else {
                    self.emit(BODY_READ)
                }
            }
            BODY_READ => {
                if r.ok {
                    self.emit(UNPACK)
                } else {
                    self.error = 1;
                    self.token = r.token;
                    self.emit(BODY_CLOSE)
                }
            }
            UNPACK => {
                if !r.ok {
                    self.error = 1;
                    self.token = r.token;
                    self.emit(BODY_CLOSE)
                } else if !valid_answer(&r) {
                    self.indexed(FAILURE, -1)
                } else {
                    self.emit(ANSWER_ACCEPT)
                }
            }
            FAILURE => {
                self.error = 1;
                self.token = r.token;
                self.emit(BODY_CLOSE)
            }
            ANSWER_ACCEPT => {
                self.boolean = true;
                self.emit(BODY_CLOSE)
            }
            BODY_CLOSE => self.result(),
            CONTEXT => {
                if self.q.op == 5 {
                    self.emit(PREFERRED)
                } else {
                    self.emit(COMPLETION_NEW)
                }
            }
            COMPLETION_NEW => {
                if self.q.op == 5 {
                    self.indexed(CHECK_START, 0)
                } else {
                    self.emit(PREFERRED)
                }
            }
            PREFERRED => {
                self.first = r.index;
                if self.q.op == 5 {
                    self.emit(PROBE_NAME)
                } else {
                    self.indexed(RESOLVE_START, self.first)
                }
            }
            RESOLVE_START => {
                self.started += 1;
                if self.started == 1 {
                    self.duration(TIMER_NEW, 500_000_000)
                } else {
                    self.emit(RESOLVE_WAIT)
                }
            }
            TIMER_NEW => {
                self.pending = 2;
                self.emit(HEAD_WAIT)
            }
            HEAD_WAIT => match r.event {
                3 => self.resolved(false),
                1 if r.flag => self.resolved(true),
                _ => {
                    if r.event == 1 {
                        self.pending -= 1;
                    }
                    self.indexed(RESOLVE_START, 1 - self.first)
                }
            },
            RESOLVE_WAIT => {
                if r.event == 3 {
                    self.resolved(false)
                } else {
                    self.pending -= 1;
                    if r.flag {
                        self.boolean = true;
                        self.change(self.first, r.index)
                    } else if self.pending == 0 {
                        self.resolved(false)
                    } else {
                        self.emit(RESOLVE_WAIT)
                    }
                }
            }
            CAS => {
                if r.flag {
                    let from = self.e.index;
                    let to = self.e.value;
                    self.indexed(SWITCH_LOG, from);
                    self.e.value = to;
                } else {
                    self.changed()
                }
            }
            SWITCH_LOG => self.changed(),
            TIMER_STOP => self.emit(CONTEXT_CANCEL),
            CONTEXT_CANCEL => {
                if self.q.op == 3 {
                    if self.boolean {
                        self.emit(ANSWER_WRITE)
                    } else {
                        self.emit(ANSWER_FAILURE)
                    }
                } else {
                    self.result()
                }
            }
            ANSWER_FAILURE => self.emit(ANSWER_WRITE),
            ANSWER_WRITE => {
                self.boolean = false;
                self.result()
            }
            PROBE_NAME => self.emit(COMPLETION_NEW),
            CHECK_START => {
                if self.e.index == 0 {
                    self.indexed(CHECK_START, 1)
                } else {
                    self.emit(CHECK_WAIT)
                }
            }
            CHECK_WAIT => {
                let i = r.index as usize;
                self.seen |= 1 << i;
                self.healthy[i] = r.flag;
                self.elapsed[i] = r.elapsed;
                if self.seen == 3 {
                    let f = self.first as usize;
                    let b = 1 - f;
                    if should_switch(
                        self.healthy[f],
                        self.healthy[b],
                        self.elapsed[f],
                        self.elapsed[b],
                    ) {
                        self.change(self.first, 1 - self.first)
                    } else {
                        self.emit(CONTEXT_CANCEL)
                    }
                } else {
                    self.emit(CHECK_WAIT)
                }
            }
            TICKER_NEW => self.emit(MONITOR_WAIT),
            MONITOR_WAIT => {
                if r.flag {
                    self.emit(CHECK_CHILD)
                } else {
                    self.emit(TICKER_STOP)
                }
            }
            CHECK_CHILD => self.emit(MONITOR_WAIT),
            TICKER_STOP => self.result(),
            CLOCK_START => self.emit(PROBE_QUESTION),
            PROBE_QUESTION => self.emit(EXCHANGE_CHILD),
            EXCHANGE_CHILD => {
                self.boolean = r.ok;
                if r.ok {
                    self.emit(ELAPSED)
                } else {
                    self.result()
                }
            }
            ELAPSED => {
                self.value = r.value;
                self.result()
            }
            CUSTOM_PRESENT => {
                if r.flag {
                    self.emit(CUSTOM_CONFIRM)
                } else {
                    self.emit(BLOCKED)
                }
            }
            CUSTOM_CONFIRM => self.confirmed(r.flag),
            BLOCKED => {
                if r.flag {
                    self.confirmed(false)
                } else {
                    self.emit(RUNNING)
                }
            }
            RUNNING => {
                if r.flag {
                    self.emit(PROBE_CONFIG)
                } else {
                    self.confirmed(false)
                }
            }
            PROBE_CONFIG => {
                if r.index == 0 {
                    self.confirmed(false)
                } else {
                    self.count = r.value;
                    self.timeout = if r.flag { 6_000_000_000 } else { 5_000_000_000 };
                    self.next_site()
                }
            }
            SITE => {
                if path_passed(&r.state) {
                    self.confirmed(true)
                } else {
                    self.site += 1;
                    self.next_site()
                }
            }
            NOTE_PRESENT => {
                if r.flag {
                    self.emit(NOTE_ANOMALY)
                } else {
                    self.emit(ANOMALY_LOG)
                }
            }
            ANOMALY_LOG => self.emit(NOTE_ANOMALY),
            NOTE_ANOMALY => self.result(),
            REJECT => {
                if r.ok {
                    self.emit(SET_BLOCKED)
                } else {
                    self.emit(REJECT_LOG)
                }
            }
            REJECT_LOG => self.result(),
            SET_BLOCKED => self.emit(SET_REASON),
            SET_REASON => self.emit(SET_SWITCH_TIME),
            SET_SWITCH_TIME => self.emit(CLEAR_NOTE),
            CLEAR_NOTE => self.emit(EVENT),
            EVENT => self.result(),
            LISTEN => {
                if r.ok {
                    self.emit(RELAY_CONTEXT)
                } else {
                    self.raw_error(r.token)
                }
            }
            RELAY_CONTEXT => self.emit(CLIENT),
            CLIENT => self.emit(SERVER),
            SERVER => self.emit(SERVER_START),
            SERVER_START => self.emit(SERVER_READY),
            SERVER_READY => self.result(),
            RELAY_CANCEL => self.emit(SERVER_SHUTDOWN),
            SERVER_SHUTDOWN => self.emit(IDLE_CLOSE),
            IDLE_CLOSE => self.result(),
            RESULT => self.emit(DONE),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn new(op: u8) -> Flow {
        Flow::new(Request {
            op,
            ..Request::default()
        })
        .unwrap()
    }
    fn go(f: &mut Flow, k: u8, r: Reply) {
        assert_eq!(f.next().kind, k);
        f.advance(Reply {
            kind: k,
            ok: true,
            ..r
        })
        .unwrap()
    }
    fn ack(f: &mut Flow, k: u8) {
        go(f, k, Reply::default())
    }
    fn valid() -> Reply {
        Reply {
            bits: 7,
            body_len: 65535,
            response: true,
            id_match: true,
            question_match: true,
            questions: 1,
            answer_questions: 1,
            ..Reply::default()
        }
    }
    fn exchange(f: &mut Flow) {
        ack(f, PACK);
        ack(f, REQUEST);
        go(
            f,
            DO,
            Reply {
                rcode: 200,
                ..Reply::default()
            },
        );
        ack(f, BODY_READ)
    }
    fn resolve(f: &mut Flow) {
        assert_eq!(f.next().duration, 5_000_000_000);
        ack(f, CONTEXT);
        ack(f, COMPLETION_NEW);
        ack(f, PREFERRED);
        ack(f, RESOLVE_START);
        assert_eq!(f.next().duration, 500_000_000);
        ack(f, TIMER_NEW)
    }
    #[test]
    fn exchange_validation_and_error_priority() {
        let mut f = new(0);
        exchange(&mut f);
        go(&mut f, UNPACK, valid());
        ack(&mut f, ANSWER_ACCEPT);
        ack(&mut f, BODY_CLOSE);
        assert!(f.next().result_bool);
        for field in 0..9 {
            let mut r = valid();
            match field {
                0 => r.body_len = 65536,
                1 => r.response = false,
                2 => r.id_match = false,
                3 => r.question_match = false,
                4 => r.questions = 0,
                5 => r.answer_questions = 2,
                6 => r.rcode = 2,
                7 => r.rcode = 5,
                _ => r.bits = 15,
            };
            let mut f = new(0);
            exchange(&mut f);
            go(&mut f, UNPACK, r);
            assert_eq!(f.next().index, -1);
            go(
                &mut f,
                FAILURE,
                Reply {
                    token: 99,
                    ..Reply::default()
                },
            );
            ack(&mut f, BODY_CLOSE);
            assert_eq!(f.next().token, 99);
            assert_eq!(f.next().error, 1)
        }
        let mut f = new(0);
        exchange(&mut f);
        f.advance(Reply {
            kind: UNPACK,
            token: 12,
            body_len: 99999,
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, BODY_CLOSE);
        ack(&mut f, BODY_CLOSE);
        assert_eq!(f.next().token, 12);
    }
    #[test]
    fn non200_does_not_read_body() {
        let mut f = new(0);
        ack(&mut f, PACK);
        ack(&mut f, REQUEST);
        go(
            &mut f,
            DO,
            Reply {
                rcode: 302,
                ..Reply::default()
            },
        );
        assert_eq!(f.next().index, 302);
        go(
            &mut f,
            FAILURE,
            Reply {
                token: 4,
                ..Reply::default()
            },
        );
        ack(&mut f, BODY_CLOSE);
        assert_eq!(f.next().token, 4)
    }
    #[test]
    fn request_fail_has_no_body_close() {
        for k in [PACK, REQUEST, DO] {
            let mut f = new(0);
            while f.next().kind != k {
                let k = f.next().kind;
                ack(&mut f, k)
            }
            f.advance(Reply {
                kind: k,
                token: 3,
                ..Reply::default()
            })
            .unwrap();
            assert_eq!(f.next().kind, RESULT);
            assert_eq!(f.next().token, 3)
        }
    }
    #[test]
    fn preferred_early_answer_no_backup_no_cas() {
        let mut f = new(1);
        resolve(&mut f);
        go(
            &mut f,
            HEAD_WAIT,
            Reply {
                event: 1,
                flag: true,
                ..Reply::default()
            },
        );
        ack(&mut f, TIMER_STOP);
        ack(&mut f, CONTEXT_CANCEL);
        assert!(f.next().result_bool)
    }
    #[test]
    fn early_failure_immediately_starts_backup() {
        let mut f = new(1);
        resolve(&mut f);
        go(
            &mut f,
            HEAD_WAIT,
            Reply {
                event: 1,
                ..Reply::default()
            },
        );
        assert_eq!(f.next().index, 1);
        ack(&mut f, RESOLVE_START);
        go(
            &mut f,
            RESOLVE_WAIT,
            Reply {
                event: 1,
                index: 1,
                ..Reply::default()
            },
        );
        ack(&mut f, TIMER_STOP);
        ack(&mut f, CONTEXT_CANCEL);
        assert!(!f.next().result_bool)
    }
    #[test]
    fn timer_keeps_preferred_alive_and_returns_first_success() {
        let mut f = new(1);
        resolve(&mut f);
        go(
            &mut f,
            HEAD_WAIT,
            Reply {
                event: 2,
                ..Reply::default()
            },
        );
        ack(&mut f, RESOLVE_START);
        go(
            &mut f,
            RESOLVE_WAIT,
            Reply {
                event: 1,
                index: 0,
                flag: true,
                ..Reply::default()
            },
        );
        assert_eq!(f.next().kind, TIMER_STOP);
        assert_eq!(f.pending, 1)
    }
    #[test]
    fn backup_answer_cas_failure_still_success() {
        let mut f = new(1);
        resolve(&mut f);
        go(
            &mut f,
            HEAD_WAIT,
            Reply {
                event: 2,
                ..Reply::default()
            },
        );
        ack(&mut f, RESOLVE_START);
        go(
            &mut f,
            RESOLVE_WAIT,
            Reply {
                event: 1,
                index: 1,
                flag: true,
                ..Reply::default()
            },
        );
        assert_eq!((f.next().index, f.next().value), (0, 1));
        ack(&mut f, CAS);
        ack(&mut f, TIMER_STOP);
        ack(&mut f, CONTEXT_CANCEL);
        assert!(f.next().result_bool)
    }
    #[test]
    fn cancellation_has_no_synthetic_priority() {
        for head in [true, false] {
            let mut f = new(3);
            resolve(&mut f);
            if !head {
                go(
                    &mut f,
                    HEAD_WAIT,
                    Reply {
                        event: 2,
                        ..Reply::default()
                    },
                );
                ack(&mut f, RESOLVE_START)
            }
            let k = f.next().kind;
            go(
                &mut f,
                k,
                Reply {
                    event: 3,
                    ..Reply::default()
                },
            );
            ack(&mut f, TIMER_STOP);
            ack(&mut f, CONTEXT_CANCEL);
            ack(&mut f, ANSWER_FAILURE);
            ack(&mut f, ANSWER_WRITE);
            assert!(!f.next().result_bool)
        }
    }
    #[test]
    fn checker_waits_both_and_strict_time_boundaries() {
        for (healthy, a, b, want) in [
            (true, 500_000_000, 1, false),
            (true, 500_000_001, 500_000_001, false),
            (true, 500_000_001, 500_000_000, true),
            (false, 0, 999, true),
        ] {
            let mut f = new(5);
            ack(&mut f, CONTEXT);
            ack(&mut f, PREFERRED);
            ack(&mut f, PROBE_NAME);
            ack(&mut f, COMPLETION_NEW);
            ack(&mut f, CHECK_START);
            ack(&mut f, CHECK_START);
            go(
                &mut f,
                CHECK_WAIT,
                Reply {
                    index: 1,
                    flag: true,
                    elapsed: b,
                    ..Reply::default()
                },
            );
            assert_eq!(f.next().kind, CHECK_WAIT);
            go(
                &mut f,
                CHECK_WAIT,
                Reply {
                    index: 0,
                    flag: healthy,
                    elapsed: a,
                    ..Reply::default()
                },
            );
            assert_eq!(f.next().kind, if want { CAS } else { CONTEXT_CANCEL });
        }
    }
    #[test]
    fn check_duplicate_completion_rejected_without_consumption() {
        let mut f = new(5);
        for k in [
            CONTEXT,
            PREFERRED,
            PROBE_NAME,
            COMPLETION_NEW,
            CHECK_START,
            CHECK_START,
        ] {
            ack(&mut f, k)
        }
        go(&mut f, CHECK_WAIT, Reply::default());
        let before = f.next().clone();
        assert_eq!(
            f.advance(Reply {
                kind: CHECK_WAIT,
                ok: true,
                ..Reply::default()
            }),
            Err(Error::Invalid)
        );
        assert_eq!(f.next(), &before);
        go(
            &mut f,
            CHECK_WAIT,
            Reply {
                index: 1,
                ..Reply::default()
            },
        );
    }
    #[test]
    fn worker_error_omits_elapsed() {
        let mut f = new(6);
        ack(&mut f, CLOCK_START);
        ack(&mut f, PROBE_QUESTION);
        f.advance(Reply {
            kind: EXCHANGE_CHILD,
            ..Reply::default()
        })
        .unwrap();
        assert!(!f.next().result_bool);
        assert_eq!(f.next().error, 0)
    }
    #[test]
    fn monitor_long_lived_no_immediate_check() {
        let mut f = new(4);
        assert_eq!(f.next().duration, 60_000_000_000);
        ack(&mut f, TICKER_NEW);
        for _ in 0..100_001 {
            go(
                &mut f,
                MONITOR_WAIT,
                Reply {
                    flag: true,
                    ..Reply::default()
                },
            );
            ack(&mut f, CHECK_CHILD)
        }
        ack(&mut f, MONITOR_WAIT);
        ack(&mut f, TICKER_STOP);
        assert_eq!(f.next().kind, RESULT)
    }
    #[test]
    fn custom_confirmation_precedes_blocked_and_running() {
        let mut f = new(9);
        go(
            &mut f,
            CUSTOM_PRESENT,
            Reply {
                flag: true,
                ..Reply::default()
            },
        );
        go(
            &mut f,
            CUSTOM_CONFIRM,
            Reply {
                flag: true,
                ..Reply::default()
            },
        );
        ack(&mut f, NOTE_PRESENT);
        ack(&mut f, ANOMALY_LOG);
        ack(&mut f, NOTE_ANOMALY);
        assert_eq!(f.next().kind, RESULT);
        assert!(!f.next().result_bool)
    }
    fn health(f: &mut Flow, android: bool) {
        ack(f, CUSTOM_PRESENT);
        ack(f, BLOCKED);
        go(
            f,
            RUNNING,
            Reply {
                flag: true,
                ..Reply::default()
            },
        );
        go(
            f,
            PROBE_CONFIG,
            Reply {
                index: 7890,
                value: 3,
                flag: android,
                ..Reply::default()
            },
        )
    }
    #[test]
    fn health_order_timeout_and_unknown_pass_state() {
        for android in [false, true] {
            let mut f = new(7);
            health(&mut f, android);
            assert_eq!(
                f.next().duration,
                if android {
                    6_000_000_000
                } else {
                    5_000_000_000
                }
            );
            for (i, v) in [b"error".to_vec(), b"skipped".to_vec()]
                .into_iter()
                .enumerate()
            {
                assert_eq!(f.next().index, i as i64);
                go(
                    &mut f,
                    SITE,
                    Reply {
                        state: v,
                        ..Reply::default()
                    },
                )
            }
            go(
                &mut f,
                SITE,
                Reply {
                    state: b"future-state".to_vec(),
                    ..Reply::default()
                },
            );
            assert!(f.next().result_bool)
        }
    }
    #[test]
    fn reject_failure_and_success_have_different_mutations() {
        for success in [false, true] {
            let mut f = new(9);
            ack(&mut f, CUSTOM_PRESENT);
            go(
                &mut f,
                BLOCKED,
                Reply {
                    flag: true,
                    ..Reply::default()
                },
            );
            f.advance(Reply {
                kind: REJECT,
                ok: success,
                ..Reply::default()
            })
            .unwrap();
            if success {
                for k in [SET_BLOCKED, SET_REASON, SET_SWITCH_TIME, CLEAR_NOTE, EVENT] {
                    ack(&mut f, k)
                }
            } else {
                ack(&mut f, REJECT_LOG)
            }
            assert_eq!(f.next().kind, RESULT)
        }
    }
    #[test]
    fn authority_failure_cannot_turn_into_reject() {
        for k in [CUSTOM_PRESENT, BLOCKED, RUNNING, PROBE_CONFIG] {
            let mut f = new(9);
            while f.next().kind != k {
                let k = f.next().kind;
                go(
                    &mut f,
                    k,
                    Reply {
                        flag: k == RUNNING,
                        ..Reply::default()
                    },
                )
            }
            let old = f.next().clone();
            assert_eq!(
                f.advance(Reply {
                    kind: k,
                    ..Reply::default()
                }),
                Err(Error::Invalid)
            );
            assert_eq!(f.next(), &old)
        }
    }
    #[test]
    fn create_close_exact_resource_order() {
        let mut f = new(10);
        for k in [
            LISTEN,
            RELAY_CONTEXT,
            CLIENT,
            SERVER,
            SERVER_START,
            SERVER_READY,
        ] {
            ack(&mut f, k)
        }
        assert_eq!(f.next().kind, RESULT);
        let mut f = new(11);
        ack(&mut f, RELAY_CANCEL);
        f.advance(Reply {
            kind: SERVER_SHUTDOWN,
            ..Reply::default()
        })
        .unwrap();
        ack(&mut f, IDLE_CLOSE);
        assert_eq!(f.next().kind, RESULT)
    }
}
