//! Session-scoped settings orchestration ABI. next is read-only; advance is consumed once.
use crate::settings_flow::{Effect, Flow, Reply, Snapshot};
use std::{
    collections::HashMap,
    panic::{catch_unwind, AssertUnwindSafe},
    sync::{Mutex, OnceLock},
};
struct Entry {
    flow: Flow,
    step: u64,
}
struct Registry {
    next: u64,
    sessions: HashMap<u64, Entry>,
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
const MAX_INPUT: usize = 1024;
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl Reader<'_> {
    fn byte(&mut self) -> Result<u8, ()> {
        let v = *self.b.get(self.p).ok_or(())?;
        self.p += 1;
        Ok(v)
    }
    fn boolean(&mut self) -> Result<bool, ()> {
        match self.byte()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(()),
        }
    }
    fn short(&mut self) -> Result<u16, ()> {
        Ok(u16::from_le_bytes([self.byte()?, self.byte()?]))
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
    let s = Snapshot {
        presence: r.short()?,
        invalid: r.short()?,
        socks_conflict: r.boolean()?,
        domestic: r.boolean()?,
        webrtc: r.boolean()?,
        dns: r.boolean()?,
        strict_value: r.boolean()?,
        domestic_value: r.boolean()?,
        webrtc_value: r.boolean()?,
        dns_value: r.boolean()?,
        url_changed: r.boolean()?,
        url_merged: r.boolean()?,
        store_available: r.boolean()?,
        cache_found: r.boolean()?,
        ensure_needed: r.boolean()?,
    };
    r.finish()?;
    Flow::new(s).map_err(|_| ())
}
fn decode_reply(b: &[u8]) -> Result<Reply, ()> {
    let mut r = Reader { b, p: 0 };
    let reply = match r.byte()? {
        0 => Reply::Ack,
        1 => Reply::Bool(r.boolean()?),
        _ => return Err(()),
    };
    r.finish()?;
    Ok(reply)
}
fn encode(e: Effect) -> Vec<u8> {
    match e {
        Effect::KernelRunning { import_policy } => vec![1, import_policy as u8],
        Effect::ApplySettings {
            presence,
            domestic,
            webrtc,
            dns,
        } => {
            let mut b = vec![2];
            b.extend_from_slice(&presence.to_le_bytes());
            b.extend_from_slice(&[domestic as u8, webrtc as u8, dns as u8]);
            b
        }
        Effect::ClearNodeState => vec![3],
        Effect::RemoveCachedNodes => vec![4],
        Effect::ClearHealth => vec![5],
        Effect::ClearRegions => vec![6],
        Effect::SaveSettings { final_save } => vec![7, final_save as u8],
        Effect::SetProfileFromCache(v) => vec![8, v as u8],
        Effect::UseCachedProfile => vec![9],
        Effect::RemoveProfile { suffix } => vec![10, suffix],
        Effect::AppendSubscription => vec![11],
        Effect::SaveSubscriptions => vec![12],
        Effect::Result { code } => vec![13, code],
        Effect::Done => vec![14],
    }
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
pub extern "C" fn smartvpn_settings_flow_version_v1() -> u32 {
    1
}
/// # Safety
/// Input and output pointers must reference caller-owned live memory of stated sizes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_settings_flow_start_v1(
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
        if r.sessions.len() >= 8 || r.next == u64::MAX {
            return -4;
        }
        let id = r.next;
        r.next += 1;
        r.sessions.insert(id, Entry { flow, step: 1 });
        *out = id;
        0
    })
}
/// # Safety
/// Writable outputs must be valid, non-overlapping and suitably aligned.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_settings_flow_next_v1(
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
pub unsafe extern "C" fn smartvpn_settings_flow_advance_v1(
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
pub extern "C" fn smartvpn_settings_flow_close_v1(id: u64) -> i32 {
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
