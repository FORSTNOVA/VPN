//! Session-scoped Subscription conversion workflow orchestration ABI. next is read-only; advance is consumed once.
use crate::converter_flow::{Assignment, Effect, Field, Flow, Node, Reply, Request, Source};
use std::{
    collections::HashMap,
    panic::{catch_unwind, AssertUnwindSafe},
    sync::{Mutex, OnceLock},
};
struct SessionEntry {
    flow: Flow,
    step: u64,
}
struct Registry {
    next: u64,
    sessions: HashMap<u64, SessionEntry>,
}
static REG: OnceLock<Mutex<Registry>> = OnceLock::new();
fn registry() -> &'static Mutex<Registry> {
    REG.get_or_init(|| {
        Mutex::new(Registry {
            next: 1,
            sessions: HashMap::new(),
        })
    })
}
const MAX_INPUT: usize = 8 << 20;
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], ()> {
        let end = self.p.checked_add(n).ok_or(())?;
        let v = self.b.get(self.p..end).ok_or(())?;
        self.p = end;
        Ok(v)
    }
    fn byte(&mut self) -> Result<u8, ()> {
        Ok(self.take(1)?[0])
    }
    fn boolean(&mut self) -> Result<bool, ()> {
        match self.byte()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(()),
        }
    }
    fn integer(&mut self) -> Result<i64, ()> {
        Ok(i64::from_le_bytes(
            self.take(8)?.try_into().map_err(|_| ())?,
        ))
    }
}
impl Reader<'_> {
    fn bytes(&mut self) -> Result<Vec<u8>, ()> {
        let n = usize::try_from(self.integer()?).map_err(|_| ())?;
        Ok(self.take(n)?.to_vec())
    }
    fn count(&mut self) -> Result<usize, ()> {
        let n = usize::try_from(self.integer()?).map_err(|_| ())?;
        if n > 100000 {
            return Err(());
        }
        Ok(n)
    }
    fn ints(&mut self) -> Result<Vec<i64>, ()> {
        (0..self.count()?).map(|_| self.integer()).collect()
    }
    fn node(&mut self) -> Result<Node, ()> {
        Ok(Node {
            name: self.bytes()?,
            kind: self.bytes()?,
            network: self.bytes()?,
            source_label: self.bytes()?,
            tls: self.boolean()?,
            ws_host: self.boolean()?,
            ws_path: self.boolean()?,
        })
    }
    fn field(&mut self) -> Result<Field, ()> {
        Ok(Field {
            key: self.bytes()?,
            reference: self.integer()?,
            trim_ref: self.integer()?,
            text: self.bytes()?,
            trim: self.bytes()?,
            empty: self.boolean()?,
            boolean: self.boolean()?,
            integer: self.integer()?,
            count: self.integer()?,
            trim_empty: self.boolean()?,
            lower: self.bytes()?,
        })
    }
}
fn decode_start(b: &[u8]) -> Result<Flow, ()> {
    let mut r = Reader { b, p: 0 };
    let op = r.byte()?;
    let body = r.integer()?;
    let sources = (0..r.count()?)
        .map(|_| {
            Ok(Source {
                body: r.integer()?,
                label: r.bytes()?,
                prefix: r.bytes()?,
            })
        })
        .collect::<Result<Vec<_>, ()>>()?;
    let q = Request {
        op,
        body,
        sources,
        name: r.bytes()?,
        sources_present: r.boolean()?,
    };
    if r.p != b.len() || (!q.sources_present && !q.sources.is_empty()) {
        return Err(());
    }
    Flow::new(q).map_err(|_| ())
}
fn decode_reply(b: &[u8]) -> Result<Reply, ()> {
    let mut r = Reader { b, p: 0 };
    let q = Reply {
        ok: r.boolean()?,
        token: r.integer()?,
        count: r.integer()?,
        text: r.bytes()?,
        flag: r.boolean()?,
        tokens: r.ints()?,
        fields: (0..r.count()?)
            .map(|_| r.field())
            .collect::<Result<Vec<_>, _>>()?,
        node: r.node()?,
        names: (0..r.count()?)
            .map(|_| r.bytes())
            .collect::<Result<Vec<_>, _>>()?,
        error_token: r.integer()?,
        tokens_present: r.boolean()?,
        fields_present: r.boolean()?,
        names_present: r.boolean()?,
    };
    if r.p != b.len()
        || (!q.tokens_present && !q.tokens.is_empty())
        || (!q.fields_present && !q.fields.is_empty())
        || (!q.names_present && !q.names.is_empty())
    {
        return Err(());
    }
    Ok(q)
}
fn int(o: &mut Vec<u8>, v: i64) {
    o.extend_from_slice(&v.to_le_bytes())
}
fn bytes(o: &mut Vec<u8>, v: &[u8]) {
    int(o, v.len() as i64);
    o.extend_from_slice(v)
}
fn node(o: &mut Vec<u8>, n: &Node) {
    for v in [&n.name, &n.kind, &n.network, &n.source_label] {
        bytes(o, v)
    }
    for v in [n.tls, n.ws_host, n.ws_path] {
        o.push(u8::from(v))
    }
}
fn assignment(o: &mut Vec<u8>, a: &Assignment) {
    bytes(o, &a.key);
    int(o, a.reference);
    bytes(o, &a.text);
    int(o, a.value);
    o.push(a.kind)
}
fn encode(e: &Effect) -> Vec<u8> {
    let mut o = Vec::new();
    bytes(&mut o, &e.kind);
    for v in [e.token, e.reference, e.index] {
        int(&mut o, v)
    }
    bytes(&mut o, &e.key);
    bytes(&mut o, &e.text);
    int(&mut o, e.value);
    o.push(u8::from(e.flag));
    int(&mut o, e.tokens.len() as i64);
    for &v in &e.tokens {
        int(&mut o, v)
    }
    int(&mut o, e.fields.len() as i64);
    for v in &e.fields {
        assignment(&mut o, v)
    }
    node(&mut o, &e.node);
    o.push(e.error);
    int(&mut o, e.error_token);
    o.push(u8::from(e.tokens_present));
    o.push(u8::from(e.fields_present));
    o
}
fn guard(f: impl FnOnce() -> i32) -> i32 {
    catch_unwind(AssertUnwindSafe(f)).unwrap_or(-9)
}
fn aligned<T>(p: *const T) -> bool {
    !p.is_null() && (p as usize).is_multiple_of(std::mem::align_of::<T>())
}
unsafe fn input<'a>(p: *const u8, n: usize) -> Result<&'a [u8], ()> {
    if n > MAX_INPUT || (!aligned(p) && n != 0) {
        return Err(());
    }
    Ok(if n == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(p, n)
    })
}
#[no_mangle]
pub extern "C" fn smartvpn_converter_flow_version_v1() -> u32 {
    2
}
/// # Safety
/// Input and output pointers must reference caller-owned live memory of stated sizes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_converter_flow_start_v1(
    p: *const u8,
    n: usize,
    out: *mut u64,
) -> i32 {
    guard(|| {
        if !aligned(out) {
            return -1;
        }
        *out = 0;
        let Ok(b) = input(p, n) else { return -1 };
        let Ok(flow) = decode_start(b) else { return -2 };
        let Ok(mut r) = registry().lock() else {
            return -9;
        };
        if r.sessions.len() >= 64 || r.next == u64::MAX {
            return -4;
        }
        let id = r.next;
        r.next += 1;
        r.sessions.insert(id, SessionEntry { flow, step: 1 });
        *out = id;
        0
    })
}
/// # Safety
/// Writable outputs must be valid, non-overlapping and suitably aligned.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_converter_flow_next_v1(
    id: u64,
    out: *mut u8,
    cap: usize,
    written: *mut usize,
    step: *mut u64,
) -> i32 {
    guard(|| {
        if !aligned(written) || !aligned(step) || (!aligned(out) && cap != 0) {
            return -1;
        }
        *written = 0;
        *step = 0;
        let Ok(r) = registry().lock() else { return -9 };
        let Some(e) = r.sessions.get(&id) else {
            return -2;
        };
        let b = encode(e.flow.next());
        if b.len() > MAX_INPUT {
            return -4;
        }
        *written = b.len();
        *step = e.step;
        if cap < b.len() {
            return -3;
        }
        std::ptr::copy_nonoverlapping(b.as_ptr(), out, b.len());
        0
    })
}
/// # Safety
/// Input pointer must be valid for n bytes. Advance never executes external effects.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_converter_flow_advance_v1(
    id: u64,
    step: u64,
    p: *const u8,
    n: usize,
) -> i32 {
    guard(|| {
        let Ok(b) = input(p, n) else { return -1 };
        let Ok(reply) = decode_reply(b) else {
            return -2;
        };
        let Ok(mut r) = registry().lock() else {
            return -9;
        };
        let Some(e) = r.sessions.get_mut(&id) else {
            return -2;
        };
        if e.step != step || step == u64::MAX {
            return -5;
        }
        if e.flow.advance(reply).is_err() {
            return -2;
        }
        e.step += 1;
        0
    })
}
#[no_mangle]
pub extern "C" fn smartvpn_converter_flow_close_v1(id: u64) -> i32 {
    guard(|| {
        let Ok(mut r) = registry().lock() else {
            return -9;
        };
        if r.sessions.remove(&id).is_some() {
            0
        } else {
            -2
        }
    })
}
