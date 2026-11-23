//! Session-scoped Subscription HTTP and analysis workflow orchestration ABI. next is read-only; advance is consumed once.
use crate::subscription_http_flow::{Effect, Flow, Node, Reply, Request};
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
    fn count(&mut self) -> Result<usize, ()> {
        usize::try_from(self.integer()?).map_err(|_| ())
    }
    fn bytes(&mut self) -> Result<Vec<u8>, ()> {
        let n = self.count()?;
        Ok(self.take(n)?.to_vec())
    }

    fn list<T>(&mut self, f: impl Fn(&mut Self) -> Result<T, ()>) -> Result<Vec<T>, ()> {
        let n = self.count()?;
        if n > 100000 {
            return Err(());
        };
        (0..n).map(|_| f(self)).collect()
    }
}
fn decode_start(b: &[u8]) -> Result<Flow, ()> {
    let mut r = Reader { b, p: 0 };
    let q = Request { op: r.byte()? };
    if r.p != b.len() {
        return Err(());
    };
    Flow::new(q).map_err(|_| ())
}
fn decode_reply(b: &[u8]) -> Result<Reply, ()> {
    let mut r = Reader { b, p: 0 };
    let q = Reply {
        kind: r.byte()?,
        ok: r.boolean()?,
        flag: r.boolean()?,
        clash: r.boolean()?,
        json: r.boolean()?,
        uri: r.boolean()?,
        info: r.boolean()?,
        host: r.boolean()?,
        path: r.boolean()?,
        count: r.integer()?,
        other: r.integer()?,
        text: r.bytes()?,
        network: r.bytes()?,
        tls: r.bytes()?,
        names: r.list(Reader::bytes)?,
        nodes: r.list(|r| {
            Ok(Node {
                vmess: r.boolean()?,
                network: r.bytes()?,
                tls: r.boolean()?,
                ws_host: r.boolean()?,
                ws_path: r.boolean()?,
            })
        })?,
    };
    if r.p != b.len() {
        return Err(());
    };
    Ok(q)
}
fn bytes(o: &mut Vec<u8>, v: &[u8]) {
    o.extend_from_slice(&(v.len() as u64).to_le_bytes());
    o.extend_from_slice(v)
}
fn encode(e: &Effect) -> Vec<u8> {
    let mut o = vec![e.kind];
    bytes(&mut o, &e.text);
    o.extend_from_slice(&e.count.to_le_bytes());
    o.extend_from_slice(&e.status.to_le_bytes());
    o.push(u8::from(e.flag));
    o.extend_from_slice(&e.interval.to_le_bytes());
    o.extend_from_slice(&(e.names.len() as u64).to_le_bytes());
    for n in &e.names {
        bytes(&mut o, n)
    }
    let a = &e.analysis;
    o.push(u8::from(a.present));
    bytes(&mut o, &a.format);
    for v in [
        a.bytes,
        a.selectable,
        a.valid_vmess,
        a.info_entries,
        a.ws_host_set,
        a.ws_path_set,
    ] {
        o.extend_from_slice(&v.to_le_bytes())
    }
    for vs in [&a.uri_counts, &a.networks, &a.tls_modes] {
        o.extend_from_slice(&(vs.len() as u64).to_le_bytes());
        for v in vs {
            bytes(&mut o, &v.name);
            o.extend_from_slice(&v.count.to_le_bytes())
        }
    }
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
pub extern "C" fn smartvpn_subscription_http_flow_version_v1() -> u32 {
    1
}
/// # Safety
/// Input and output pointers must reference caller-owned live memory of stated sizes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_subscription_http_flow_start_v1(
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
pub unsafe extern "C" fn smartvpn_subscription_http_flow_next_v1(
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
pub unsafe extern "C" fn smartvpn_subscription_http_flow_advance_v1(
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
pub extern "C" fn smartvpn_subscription_http_flow_close_v1(id: u64) -> i32 {
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
