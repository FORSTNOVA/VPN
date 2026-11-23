//! Ordered log privacy workflows over fakeable file and sink resources.
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

#[derive(Clone, Debug)]
enum J {
    Null,
    Bool(bool),
    Num(i64),
    Arr(Vec<J>),
    Str(String),
    Obj(Vec<(String, J)>),
}
fn quote(s: &str) -> String {
    let mut o = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => o.push_str("\\\""),
            '\\' => o.push_str("\\\\"),
            '\n' => o.push_str("\\n"),
            '\r' => o.push_str("\\r"),
            '\t' => o.push_str("\\t"),
            c if c < ' ' => o.push_str(&format!("\\u{:04x}", c as u32)),
            c => o.push(c),
        }
    }
    o.push('"');
    o
}
impl J {
    fn json(&self) -> String {
        match self {
            Self::Null => "null".into(),
            Self::Bool(v) => v.to_string(),
            Self::Num(v) => v.to_string(),
            Self::Str(v) => quote(v),
            Self::Arr(v) => format!(
                "[{}]",
                v.iter().map(Self::json).collect::<Vec<_>>().join(",")
            ),
            Self::Obj(v) => format!(
                "{{{}}}",
                v.iter()
                    .map(|(k, v)| format!("{}:{}", quote(k), v.json()))
                    .collect::<Vec<_>>()
                    .join(",")
            ),
        }
    }
}
fn st(v: impl Into<String>) -> J {
    J::Str(v.into())
}
fn obj(v: Vec<(&str, J)>) -> J {
    J::Obj(v.into_iter().map(|(k, v)| (k.into(), v)).collect())
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Done,
}
#[derive(Default)]
struct Outcome {
    status: i64,
    text: String,
    data: Option<J>,
    error: Option<String>,
}
impl Outcome {
    fn fail(status: i64, e: String) -> Self {
        Self {
            status,
            error: Some(e),
            ..Default::default()
        }
    }
    fn json(self) -> String {
        obj(vec![
            ("status", J::Num(self.status)),
            ("text", st(self.text)),
            ("data", self.data.unwrap_or(J::Null)),
            ("hasError", J::Bool(self.error.is_some())),
            ("error", st(self.error.unwrap_or_default())),
        ])
        .json()
    }
}
use std::collections::BTreeSet;
fn unhex(s: &str) -> Result<Vec<u8>, Error> {
    if !s.len().is_multiple_of(2) {
        return Err(Error::Invalid);
    }
    s.as_bytes()
        .chunks_exact(2)
        .map(|p| {
            fn digit(x: u8) -> Option<u8> {
                match x {
                    b'0'..=b'9' => Some(x - b'0'),
                    b'a'..=b'f' => Some(x - b'a' + 10),
                    b'A'..=b'F' => Some(x - b'A' + 10),
                    _ => None,
                }
            }
            Ok(digit(p[0]).ok_or(Error::Invalid)? * 16 + digit(p[1]).ok_or(Error::Invalid)?)
        })
        .collect()
}
fn hex(v: &[u8]) -> String {
    const H: &[u8] = b"0123456789abcdef";
    let mut o = String::with_capacity(v.len() * 2);
    for b in v {
        o.push(H[(b >> 4) as usize] as char);
        o.push(H[(b & 15) as usize] as char)
    }
    o
}
fn count(body: &[u8], needle: &[u8]) -> Result<usize, Error> {
    if needle.is_empty() {
        return Err(Error::Invalid);
    }
    let mut prefix = vec![0; needle.len()];
    let mut n = 0;
    for i in 1..needle.len() {
        while n > 0 && needle[i] != needle[n] {
            n = prefix[n - 1]
        }
        if needle[i] == needle[n] {
            n += 1
        }
        prefix[i] = n
    }
    n = 0;
    let mut total = 0;
    for b in body {
        while n > 0 && *b != needle[n] {
            n = prefix[n - 1]
        }
        if *b == needle[n] {
            n += 1
        }
        if n == needle.len() {
            total += 1;
            n = 0
        }
    }
    Ok(total)
}
#[derive(Clone)]
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u8,
    secrets: Vec<Vec<u8>>,
    summary: String,
    original: usize,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if !matches!(q.op.as_str(), "secrets" | "scrub" | "write") {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            secrets: vec![],
            summary: String::new(),
            original: 0,
        };
        f.emit(
            if f.q.op == "write" {
                "write-source"
            } else {
                "secrets"
            },
            1,
        );
        Ok(f)
    }
    pub fn next(&self) -> &Effect {
        &self.e
    }
    fn emit(&mut self, k: &str, pc: u8) {
        self.e = Effect {
            kind: k.into(),
            ..Default::default()
        };
        self.pc = pc
    }
    fn done(&mut self, o: Outcome) {
        self.emit("result", 0);
        self.e.result = o.json()
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.e.kind == "done" {
            return Err(Error::Done);
        }
        if self.e.kind == "result" {
            self.emit("done", 0);
            return Ok(());
        }
        match self.pc {
            1 => {
                let decoded: Vec<Vec<u8>> =
                    r.names.iter().map(|s| unhex(s)).collect::<Result<_, _>>()?;
                if self.q.op == "write" {
                    let len = r.bytes.len();
                    let mut stripped = r.bytes;
                    for secret in decoded {
                        if count(&stripped, &secret)? > 0 {
                            stripped = crate::service_logic::redact(&stripped, &secret)
                                .ok_or(Error::Limit)?
                        }
                    }
                    self.original = len;
                    self.emit("sink", 2);
                    self.e.text = hex(&stripped)
                } else {
                    let mut seen = BTreeSet::new();
                    let mut indices = vec![];
                    let mut secrets = vec![];
                    for (i, v) in decoded.into_iter().enumerate() {
                        let v = trim(&v).to_vec();
                        if v.len() >= 12 && seen.insert(v.clone()) {
                            indices.push(J::Num(i as i64));
                            secrets.push(v)
                        }
                    }
                    if self.q.op == "secrets" {
                        self.done(Outcome {
                            data: Some(obj(vec![("indices", J::Arr(indices))])),
                            ..Default::default()
                        })
                    } else {
                        self.secrets = secrets;
                        self.emit("read", 3)
                    }
                }
            }
            2 => {
                if let Some(e) = r.error {
                    self.done(Outcome::fail(0, e))
                } else {
                    self.done(Outcome {
                        status: self.original as i64,
                        ..Default::default()
                    })
                }
            }
            3 => {
                if r.error.is_some() {
                    self.done(Outcome::default())
                } else {
                    let start = crate::service_logic::tail_start(&r.bytes, 8 << 20);
                    let dropped = &r.bytes[..start];
                    let mut cleaned = r.bytes[start..].to_vec();
                    let (mut dc, mut rc) = (0usize, 0usize);
                    for secret in &self.secrets {
                        dc += count(dropped, secret)?;
                        let n = count(&cleaned, secret)?;
                        if n > 0 {
                            rc += n;
                            cleaned = crate::service_logic::redact(&cleaned, secret)
                                .ok_or(Error::Limit)?
                        }
                    }
                    if cleaned == r.bytes {
                        self.done(Outcome::default())
                    } else {
                        let note = if dc > 0 {
                            format!(", and those held {dc} subscription URL(s)")
                        } else {
                            String::new()
                        };
                        self.summary=format!("kernel log: kept the newest {} kB, dropped {} kB of older lines{}, redacted {} subscription URL(s)",cleaned.len()/1024,dropped.len()/1024,note,rc);
                        self.emit("write-temp", 4);
                        self.e.text = hex(&cleaned);
                        self.e.number = 0o600
                    }
                }
            }
            4 | 5 => {
                if let Some(e) = r.error {
                    self.emit("log-error", 7);
                    self.e.text = e
                } else if self.pc == 4 {
                    self.emit("rename", 5)
                } else {
                    self.emit("log-summary", 7);
                    self.e.text = std::mem::take(&mut self.summary)
                }
            }
            7 => self.done(Outcome::default()),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}

fn trim(v: &[u8]) -> &[u8] {
    fn space(c: char) -> bool {
        matches!(c,'\u{0009}'..='\u{000d}'|' '|'\u{0085}'|'\u{00a0}'|'\u{1680}'|'\u{2000}'..='\u{200a}'|'\u{2028}'|'\u{2029}'|'\u{202f}'|'\u{205f}'|'\u{3000}')
    }
    let (mut start, mut end) = (0, v.len());
    while start < end {
        let mut consumed = 0;
        for n in 1..=4.min(end - start) {
            if let Ok(s) = std::str::from_utf8(&v[start..start + n]) {
                if s.chars().count() == 1 && space(s.chars().next().unwrap()) {
                    consumed = n;
                    break;
                }
            }
        }
        if consumed == 0 {
            break;
        }
        start += consumed;
    }
    while end > start {
        let mut consumed = 0;
        for n in 1..=4.min(end - start) {
            if let Ok(s) = std::str::from_utf8(&v[end - n..end]) {
                if s.chars().count() == 1 && space(s.chars().next().unwrap()) {
                    consumed = n;
                    break;
                }
            }
        }
        if consumed == 0 {
            break;
        }
        end -= consumed;
    }
    &v[start..end]
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(op: &str, mut r: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(Request {
            op: op.into(),
            ..Default::default()
        })
        .unwrap();
        let mut t = vec![];
        for _ in 0..30 {
            let e = f.next().clone();
            if e.kind == "result" {
                return (e, t);
            }
            let v = r(&e);
            t.push(e);
            f.advance(v).unwrap()
        }
        panic!("unfinished")
    }
    #[test]
    fn trim_bytes_unicode_and_invalid_kept() {
        assert_eq!(trim(b" \xff\t"), b"\xff");
        assert_eq!(trim("\u{0085}secret\u{3000}".as_bytes()), b"secret");
        assert_eq!(trim(b"\x1csecret\x1c"), b"\x1csecret\x1c");
    }
    #[test]
    fn secrets_trim_dedup_order_length() {
        let (r, _) = run("secrets", |_| Reply {
            names: vec![
                hex(b" 123456789012 "),
                hex(b"123456789012"),
                hex(b"short"),
                hex(b"abcdefghijkl"),
            ],
            ..Default::default()
        });
        assert!(r.result.contains("[0,3]"));
    }
    #[test]
    fn write_raw_invalid_bytes_and_original_count() {
        let (r, t) = run("write", |e| {
            if e.kind == "write-source" {
                Reply {
                    bytes: vec![255, 0, 255],
                    names: vec![hex(&[255])],
                    ..Default::default()
                }
            } else {
                Reply::default()
            }
        });
        assert!(r.result.contains("\"status\":3"));
        assert_eq!(unhex(&t[1].text).unwrap(), b"<redacted>\0<redacted>");
    }
    #[test]
    fn sequential_overlap_order_and_sink_error() {
        let (r, t) = run("write", |e| {
            if e.kind == "write-source" {
                Reply {
                    bytes: b"aaaa".to_vec(),
                    names: vec![hex(b"aa"), hex(b"<redacted>")],
                    ..Default::default()
                }
            } else {
                Reply {
                    error: Some("sink".into()),
                    ..Default::default()
                }
            }
        });
        assert_eq!(unhex(&t[1].text).unwrap(), b"<redacted><redacted>");
        assert!(r.result.contains("\"hasError\":true"));
        assert!(r.result.contains("\"status\":0"));
    }
    #[test]
    fn scrub_unchanged_and_readerror_no_writes() {
        for err in [false, true] {
            let (_, t) = run("scrub", |e| {
                if e.kind == "read" {
                    Reply {
                        bytes: b"safe".to_vec(),
                        error: err.then(|| "read".into()),
                        ..Default::default()
                    }
                } else {
                    Reply::default()
                }
            });
            assert_eq!(t.len(), 2);
        }
    }
    #[test]
    fn scrub_write_and_rename_errors_keep_temp() {
        for fail in ["write-temp", "rename"] {
            let (_, t) = run("scrub", |e| match e.kind.as_str() {
                "secrets" => Reply {
                    names: vec![hex(b"abcdefghijkl")],
                    ..Default::default()
                },
                "read" => Reply {
                    bytes: b"abcdefghijkl".to_vec(),
                    ..Default::default()
                },
                k if k == fail => Reply {
                    error: Some("x".into()),
                    ..Default::default()
                },
                _ => Reply::default(),
            });
            assert_eq!(t.last().unwrap().kind, "log-error");
            assert!(!t
                .iter()
                .any(|e| e.kind == "remove" || e.kind == "log-summary"));
            assert_eq!(
                t.iter().find(|e| e.kind == "write-temp").unwrap().number,
                0o600
            );
        }
    }
    #[test]
    fn long_tail_summary_counts_dropped_and_cleaned_separately() {
        let secret = b"abcdefghijkl";
        let mut body = secret.to_vec();
        body.push(b'\n');
        body.extend(vec![b'x'; 8 * 1024 * 1024]);
        body.extend(secret);
        let (_, t) = run("scrub", |e| match e.kind.as_str() {
            "secrets" => Reply {
                names: vec![hex(secret)],
                ..Default::default()
            },
            "read" => Reply {
                bytes: body.clone(),
                ..Default::default()
            },
            _ => Reply::default(),
        });
        let summary = &t.last().unwrap().text;
        assert!(summary.contains("those held 1 subscription URL(s)"));
        assert!(summary.ends_with("redacted 1 subscription URL(s)"));
        assert!(
            unhex(&t.iter().find(|e| e.kind == "write-temp").unwrap().text)
                .unwrap()
                .len()
                > 8 * 1024 * 1024
        );
    }
    #[test]
    fn empty_needle_unsupported_before_sink_and_badhex_atomic() {
        let mut f = Flow::new(Request {
            op: "write".into(),
            ..Default::default()
        })
        .unwrap();
        let before = f.next().clone();
        for names in [vec!["".into()], vec!["gh".into()]] {
            assert_eq!(
                f.advance(Reply {
                    names,
                    bytes: b"x".to_vec(),
                    ..Default::default()
                }),
                Err(Error::Invalid)
            );
            assert_eq!(f.next(), &before);
        }
    }
    #[test]
    fn count_nonoverlap_and_adversarial_prefix() {
        assert_eq!(count(b"aaaaa", b"aa").unwrap(), 2);
        let mut needle = vec![b'a'; 10000];
        needle.push(b'b');
        assert_eq!(count(&vec![b'a'; 100000], &needle).unwrap(), 0);
    }
}
