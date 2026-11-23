//! Session-scoped privacy log workflow orchestration ABI. next is read-only; advance is consumed once.
use crate::privacy_log::{Effect, Flow, Reply, Request};
use std::{
    collections::HashMap,
    panic::{catch_unwind, AssertUnwindSafe},
    sync::{Mutex, OnceLock},
};
struct SessionEntry {
    flow: Flow,
    step: u64,
    input_bytes: usize,
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
const MAX_INPUT: usize = 64 << 20;
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
    fn string(&mut self) -> Result<String, ()> {
        String::from_utf8(self.bytes()?).map_err(|_| ())
    }
}
fn decode_start(b: &[u8]) -> Result<Flow, ()> {
    let mut r = Reader { b, p: 0 };
    let q = Request {
        op: r.string()?,
        path: r.string()?,
    };
    if r.p != b.len() {
        return Err(());
    };
    Flow::new(q).map_err(|_| ())
}
fn decode_reply(b: &[u8]) -> Result<Reply, ()> {
    let mut r = Reader { b, p: 0 };
    let text = r.string()?;
    let number = r.integer()?;
    let flag = r.boolean()?;
    let names = r.list(Reader::string)?;
    let bytes = r.bytes()?;
    let has_error = r.boolean()?;
    let error = r.string()?;
    if r.p != b.len() {
        return Err(());
    };
    Ok(Reply {
        text,
        number,
        flag,
        names,
        bytes,
        error: if has_error { Some(error) } else { None },
    })
}
fn bytes(o: &mut Vec<u8>, s: &str) {
    o.extend_from_slice(&(s.len() as u64).to_le_bytes());
    o.extend_from_slice(s.as_bytes())
}
fn encode(e: &Effect) -> Vec<u8> {
    let mut b = vec![];
    bytes(&mut b, &e.kind);
    bytes(&mut b, &e.text);
    bytes(&mut b, &e.other);
    b.extend_from_slice(&e.number.to_le_bytes());
    bytes(&mut b, &e.result);
    b
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
pub extern "C" fn smartvpn_privacy_log_version_v1() -> u32 {
    1
}
/// # Safety
/// Input and output pointers must reference caller-owned live memory of stated sizes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_privacy_log_start_v1(
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
        r.sessions.insert(
            id,
            SessionEntry {
                flow,
                step: 1,
                input_bytes: n,
            },
        );
        *out = id;
        0
    })
}
/// # Safety
/// Writable outputs must be valid, non-overlapping and suitably aligned.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_privacy_log_next_v1(
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
pub unsafe extern "C" fn smartvpn_privacy_log_advance_v1(
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
        if e.input_bytes.saturating_add(n) > 128 << 20 {
            return -4;
        }
        if e.flow.advance(reply).is_err() {
            return -2;
        }
        e.step += 1;
        e.input_bytes += n;
        0
    })
}
#[no_mangle]
pub extern "C" fn smartvpn_privacy_log_close_v1(id: u64) -> i32 {
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

#[cfg(test)]
mod tests {
    use super::*;
    fn request() -> Vec<u8> {
        let mut b = vec![];
        bytes(&mut b, "write");
        bytes(&mut b, "");
        b
    }
    fn reply() -> Vec<u8> {
        let mut b = vec![];
        bytes(&mut b, "");
        b.extend_from_slice(&0i64.to_le_bytes());
        b.push(0);
        b.extend_from_slice(&0u64.to_le_bytes());
        bytes(&mut b, "");
        b.push(0);
        bytes(&mut b, "");
        b
    }
    #[test]
    fn privacy_log_abi_truncation_trailing_and_invalid_bool() {
        let q = request();
        assert!(decode_start(&q).is_ok());
        for n in 0..q.len() {
            assert!(decode_start(&q[..n]).is_err())
        }
        let r = reply();
        assert!(decode_reply(&r).is_ok());
        for n in 0..r.len() {
            assert!(decode_reply(&r[..n]).is_err())
        }
        let mut bad = r.clone();
        bad.push(0);
        assert!(decode_reply(&bad).is_err());
        bad = r;
        bad[16] = 2;
        assert!(decode_reply(&bad).is_err())
    }
    #[test]
    fn privacy_log_abi_readonly_capacity_and_step_ack() {
        unsafe {
            let b = request();
            let mut id = 0;
            assert_eq!(
                smartvpn_privacy_log_start_v1(b.as_ptr(), b.len(), &mut id),
                0
            );
            let mut n = 0;
            let mut step = 0;
            assert_eq!(
                smartvpn_privacy_log_next_v1(id, std::ptr::null_mut(), 0, &mut n, &mut step),
                -3
            );
            assert_eq!(step, 1);
            let mut out = vec![0; n];
            assert_eq!(
                smartvpn_privacy_log_next_v1(id, out.as_mut_ptr(), out.len(), &mut n, &mut step),
                0
            );
            let expected = out.clone();
            assert_eq!(
                smartvpn_privacy_log_next_v1(id, out.as_mut_ptr(), out.len(), &mut n, &mut step),
                0
            );
            assert_eq!(out, expected);
            let r = reply();
            assert_eq!(
                smartvpn_privacy_log_advance_v1(id, 2, r.as_ptr(), r.len()),
                -5
            );
            assert_eq!(
                smartvpn_privacy_log_advance_v1(id, 1, r.as_ptr(), r.len()),
                0
            );
            assert_eq!(
                smartvpn_privacy_log_advance_v1(id, 1, r.as_ptr(), r.len()),
                -5
            );
            assert_eq!(smartvpn_privacy_log_close_v1(id), 0);
            assert_eq!(smartvpn_privacy_log_close_v1(id), -2)
        }
    }
    #[test]
    fn privacy_log_abi_null_and_size_rejected() {
        unsafe {
            let q = request();
            assert_eq!(
                smartvpn_privacy_log_start_v1(q.as_ptr(), q.len(), std::ptr::null_mut()),
                -1
            );
            let mut id = 9;
            assert_eq!(
                smartvpn_privacy_log_start_v1(std::ptr::null(), MAX_INPUT + 1, &mut id),
                -1
            );
            assert_eq!(id, 0)
        }
    }
}
