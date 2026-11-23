//! Complete Wintun installation decisions over narrow platform resource facts.
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

use crate::driver_install_text::*;
#[derive(Clone, Debug)]
enum J {
    Null,
    Bool(bool),
    Num(i64),
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
struct Engine<'a> {
    replies: &'a [Reply],
    pos: usize,
}
type Step<T> = Result<T, Effect>;
fn message(i: usize, args: &[String]) -> String {
    let t = TEXT[i];
    let mut o = String::new();
    let mut c = t.chars().peekable();
    let mut n = 0;
    while let Some(v) = c.next() {
        if v == '%' {
            let k = c.next().unwrap_or('%');
            if matches!(k, 's' | 'd' | 'w') {
                o.push_str(args.get(n).map(String::as_str).unwrap_or(""));
                n += 1;
            } else {
                o.push('%');
                o.push(k)
            }
        } else {
            o.push(v)
        }
    }
    o
}
impl Engine<'_> {
    fn call(&mut self, k: &str, t: &str, o: &str, n: i64) -> Step<Reply> {
        if let Some(r) = self.replies.get(self.pos) {
            self.pos += 1;
            Ok(r.clone())
        } else {
            Err(Effect {
                kind: k.into(),
                text: t.into(),
                other: o.into(),
                number: n,
                result: String::new(),
            })
        }
    }
    fn pe(&mut self) -> Step<Option<String>> {
        let r = self.call("pe-header", "", "", 0)?;
        if r.number < 64 || r.bytes.len() < 64 || !r.bytes.starts_with(b"MZ") {
            return Ok(Some(ARCH.into()));
        }
        let off = u32::from_le_bytes(r.bytes[60..64].try_into().unwrap()) as i64;
        if off <= 0 || off + 6 > r.number {
            return Ok(Some(ARCH.into()));
        }
        let p = self.call("pe-at", "", "", off)?.bytes;
        if p.len() != 6 || &p[..4] != b"PE\0\0" {
            return Ok(Some(ARCH.into()));
        }
        let m = u16::from_le_bytes([p[4], p[5]]);
        Ok((m != 0x8664).then(|| {
            TEXT[0]
                .replace("%w", ARCH)
                .replace("%04x", &format!("{m:04x}"))
        }))
    }
    fn signature(&mut self, path: &str) -> Step<(String, Option<String>)> {
        let r = self.call("signature", path, "", 0)?;
        let text = r.text.trim();
        if r.error.is_some() {
            return Ok((String::new(), Some(message(1, &[text.into()]))));
        }
        let Some(subject) = text.strip_prefix("valid:") else {
            return Ok((
                String::new(),
                Some(message(
                    2,
                    &[
                        BAD.into(),
                        text.strip_prefix("invalid:").unwrap_or(text).into(),
                    ],
                )),
            ));
        };
        if !subject.contains(SIGNER) {
            return Ok((
                subject.into(),
                Some(message(3, &[BAD.into(), subject.into()])),
            ));
        }
        Ok((SIGNER.into(), None))
    }
    fn download(&mut self) -> Step<Option<String>> {
        let r = self.call("http-open", URL, "", 0)?;
        if r.error.is_some() {
            return Ok(Some(TEXT[4].into()));
        }
        let result = self.download_open(r.number)?;
        self.call("http-close", "", "", 0)?;
        Ok(result)
    }
    fn download_open(&mut self, status: i64) -> Step<Option<String>> {
        if status != 200 {
            return Ok(Some(message(5, &[status.to_string()])));
        }
        if self
            .call("http-read", "", "", 8 * 1024 * 1024)?
            .error
            .is_some()
        {
            return Ok(Some(TEXT[6].into()));
        }
        if self.call("hash-zip", "", "", 0)?.text != ZIP_HASH {
            return Ok(Some(message(7, &[BAD.into()])));
        }
        let z = self.call("zip-open", "", "", 0)?;
        if z.error.is_some() {
            return Ok(Some(message(8, &[BAD.into()])));
        }
        for (i, n) in z.names.iter().enumerate() {
            if n != ENTRY {
                continue;
            }
            if self
                .call("zip-entry-open", "", "", i as i64)?
                .error
                .is_some()
            {
                return Ok(Some(message(9, &[BAD.into()])));
            }
            let result = self.entry()?;
            self.call("zip-entry-close", "", "", 0)?;
            return Ok(result);
        }
        Ok(Some(message(11, &[BAD.into()])))
    }
    fn entry(&mut self) -> Step<Option<String>> {
        if self
            .call("zip-entry-read", "", "", 2 * 1024 * 1024)?
            .error
            .is_some()
        {
            return Ok(Some(message(9, &[BAD.into()])));
        }
        if self.call("hash-dll", "", "", 0)?.text != DLL_HASH {
            return Ok(Some(message(10, &[BAD.into()])));
        }
        self.pe()
    }
    fn status(&mut self, detail: Option<String>) -> Step<(bool, J)> {
        let p = self.call("target", "", "", 0)?.text;
        let r = self.call("stat", &p, "", 0)?;
        let installed = r.error.is_none() && !r.flag;
        let mut v = vec![("path", st(p.clone())), ("installed", J::Bool(installed))];
        let mut d = if installed {
            String::new()
        } else {
            NOT_INSTALLED.into()
        };
        if installed {
            v.push(("version", st(VERSION)));
            v.push(("signer", st(SIGNER)));
            let h = self.call("hash-file", &p, "", 0)?;
            if h.error.is_none() && !h.text.is_empty() {
                v.push(("sha256", st(h.text)))
            }
        }
        if let Some(x) = detail {
            d = x
        }
        if !d.is_empty() {
            v.push(("detail", st(d)))
        }
        Ok((installed, obj(v)))
    }
    fn install(&mut self) -> Step<Outcome> {
        let r = self.call("decode", "", "", 0)?;
        if r.error.is_some() && !r.flag {
            return Ok(Outcome::fail(400, "invalid request body".into()));
        }
        let source = r.text.trim();
        let detail = if !source.is_empty() {
            if self.call("read-local", source, "", 0)?.error.is_some() {
                return Ok(Outcome::fail(400, TEXT[12].into()));
            }
            if let Some(e) = self.pe()? {
                return Ok(Outcome::fail(400, e));
            }
            TEXT[13].into()
        } else {
            if let Some(e) = self.download()? {
                return Ok(Outcome::fail(502, e));
            }
            message(14, &[VERSION.into()])
        };
        self.call("lock", "", "", 0)?;
        let target = self.call("target", "", "", 0)?.text;
        let temp = format!("{target}.tmp");
        if self.call("write-temp", &temp, "", 0o600)?.error.is_some() {
            return Ok(Outcome::fail(500, TEXT[15].into()));
        }
        let (signer, error) = self.signature(&temp)?;
        if let Some(e) = error {
            self.call("remove-temp", &temp, "", 0)?;
            return Ok(Outcome::fail(502, e));
        }
        if self.call("rename", &temp, &target, 0)?.error.is_some() {
            self.call("remove-temp", &temp, "", 0)?;
            return Ok(Outcome::fail(500, TEXT[16].into()));
        }
        self.call("log", &target, &signer, 0)?;
        let (_, status) = self.status(Some(detail))?;
        let available = if self.call("elevated", "", "", 0)?.flag {
            self.status(None)?.0
        } else {
            false
        };
        Ok(Outcome {
            status: 200,
            data: Some(obj(vec![
                ("ok", J::Bool(true)),
                ("wintun", status),
                ("available", J::Bool(available)),
            ])),
            ..Default::default()
        })
    }
    fn run(&mut self, q: &Request) -> Step<Outcome> {
        match q.op.as_str() {
            "install" => self.install(),
            "download" => Ok(Outcome {
                error: self.download()?,
                ..Default::default()
            }),
            "pe" => Ok(Outcome {
                error: self.pe()?,
                ..Default::default()
            }),
            "signature" => {
                let (text, error) = self.signature(&q.path)?;
                Ok(Outcome {
                    text,
                    error,
                    ..Default::default()
                })
            }
            _ => unreachable!(),
        }
    }
}
#[derive(Clone)]
pub struct Flow {
    request: Request,
    replies: Vec<Reply>,
    effect: Effect,
    used: usize,
}
impl Flow {
    pub fn new(request: Request) -> Result<Self, Error> {
        if !matches!(
            request.op.as_str(),
            "install" | "download" | "pe" | "signature"
        ) {
            return Err(Error::Invalid);
        }
        let mut f = Self {
            request,
            replies: Vec::new(),
            effect: Effect::default(),
            used: 0,
        };
        f.refresh();
        Ok(f)
    }
    fn refresh(&mut self) {
        self.effect = match (Engine {
            replies: &self.replies,
            pos: 0,
        })
        .run(&self.request)
        {
            Ok(o) => Effect {
                kind: "result".into(),
                result: o.json(),
                ..Default::default()
            },
            Err(e) => e,
        }
    }
    pub fn next(&self) -> &Effect {
        &self.effect
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.effect.kind == "done" {
            return Err(Error::Done);
        }
        if self.effect.kind == "result" {
            self.effect = Effect {
                kind: "done".into(),
                ..Default::default()
            };
            return Ok(());
        }
        let size = std::mem::size_of::<Reply>()
            + r.text.len()
            + r.bytes.len()
            + r.error.as_ref().map_or(0, String::len)
            + r.names
                .iter()
                .map(|s| s.len() + std::mem::size_of::<String>())
                .sum::<usize>();
        if self.replies.len() >= 256 || size > 8 * 1024 * 1024 - self.used {
            return Err(Error::Limit);
        }
        self.used += size;
        self.replies.push(r);
        self.refresh();
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(op: &str, mut reply: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(Request {
            op: op.into(),
            path: "x".into(),
        })
        .unwrap();
        let mut trace = vec![];
        for _ in 0..100 {
            let e = f.next().clone();
            if e.kind == "result" {
                return (e, trace);
            }
            let r = reply(&e);
            trace.push(e);
            f.advance(r).unwrap()
        }
        panic!("unfinished")
    }
    fn valid(e: &Effect) -> Reply {
        let mut r = Reply::default();
        match e.kind.as_str() {
            "pe-header" => {
                r.number = 100;
                r.bytes = vec![0; 64];
                r.bytes[..2].copy_from_slice(b"MZ");
                r.bytes[60] = 64
            }
            "pe-at" => r.bytes = b"PE\0\0\x64\x86".to_vec(),
            "http-open" => r.number = 200,
            "hash-zip" => r.text = ZIP_HASH.into(),
            "hash-dll" => r.text = DLL_HASH.into(),
            "zip-open" => r.names = vec!["skip".into(), ENTRY.into()],
            "signature" => r.text = format!("valid:{SIGNER}"),
            "decode" => r.text = " local ".into(),
            "target" => r.text = "target".into(),
            _ => {}
        }
        r
    }
    #[test]
    fn pe_valid_and_arch() {
        let (e, t) = run("pe", valid);
        assert!(e.result.contains("\"hasError\":false"));
        assert_eq!(t[1].number, 64);
        let (e, _) = run("pe", |e| {
            let mut r = valid(e);
            if e.kind == "pe-at" {
                r.bytes[4] = 0x4c;
                r.bytes[5] = 1
            }
            r
        });
        assert!(e.result.contains("machine 0x014c"));
    }
    #[test]
    fn pe_bad_headers_skip_at() {
        for bytes in [vec![], vec![0; 64]] {
            let (_, t) = run("pe", |_| Reply {
                bytes: bytes.clone(),
                number: 64,
                ..Default::default()
            });
            assert_eq!(t.len(), 1)
        }
    }
    #[test]
    fn signature_priority_and_literal_format_tokens() {
        let (e, _) = run("signature", |_| Reply {
            text: " invalid:bad %s ".into(),
            error: Some("exit".into()),
            ..Default::default()
        });
        assert!(e.result.contains("invalid:bad %s"));
        let (e, _) = run("signature", |_| Reply {
            text: "valid:wrong %w".into(),
            ..Default::default()
        });
        assert!(e.result.contains("wrong %w"));
        assert!(e.result.contains("\"text\":\"wrong %w\""));
    }
    #[test]
    fn download_cleanup_lifo() {
        let (e, t) = run("download", valid);
        assert!(e.result.contains("\"hasError\":false"));
        let k: Vec<_> = t.iter().map(|e| e.kind.as_str()).collect();
        assert_eq!(&k[k.len() - 2..], ["zip-entry-close", "http-close"]);
        assert_eq!(
            t.iter()
                .find(|e| e.kind == "zip-entry-open")
                .unwrap()
                .number,
            1
        )
    }
    #[test]
    fn every_download_failure_cleans_only_owned_resources() {
        for fail in [
            "http-open",
            "http-read",
            "zip-open",
            "zip-entry-open",
            "zip-entry-read",
        ] {
            let (_, t) = run("download", |e| {
                let mut r = valid(e);
                if e.kind == fail {
                    r.error = Some("x".into())
                }
                r
            });
            let k: Vec<_> = t.iter().map(|e| e.kind.as_str()).collect();
            assert_eq!(k.contains(&"http-close"), fail != "http-open");
            assert_eq!(k.contains(&"zip-entry-close"), fail == "zip-entry-read");
        }
    }
    #[test]
    fn install_local_lock_and_response_no_unlock() {
        let (e, t) = run("install", valid);
        assert!(e.result.contains("\"status\":200"));
        assert!(!t
            .iter()
            .any(|e| e.kind == "unlock" || e.kind == "http-open"));
        let w = t.iter().find(|e| e.kind == "write-temp").unwrap();
        assert_eq!(w.number, 0o600);
        assert_eq!(w.text, "target.tmp");
    }
    #[test]
    fn install_failure_cleanup_matches_original() {
        for fail in ["read-local", "write-temp", "signature", "rename"] {
            let (e, t) = run("install", |e| {
                let mut r = valid(e);
                if e.kind == fail {
                    r.error = Some("x".into())
                }
                r
            });
            assert!(e.result.contains("\"hasError\":true"));
            assert_eq!(
                t.iter().filter(|e| e.kind == "remove-temp").count(),
                usize::from(fail == "signature" || fail == "rename")
            );
        }
    }
    #[test]
    fn decode_eof_allowed_non_eof_rejected() {
        for eof in [false, true] {
            let (e, t) = run("install", |e| {
                let mut r = valid(e);
                if e.kind == "decode" {
                    r.error = Some("decode".into());
                    r.flag = eof
                }
                r
            });
            assert_eq!(t.len() == 1, !eof);
            assert!(e.result.contains(if eof {
                "\"status\":200"
            } else {
                "\"status\":400"
            }));
        }
    }
    #[test]
    fn no_admin_does_not_repeat_status() {
        let (_, t) = run("install", valid);
        assert_eq!(t.iter().filter(|e| e.kind == "target").count(), 2);
        let (_, t) = run("install", |e| {
            let mut r = valid(e);
            if e.kind == "elevated" {
                r.flag = true
            }
            r
        });
        assert_eq!(t.iter().filter(|e| e.kind == "target").count(), 3);
    }
    #[test]
    fn oversize_reply_does_not_consume() {
        let mut f = Flow::new(Request {
            op: "pe".into(),
            ..Default::default()
        })
        .unwrap();
        let e = f.next().clone();
        assert_eq!(
            f.advance(Reply {
                bytes: vec![0; 8 * 1024 * 1024],
                ..Default::default()
            }),
            Err(Error::Limit)
        );
        assert_eq!(f.next(), &e);
    }
}
