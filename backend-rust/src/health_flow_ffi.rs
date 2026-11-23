//! Session-scoped health orchestration ABI. next is read-only; advance is consumed once.
use crate::health_flow::{Effect, Flow, Mode, Node, Reply, Snapshot};
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
const MAX_INPUT: usize = 8 << 20;
const MAX_NODES: usize = 100_000;
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
    fn int(&mut self) -> Result<i64, ()> {
        Ok(i64::from_le_bytes(
            self.take(8)?.try_into().map_err(|_| ())?,
        ))
    }
    fn count(&mut self) -> Result<usize, ()> {
        let n = usize::try_from(self.int()?).map_err(|_| ())?;
        if n > MAX_NODES {
            Err(())
        } else {
            Ok(n)
        }
    }
    fn index(&mut self) -> Result<Option<usize>, ()> {
        match self.int()? {
            -1 => Ok(None),
            n if n >= 0 => Ok(Some(n as usize)),
            _ => Err(()),
        }
    }
    fn finish(&self) -> Result<(), ()> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(())
        }
    }
    fn delays(&mut self) -> Result<Vec<i64>, ()> {
        let n = self.count()?;
        (0..n).map(|_| self.int()).collect()
    }
}
fn decode_start(b: &[u8]) -> Result<Flow, ()> {
    let mut r = Reader { b, p: 0 };
    let n = r.count()?;
    let mut nodes = Vec::new();
    for _ in 0..n {
        nodes.push(Node {
            region_match: r.boolean()?,
            verified: r.boolean()?,
            region_fresh: r.boolean()?,
            cooldown_active: r.boolean()?,
            state: r.int()?,
            failures: r.int()?,
            successes: r.int()?,
            latency: r.int()?,
        });
    }
    let snapshot = Snapshot {
        nodes,
        active: r.index()?,
        chain: r.boolean()?,
        has_region: r.boolean()?,
        dwell_elapsed: r.boolean()?,
        blocked: r.boolean()?,
        failure_threshold: r.int()?,
        recovery_successes: r.int()?,
    };
    let mode = match r.byte()? {
        0 => Mode::Patrol {
            failed: r.boolean()?,
            delays: r.delays()?,
        },
        1 => Mode::Failover,
        2 => Mode::Block,
        3 => Mode::Unblock,
        _ => return Err(()),
    };
    r.finish()?;
    Flow::new(snapshot, mode).map_err(|_| ())
}
fn decode_reply(b: &[u8]) -> Result<Reply, ()> {
    let mut r = Reader { b, p: 0 };
    let v = match r.byte()? {
        0 => Reply::Ack,
        1 => Reply::Bool(r.boolean()?),
        2 => Reply::Delays {
            ok: r.boolean()?,
            values: r.delays()?,
        },
        _ => return Err(()),
    };
    r.finish()?;
    Ok(v)
}
fn integer(b: &mut Vec<u8>, v: i64) {
    b.extend_from_slice(&v.to_le_bytes());
}
fn index(b: &mut Vec<u8>, v: Option<usize>) {
    integer(b, v.map(|x| x as i64).unwrap_or(-1));
}
fn encode(e: Effect) -> Vec<u8> {
    let mut b = Vec::new();
    match e {
        Effect::ClearNote => b.push(1),
        Effect::RecordHealth {
            node,
            values,
            success,
            cooldown,
        } => {
            b.push(2);
            index(&mut b, Some(node));
            for v in values {
                integer(&mut b, v)
            }
            b.push(success as u8);
            b.push(cooldown as u8)
        }
        Effect::MeasureGroup => b.push(3),
        Effect::ConfirmTraffic => b.push(4),
        Effect::Switch { target } => {
            b.push(5);
            index(&mut b, target)
        }
        Effect::SaveSettings => b.push(6),
        Effect::Commit { kind, target } => {
            b.push(7);
            b.push(kind);
            index(&mut b, target)
        }
        Effect::AppendEvent {
            kind,
            from,
            target,
            delay,
        } => {
            b.push(8);
            b.push(kind);
            index(&mut b, from);
            index(&mut b, target);
            integer(&mut b, delay)
        }
        Effect::NoteAnomaly => b.push(9),
        Effect::Done => b.push(10),
    }
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
pub extern "C" fn smartvpn_health_flow_version_v1() -> u32 {
    1
}
/// # Safety
/// Input and output pointers must reference caller-owned live memory of stated sizes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_health_flow_start_v1(
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
pub unsafe extern "C" fn smartvpn_health_flow_next_v1(
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
pub unsafe extern "C" fn smartvpn_health_flow_advance_v1(
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
pub extern "C" fn smartvpn_health_flow_close_v1(id: u64) -> i32 {
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
mod abi_tests {
    use super::*;
    #[test]
    fn session_step_and_read_only_next_contract() {
        unsafe {
            let mut input = Vec::new();
            integer(&mut input, 1);
            input.extend_from_slice(&[1, 1, 1, 0]);
            for n in [1, 0, 0, 10] {
                integer(&mut input, n);
            }
            integer(&mut input, -1);
            input.extend_from_slice(&[0, 1, 1, 1]);
            integer(&mut input, 2);
            integer(&mut input, 3);
            input.push(3);
            let mut id = 0;
            assert_eq!(
                smartvpn_health_flow_start_v1(input.as_ptr(), input.len(), &mut id),
                0
            );
            let mut buf = [0; 128];
            let mut n = 0;
            let mut step = 0;
            assert_eq!(
                smartvpn_health_flow_next_v1(id, buf.as_mut_ptr(), 0, &mut n, &mut step),
                -3
            );
            assert_eq!(step, 1);
            assert_eq!(
                smartvpn_health_flow_next_v1(id, buf.as_mut_ptr(), 128, &mut n, &mut step),
                0
            );
            assert_eq!(buf[0], 5);
            assert_eq!(smartvpn_health_flow_advance_v1(id, 1, [0].as_ptr(), 1), -2); // wrong reply type
            assert_eq!(
                smartvpn_health_flow_advance_v1(id, 1, [1, 1].as_ptr(), 2),
                0
            );
            assert_eq!(
                smartvpn_health_flow_advance_v1(id, 1, [1, 1].as_ptr(), 2),
                -5
            );
            assert_eq!(
                smartvpn_health_flow_next_v1(id, buf.as_mut_ptr(), 128, &mut n, &mut step),
                0
            );
            assert_eq!(step, 2);
            assert_eq!(buf[0], 7);
            assert_eq!(smartvpn_health_flow_close_v1(id), 0);
            assert_eq!(smartvpn_health_flow_close_v1(id), -2);
            assert_eq!(
                smartvpn_health_flow_next_v1(id, buf.as_mut_ptr(), 128, &mut n, &mut step),
                -2
            );
            let mut fresh = 0;
            assert_eq!(
                smartvpn_health_flow_start_v1(input.as_ptr(), input.len(), &mut fresh),
                0
            );
            assert!(fresh > id);
            assert_eq!(smartvpn_health_flow_close_v1(fresh), 0);
            input.push(0);
            assert_eq!(
                smartvpn_health_flow_start_v1(input.as_ptr(), input.len(), &mut fresh),
                -2
            );
            assert_eq!(fresh, 0);
        }
    }
}
