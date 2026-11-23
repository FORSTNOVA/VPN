//! Persistent per-instance authority. Calls execute once; status/ack recover delivery failures.
use crate::service_state::{Ack, Command, Error, Snapshot, State};
use std::{
    collections::BTreeMap,
    panic::{catch_unwind, AssertUnwindSafe},
    sync::{Mutex, OnceLock},
};
const OUTPUT: usize = 32 << 20;
struct Registry {
    next: u64,
    states: BTreeMap<u64, State>,
}
fn registry() -> &'static Mutex<Registry> {
    static R: OnceLock<Mutex<Registry>> = OnceLock::new();
    R.get_or_init(|| {
        Mutex::new(Registry {
            next: 1,
            states: BTreeMap::new(),
        })
    })
}
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], Error> {
        let end = self.p.checked_add(n).ok_or(Error::Invalid)?;
        let v = self.b.get(self.p..end).ok_or(Error::Invalid)?;
        self.p = end;
        Ok(v)
    }
    fn u8(&mut self) -> Result<u8, Error> {
        Ok(self.take(1)?[0])
    }
    fn u64(&mut self) -> Result<u64, Error> {
        Ok(u64::from_le_bytes(self.take(8)?.try_into().unwrap()))
    }
    fn boolean(&mut self) -> Result<bool, Error> {
        match self.u8()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(Error::Invalid),
        }
    }
    fn bytes(&mut self) -> Result<Vec<u8>, Error> {
        let n = usize::try_from(self.u64()?).map_err(|_| Error::Invalid)?;
        if n > 8 << 20 {
            return Err(Error::Invalid);
        };
        Ok(self.take(n)?.to_vec())
    }
    fn command(&mut self) -> Result<Command, Error> {
        Ok(Command {
            epoch: self.u64()?,
            id: self.u64()?,
            expected_revision: self.u64()?,
            kind: self.u8()?,
            has_baseline: self.boolean()?,
            time_token: self.u64()?,
            up: self.u64()? as i64,
            down: self.u64()? as i64,
            sampling_error: self.bytes()?,
            path_payload: self.bytes()?,
            health_payload: Vec::new(),
            control_payload: Vec::new(),
            catalog_payload: Vec::new(),
            china_payload: Vec::new(),
        })
    }
    fn ack(&mut self) -> Result<Ack, Error> {
        Ok(Ack {
            epoch: self.u64()?,
            id: self.u64()?,
            revision: self.u64()?,
        })
    }
    fn done(&self) -> Result<(), Error> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(Error::Invalid)
        }
    }
}
fn n(b: &mut Vec<u8>, v: u64) {
    b.extend(v.to_le_bytes())
}
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    n(b, v.len() as u64);
    b.extend(v)
}
fn snapshot(b: &mut Vec<u8>, s: &Snapshot) {
    n(b, s.epoch);
    n(b, s.revision);
    b.push(u8::from(s.has_baseline));
    n(b, s.time_token);
    n(b, s.up as u64);
    n(b, s.down as u64);
    bytes(b, &s.sampling_error);
    bytes(b, &s.path_payload)
}
fn command(b: &mut Vec<u8>, s: &Command) {
    n(b, s.epoch);
    n(b, s.id);
    n(b, s.expected_revision);
    b.push(s.kind);
    b.push(u8::from(s.has_baseline));
    n(b, s.time_token);
    n(b, s.up as u64);
    n(b, s.down as u64);
    bytes(b, &s.sampling_error);
    bytes(b, &s.path_payload)
}
fn ack(b: &mut Vec<u8>, s: &Ack) {
    n(b, s.epoch);
    n(b, s.id);
    n(b, s.revision)
}
fn dispatch(s: &mut State, op: u8, input: &[u8]) -> Result<Vec<u8>, Error> {
    let mut r = Reader { b: input, p: 0 };
    let mut out = Vec::new();
    match op {
        1 => {
            r.done()?;
            snapshot(&mut out, &s.meta()?);
        }
        2 => {
            let elapsed = f64::from_bits(r.u64()?);
            let up = r.u64()? as i64;
            let down = r.u64()? as i64;
            r.done()?;
            let v = s.rate(elapsed, up, down)?;
            n(&mut out, v.up as u64);
            n(&mut out, v.down as u64);
            out.push(u8::from(v.supported));
        }
        3 => {
            let c = r.command()?;
            r.done()?;
            if c.kind >= 4 {
                return Err(Error::Invalid);
            }
            ack(&mut out, &s.apply(c)?);
        }
        4 => {
            let c = r.command()?;
            r.done()?;
            if c.kind >= 4 {
                return Err(Error::Invalid);
            }
            let v = s.status(&c)?;
            out.push(u8::from(v.found));
            if v.found {
                command(&mut out, &v.command);
                ack(&mut out, &v.ack);
                out.push(u8::from(v.pending));
            }
        }
        5 => {
            r.done()?;
            let cp = s.export()?;
            snapshot(&mut out, &cp.snapshot);
            out.push(u8::from(cp.has_last));
            if cp.has_last {
                command(&mut out, &cp.last_command);
                ack(&mut out, &cp.last_ack)
            }
            out.push(u8::from(cp.pending));
        }
        6 => {
            let a = r.ack()?;
            r.done()?;
            s.acknowledge(a)?;
        }
        _ => return Err(Error::Invalid),
    };
    Ok(out)
}
#[unsafe(no_mangle)]
pub extern "C" fn smartvpn_service_state_version_v2() -> u32 {
    2
}
/// # Safety
/// `output` must reference writable u64 storage.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn smartvpn_service_state_open_v2(output: *mut u64) -> i32 {
    catch_unwind(AssertUnwindSafe(|| {
        if output.is_null() {
            return -1;
        }
        unsafe { *output = 0 };
        let Ok(mut r) = registry().lock() else {
            return -2;
        };
        if r.states.len() >= 64 || r.next == u64::MAX {
            return -3;
        };
        let id = r.next;
        r.next += 1;
        let Ok(s) = State::new(Snapshot {
            epoch: id,
            ..Snapshot::default()
        }) else {
            return -4;
        };
        r.states.insert(id, s);
        unsafe { *output = id };
        0
    }))
    .unwrap_or(-99)
}
/// # Safety
/// Input and output spans must be valid, disjoint, and writable as specified.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn smartvpn_service_state_call_v2(
    id: u64,
    op: u8,
    input: *const u8,
    len: usize,
    output: *mut u8,
    cap: usize,
    written: *mut usize,
) -> i32 {
    catch_unwind(AssertUnwindSafe(|| {
        if written.is_null() {
            return -1;
        }
        unsafe { *written = 0 };
        if len > 16 << 20
            || (len > 0 && input.is_null())
            || (cap > 0 && output.is_null())
            || (op == 3 && cap < 24)
        {
            return -1;
        };
        let data = if len == 0 {
            &[]
        } else {
            unsafe { std::slice::from_raw_parts(input, len) }
        };
        let Ok(mut r) = registry().lock() else {
            return -2;
        };
        let Some(s) = r.states.get_mut(&id) else {
            return -3;
        };
        let result = match dispatch(s, op, data) {
            Ok(v) => v,
            Err(_) => return -4,
        };
        if result.len() > OUTPUT {
            return -5;
        }
        if result.len() > cap {
            unsafe { *written = result.len() };
            return -6;
        };
        unsafe {
            if !result.is_empty() {
                std::ptr::copy_nonoverlapping(result.as_ptr(), output, result.len());
            }
            *written = result.len()
        };
        0
    }))
    .unwrap_or(-99)
}
#[unsafe(no_mangle)]
pub extern "C" fn smartvpn_service_state_close_v2(id: u64) -> i32 {
    catch_unwind(AssertUnwindSafe(|| {
        let Ok(mut r) = registry().lock() else {
            return -2;
        };
        let Some(mut s) = r.states.remove(&id) else {
            return -3;
        };
        if s.close().is_err() {
            return -4;
        };
        0
    }))
    .unwrap_or(-99)
}

#[unsafe(no_mangle)]
pub extern "C" fn smartvpn_service_state_capabilities_v2() -> u64 {
    3
}
