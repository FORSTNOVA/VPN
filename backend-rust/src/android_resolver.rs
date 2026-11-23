//! Android resolver rotation. Socket/context/connection/atomic resources stay in Go.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    pub op: String,
    pub path: String,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Effect {
    pub kind: String,
    pub text: String,
    pub other: String,
    pub number: i64,
    pub result: String,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Reply {
    pub text: String,
    pub number: i64,
    pub flag: bool,
    pub names: Vec<String>,
    pub bytes: Vec<u8>,
    pub error: Option<String>,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}
fn quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c < ' ' => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}
pub struct Flow {
    effect: Effect,
    pc: u8,
    count: i64,
    start: i64,
    tried: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op != "dial" {
            return Err(Error::Invalid);
        };
        Ok(Self {
            effect: Effect {
                kind: "count".into(),
                ..Default::default()
            },
            pc: 0,
            count: 0,
            start: 0,
            tried: 0,
        })
    }
    pub fn next(&self) -> &Effect {
        &self.effect
    }
    fn emit(&mut self, k: &str, n: i64, text: &str) {
        self.effect = Effect {
            kind: k.into(),
            number: n,
            text: text.into(),
            ..Default::default()
        }
    }
    // count is positive and start/tried are in [0,count). The intermediate
    // sum can exceed i64 even though the final index always fits.
    fn dial_index(&self) -> i64 {
        ((i128::from(self.start) + i128::from(self.tried)) % i128::from(self.count)) as i64
    }
    fn finish(&mut self, status: i64, error: Option<String>) {
        self.pc = 3;
        self.effect = Effect {
            kind: "result".into(),
            result: format!(
                "{{\"status\":{},\"text\":\"\",\"data\":null,\"hasError\":{},\"error\":{}}}",
                status,
                error.is_some(),
                quote(error.as_deref().unwrap_or(""))
            ),
            ..Default::default()
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        match self.pc {
            0 => {
                self.count = r.number;
                self.pc = 1;
                self.emit("advance", 0, "")
            }
            1 => {
                if self.count <= 0 || r.number < 0 {
                    return Err(Error::Invalid);
                };
                self.start = r.number % self.count;
                self.pc = 2;
                self.emit("dial", self.start, "2500")
            }
            2 => {
                let index = self.dial_index();
                if r.error.is_none() {
                    self.finish(index + 1, None)
                } else {
                    self.tried += 1;
                    if self.tried == self.count {
                        self.finish(0, r.error)
                    } else {
                        self.emit("dial", self.dial_index(), "2500")
                    }
                }
            }
            _ => return Err(Error::Done),
        };
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn begin(counter: i64) -> Flow {
        let mut f = Flow::new(Request {
            op: "dial".into(),
            ..Default::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, "count");
        f.advance(Reply {
            number: 5,
            ..Default::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, "advance");
        f.advance(Reply {
            number: counter,
            ..Default::default()
        })
        .unwrap();
        f
    }
    #[test]
    fn maximum_count_modular_index_never_overflows() {
        let mut f = begin(0);
        f.count = i64::MAX;
        f.start = i64::MAX - 1;
        f.tried = 0;
        f.emit("dial", f.start, "2500");
        for expected in [i64::MAX - 1, 0] {
            assert_eq!(f.next().number, expected);
            f.advance(Reply {
                error: Some("failed".into()),
                ..Default::default()
            })
            .unwrap();
        }
        assert_eq!(f.next().number, 1);
        f.advance(Reply::default()).unwrap();
        assert!(f.next().result.contains("\"status\":2"));
    }
    #[test]
    fn maximum_final_attempt_and_success_status_fit() {
        let mut f = begin(0);
        f.count = i64::MAX;
        f.start = i64::MAX - 1;
        f.tried = i64::MAX - 1;
        assert_eq!(f.dial_index(), i64::MAX - 2);
        f.advance(Reply {
            error: Some("last".into()),
            ..Default::default()
        })
        .unwrap();
        assert!(f.next().result.contains("last"));
        let mut f = begin(0);
        f.count = i64::MAX;
        f.start = i64::MAX - 1;
        f.advance(Reply::default()).unwrap();
        assert!(f
            .next()
            .result
            .contains(&format!("\"status\":{}", i64::MAX)));
    }
    #[test]
    fn rotation_add_before_modulo_and_timeout() {
        let mut f = begin(1);
        for n in [1, 2, 3, 4, 0] {
            assert_eq!(f.next().number, n);
            assert_eq!(f.next().text, "2500");
            f.advance(Reply {
                error: Some(format!("error{n}")),
                ..Default::default()
            })
            .unwrap()
        }
        assert!(f.next().result.contains("error0"));
        assert_eq!(f.next().kind, "result")
    }
    #[test]
    fn first_success_stops_and_last_error_is_not_reused() {
        let mut f = begin(4);
        f.advance(Reply {
            error: Some("first".into()),
            ..Default::default()
        })
        .unwrap();
        assert_eq!(f.next().number, 0);
        f.advance(Reply::default()).unwrap();
        assert!(f.next().result.contains("\"status\":1"));
        assert!(!f.next().result.contains("first"));
        assert_eq!(f.advance(Reply::default()), Err(Error::Done))
    }
    #[test]
    fn unsigned_counter_wrap_and_empty_error() {
        let mut f = begin(0);
        assert_eq!(f.next().number, 0);
        for _ in 0..5 {
            f.advance(Reply {
                error: Some(String::new()),
                ..Default::default()
            })
            .unwrap()
        }
        assert!(f.next().result.contains("\"hasError\":true"))
    }
}
