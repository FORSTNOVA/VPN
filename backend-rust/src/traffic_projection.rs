//! Pure projection and query normalization. All Unicode/path/time parsing facts
//! come from Go standard codecs; this module owns choices, ranking and bounds.
use std::cmp::Reverse;
const UNKNOWN_APP: &str = "未识别应用";
const MAX: usize = 64 << 20;
#[derive(Clone, Default, Debug, PartialEq)]
pub struct Facts {
    // Matches the Go projection wire: process, path, base, host, IP, port,
    // host:port, IP:port, UID, network, type, rule, rule payload.
    pub s: [Vec<u8>; 13],
    pub chains: Vec<Vec<u8>>,
    pub up: i64,
    pub down: i64,
    pub elapsed: i64,
    pub parsed: bool,
}
#[derive(Default)]
struct Request {
    op: u8,
    rows: Vec<Facts>,
    period: Vec<u8>,
    date: Vec<u8>,
    formatted: Vec<u8>,
    parsed: bool,
    offset: Vec<u8>,
    limit: Vec<u8>,
    ov: i64,
    lv: i64,
    ook: bool,
    lok: bool,
}
#[derive(Default, Debug, PartialEq)]
pub struct Row {
    pub s: [Vec<u8>; 6],
    pub up: i64,
    pub down: i64,
    pub duration: i64,
}
#[derive(Default, Debug, PartialEq)]
struct ResultValue {
    code: u8,
    text: Vec<u8>,
    other: Vec<u8>,
    offset: i64,
    limit: i64,
    rows: Option<Vec<Row>>,
}
fn process(f: &Facts) -> Vec<u8> {
    if !f.s[0].is_empty() {
        return f.s[0].clone();
    };
    if f.s[1].is_empty() || f.s[2] == b"." || f.s[2] == b"/" {
        return Vec::new();
    };
    f.s[2].clone()
}
fn target(f: &Facts) -> Vec<u8> {
    if !f.s[3].is_empty() {
        return f.s[if f.s[5].is_empty() { 3 } else { 6 }].clone();
    };
    if f.s[4].is_empty() {
        return Vec::new();
    };
    f.s[if f.s[5].is_empty() { 4 } else { 7 }].clone()
}
fn cat(parts: &[&[u8]]) -> Vec<u8> {
    parts.concat()
}
fn rule(f: &Facts) -> Vec<u8> {
    if f.s[11].is_empty() {
        return Vec::new();
    };
    if f.s[12].is_empty() {
        return f.s[11].clone();
    };
    cat(&[&f.s[11], b"(", &f.s[12], b")"])
}
fn chain(f: &Facts) -> Vec<u8> {
    f.chains
        .iter()
        .find(|x| !x.is_empty())
        .cloned()
        .unwrap_or_default()
}
fn project(q: &Request) -> ResultValue {
    let mut r = ResultValue::default();
    let empty = Facts::default();
    let f = q.rows.first().unwrap_or(&empty);
    match q.op {
        0 => {
            let mut rows: Vec<Row> = q
                .rows
                .iter()
                .map(|f| Row {
                    s: [
                        process(f),
                        target(f),
                        f.s[9].clone(),
                        f.s[10].clone(),
                        rule(f),
                        chain(f),
                    ],
                    up: f.up,
                    down: f.down,
                    duration: if f.parsed && f.elapsed > 0 {
                        f.elapsed / 1_000_000
                    } else {
                        0
                    },
                })
                .collect();
            rows.sort_by_key(|v| Reverse(v.up.wrapping_add(v.down)));
            rows.truncate(80);
            r.rows = Some(rows)
        }
        1 => r.text = process(f),
        2 => r.text = target(f),
        3 => r.text = rule(f),
        4 => r.text = chain(f),
        5 => {
            let name = process(f);
            r.other = name.clone();
            if !f.s[1].is_empty() {
                r.text = cat(&[b"path:", &f.s[1]])
            } else if !name.is_empty() {
                r.text = cat(&[b"process:", &name])
            } else if !f.s[8].is_empty() && f.s[8] != b"-1" {
                r.text = cat(&[b"uid:", &f.s[8]]);
                r.other = cat(&[b"UID ", &f.s[8]])
            } else {
                r.text = b"unknown".to_vec();
                r.other = UNKNOWN_APP.as_bytes().to_vec()
            }
        }
        6 => {
            r.text = match q.period.as_slice() {
                b"hour" => b"2006-01-02T15".to_vec(),
                b"day" => b"2006-01-02".to_vec(),
                b"month" => b"2006-01".to_vec(),
                b"year" => b"2006".to_vec(),
                _ => {
                    r.code = 3;
                    return r;
                }
            };
            r.code = if q.date.is_empty() { 1 } else { 2 }
        }
        7 => {
            if !q.parsed || q.formatted != q.date {
                r.code = 4
            } else {
                r.text = q.date.clone()
            }
        }
        8 => {
            r.limit = 100;
            if !q.offset.is_empty() {
                if !q.ook || q.ov < 0 {
                    return ResultValue {
                        code: 5,
                        ..Default::default()
                    };
                };
                r.offset = q.ov
            };
            if !q.limit.is_empty() {
                if !q.lok || q.lv < 1 || q.lv > 200 {
                    return ResultValue {
                        code: 5,
                        ..Default::default()
                    };
                };
                r.limit = q.lv
            }
        }
        _ => r.code = 6,
    };
    r
}

pub fn connection_rows(rows: Vec<Facts>) -> Vec<Row> {
    project(&Request {
        op: 0,
        rows,
        ..Request::default()
    })
    .rows
    .unwrap_or_default()
}
struct Reader<'a> {
    b: &'a [u8],
    i: usize,
}
impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Option<&'a [u8]> {
        let e = self.i.checked_add(n)?;
        let v = self.b.get(self.i..e)?;
        self.i = e;
        Some(v)
    }
    fn byte(&mut self) -> Option<u8> {
        Some(self.take(1)?[0])
    }
    fn count(&mut self) -> Option<usize> {
        Some(u32::from_le_bytes(self.take(4)?.try_into().ok()?) as usize)
    }
    fn num(&mut self) -> Option<i64> {
        Some(i64::from_le_bytes(self.take(8)?.try_into().ok()?))
    }
    fn flag(&mut self) -> Option<bool> {
        match self.byte()? {
            0 => Some(false),
            1 => Some(true),
            _ => None,
        }
    }
    fn text(&mut self) -> Option<Vec<u8>> {
        let n = self.count()?;
        Some(self.take(n)?.to_vec())
    }
}
fn decode(b: &[u8]) -> Option<Request> {
    let mut r = Reader { b, i: 0 };
    let mut q = Request {
        op: r.byte()?,
        ..Default::default()
    };
    if q.op > 8 {
        return None;
    };
    let n = r.count()?;
    if n > 100000 || n > (r.b.len() - r.i) / 81 {
        return None;
    };
    for _ in 0..n {
        let mut f = Facts::default();
        for s in &mut f.s {
            *s = r.text()?
        }
        let c = r.count()?;
        if c > 100000 || c > (r.b.len() - r.i) / 4 {
            return None;
        };
        for _ in 0..c {
            f.chains.push(r.text()?)
        }
        f.up = r.num()?;
        f.down = r.num()?;
        f.elapsed = r.num()?;
        f.parsed = r.flag()?;
        q.rows.push(f)
    }
    q.period = r.text()?;
    q.date = r.text()?;
    q.formatted = r.text()?;
    q.parsed = r.flag()?;
    q.offset = r.text()?;
    q.limit = r.text()?;
    q.ov = r.num()?;
    q.lv = r.num()?;
    q.ook = r.flag()?;
    q.lok = r.flag()?;
    if r.i != b.len() {
        return None;
    };
    Some(q)
}
fn put(b: &mut Vec<u8>, v: &[u8]) {
    b.extend_from_slice(&(v.len() as u32).to_le_bytes());
    b.extend_from_slice(v)
}
fn encode(r: &ResultValue) -> Vec<u8> {
    let mut b = vec![r.code];
    put(&mut b, &r.text);
    put(&mut b, &r.other);
    b.extend_from_slice(&r.offset.to_le_bytes());
    b.extend_from_slice(&r.limit.to_le_bytes());
    b.push(u8::from(r.rows.is_some()));
    let rows = r.rows.as_deref().unwrap_or(&[]);
    b.extend_from_slice(&(rows.len() as u32).to_le_bytes());
    for row in rows {
        for s in &row.s {
            put(&mut b, s)
        }
        for v in [row.up, row.down, row.duration] {
            b.extend_from_slice(&v.to_le_bytes())
        }
    }
    b
}
/// # Safety
/// Input and output buffers must reference their stated lengths. `written` is
/// writable. Status 1 reports the needed output capacity; this call is pure.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_traffic_projection_v1(
    input: *const u8,
    len: usize,
    output: *mut u8,
    cap: usize,
    written: *mut usize,
) -> i32 {
    if input.is_null()
        || written.is_null()
        || !(written as usize).is_multiple_of(std::mem::align_of::<usize>())
        || len == 0
        || len > MAX
        || cap > MAX
        || output.is_null()
    {
        return -1;
    }
    std::panic::catch_unwind(|| {
        let bytes = unsafe { std::slice::from_raw_parts(input, len) };
        let Some(q) = decode(bytes) else { return -1 };
        let out = encode(&project(&q));
        if out.len() > MAX {
            return -1;
        };
        unsafe { *written = out.len() };
        if out.len() > cap {
            return 1;
        };
        unsafe { std::ptr::copy_nonoverlapping(out.as_ptr(), output, out.len()) };
        0
    })
    .unwrap_or(-1)
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn stable_wrapping_rank_and_cap() {
        let mut q = Request::default();
        for i in 0..90 {
            let mut f = Facts {
                up: 1,
                ..Default::default()
            };
            f.s[0] = i.to_string().into_bytes();
            q.rows.push(f)
        }
        q.rows[0].up = i64::MAX;
        q.rows[0].down = 1;
        let r = project(&q);
        let rows = r.rows.unwrap();
        assert_eq!(rows.len(), 80);
        assert_eq!(rows[0].s[0], b"1");
        assert_eq!(rows[79].s[0], b"80")
    }
    #[test]
    fn uid_is_not_trimmed() {
        let mut f = Facts::default();
        f.s[8] = b" -1 ".to_vec();
        let r = project(&Request {
            op: 5,
            rows: vec![f],
            ..Default::default()
        });
        assert_eq!(r.text, b"uid: -1 ");
        assert_eq!(r.other, b"UID  -1 ")
    }
    #[test]
    fn date_roundtrip_and_page() {
        assert_eq!(
            project(&Request {
                op: 6,
                period: b" day".to_vec(),
                ..Default::default()
            })
            .code,
            3
        );
        assert_eq!(
            project(&Request {
                op: 7,
                parsed: true,
                date: b"2020-1".to_vec(),
                formatted: b"2020-01".to_vec(),
                ..Default::default()
            })
            .code,
            4
        );
        assert_eq!(
            project(&Request {
                op: 8,
                ..Default::default()
            })
            .limit,
            100
        );
        assert_eq!(
            project(&Request {
                op: 8,
                limit: b"0".to_vec(),
                lok: true,
                ..Default::default()
            })
            .code,
            5
        )
    }
    #[test]
    fn bytes_and_duration() {
        let mut f = Facts {
            elapsed: 999999,
            parsed: true,
            ..Default::default()
        };
        f.s[0] = vec![255, 128];
        let r = project(&Request {
            rows: vec![f],
            ..Default::default()
        });
        let rows = r.rows.unwrap();
        assert_eq!(rows[0].s[0], vec![255, 128]);
        assert_eq!(rows[0].duration, 0)
    }
}
