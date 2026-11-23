//! Session-scoped quota refresh orchestration ABI. next is read-only; advance is consumed once.
use crate::quota_flow::{Effect, Entry, Flow, Info, Reply, Request};
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
const MAX_ITEMS: usize = 100_000;
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
    fn strings(&mut self) -> Result<Vec<Vec<u8>>, ()> {
        let n = self.count()?;
        if n > MAX_ITEMS {
            return Err(());
        }
        (0..n).map(|_| self.bytes()).collect()
    }
    fn entries(&mut self) -> Result<Vec<Entry>, ()> {
        let n = self.count()?;
        if n > MAX_ITEMS {
            return Err(());
        };
        (0..n)
            .map(|_| {
                Ok(Entry {
                    id: self.bytes()?,
                    url: self.bytes()?,
                })
            })
            .collect()
    }
    fn info(&mut self) -> Result<Option<Info>, ()> {
        if !self.boolean()? {
            return Ok(None);
        };
        Ok(Some(Info {
            upload: self.integer()?,
            download: self.integer()?,
            total: self.integer()?,
            expire: self.integer()?,
        }))
    }
    fn short(&mut self) -> Result<u16, ()> {
        Ok(u16::from_le_bytes(
            self.take(2)?.try_into().map_err(|_| ())?,
        ))
    }
    fn finish(&self) -> Result<(), ()> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(())
        }
    }
}
fn decode_start(b: &[u8]) -> Result<Flow, ()> {
    let mut r = Reader { b, p: 0 };
    let request = Request {
        id: r.bytes()?,
        error_mode: r.boolean()?,
        original_url: r.bytes()?,
        message: r.bytes()?,
        status: r.short()?,
    };
    r.finish()?;
    Flow::new(request).map_err(|_| ())
}
fn decode_reply(b: &[u8]) -> Result<Reply, ()> {
    let mut r = Reader { b, p: 0 };
    let reply = match r.byte()? {
        0 => Reply::Ack,
        1 => Reply::Entries(r.entries()?),
        2 => Reply::Strings(r.strings()?),
        3 => Reply::Fetch {
            ok: r.boolean()?,
            info: r.info()?,
            no_quota: r.boolean()?,
        },
        4 => Reply::Time(r.integer()?),
        5 => Reply::Saved(r.boolean()?),
        _ => return Err(()),
    };
    r.finish()?;
    Ok(reply)
}
fn bytes(out: &mut Vec<u8>, v: &[u8]) {
    out.extend_from_slice(&(v.len() as u64).to_le_bytes());
    out.extend_from_slice(v);
}
fn integer(out: &mut Vec<u8>, v: i64) {
    out.extend_from_slice(&v.to_le_bytes())
}
fn info(out: &mut Vec<u8>, v: Option<Info>) {
    out.push(v.is_some() as u8);
    if let Some(v) = v {
        for x in [v.upload, v.download, v.total, v.expire] {
            integer(out, x)
        }
    }
}
fn encode(e: Effect) -> Vec<u8> {
    let mut out = Vec::new();
    match e {
        Effect::Lock => out.push(1),
        Effect::Categorize(v) => {
            out.push(2);
            bytes(&mut out, &v)
        }
        Effect::Proxies => out.push(3),
        Effect::Unlock => out.push(4),
        Effect::Alternate(v) => {
            out.push(5);
            bytes(&mut out, &v)
        }
        Effect::Fetch { url, proxy } => {
            out.push(6);
            bytes(&mut out, &url);
            bytes(&mut out, &proxy)
        }
        Effect::Relock => out.push(7),
        Effect::Now => out.push(8),
        Effect::Commit {
            index,
            info: v,
            timestamp,
            error,
            message,
        } => {
            out.push(9);
            integer(&mut out, index as i64);
            info(&mut out, v);
            integer(&mut out, timestamp);
            out.push(error);
            bytes(&mut out, &message)
        }
        Effect::Save => out.push(10),
        Effect::Result {
            status,
            code,
            index,
            message,
        } => {
            out.push(11);
            out.extend_from_slice(&status.to_le_bytes());
            out.push(code);
            integer(&mut out, index.map_or(-1, |i| i as i64));
            bytes(&mut out, &message)
        }
        Effect::Done => out.push(12),
    };
    out
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
pub extern "C" fn smartvpn_quota_flow_version_v1() -> u32 {
    1
}
/// # Safety
/// Input and output pointers must reference caller-owned live memory of stated sizes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_quota_flow_start_v1(
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
pub unsafe extern "C" fn smartvpn_quota_flow_next_v1(
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
pub unsafe extern "C" fn smartvpn_quota_flow_advance_v1(
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
pub extern "C" fn smartvpn_quota_flow_close_v1(id: u64) -> i32 {
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
