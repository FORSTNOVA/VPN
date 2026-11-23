//! Execute-once result ownership. No arbitrary SQL operation is exported.
use crate::{
    wire::{self, Reply},
    Store,
};
use std::{
    collections::HashMap,
    panic::{catch_unwind, AssertUnwindSafe},
    path::PathBuf,
    sync::{Arc, Mutex, OnceLock},
};
const MAX_STORES: usize = 64;
const MAX_RESULTS: usize = 256;
const MAX_INPUT: usize = 8 << 20;
const MAX_RESULT: usize = 16 << 20;
const MAX_TOTAL: usize = 64 << 20;
struct StoreEntry {
    store: Arc<Store>,
    claimed: bool,
}
struct ResultEntry {
    bytes: Vec<u8>,
    pending: Option<u64>,
}
struct Registry {
    next: u64,
    stores: HashMap<u64, StoreEntry>,
    results: HashMap<u64, ResultEntry>,
    bytes: usize,
}
impl Registry {
    fn new() -> Self {
        Self {
            next: 1,
            stores: HashMap::new(),
            results: HashMap::new(),
            bytes: 0,
        }
    }
    fn reserve(&self, ids: u64) -> bool {
        self.results.len() < MAX_RESULTS
            && self.bytes <= MAX_TOTAL - MAX_RESULT
            && self.next.checked_add(ids).is_some()
    }
    fn id(&mut self) -> u64 {
        let id = self.next;
        self.next += 1;
        id
    }
    fn result(&mut self, reply: Reply, pending: Option<u64>) -> u64 {
        let reply = if reply.payload.len() > MAX_RESULT - 20 {
            Reply {
                category: 4,
                sqlcode: 0,
                outcome: 2,
                payload: Vec::new(),
            }
        } else {
            reply
        };
        let mut bytes = Vec::with_capacity(20 + reply.payload.len());
        bytes.extend_from_slice(&reply.category.to_le_bytes());
        bytes.extend_from_slice(&reply.sqlcode.to_le_bytes());
        bytes.extend_from_slice(&reply.outcome.to_le_bytes());
        bytes.extend_from_slice(&(reply.payload.len() as u64).to_le_bytes());
        bytes.extend_from_slice(&reply.payload);
        let id = self.id();
        self.bytes += bytes.len();
        self.results.insert(id, ResultEntry { bytes, pending });
        id
    }
}
static REGISTRY: OnceLock<Mutex<Registry>> = OnceLock::new();
fn registry() -> &'static Mutex<Registry> {
    REGISTRY.get_or_init(|| Mutex::new(Registry::new()))
}
fn guard(f: impl FnOnce() -> i32) -> i32 {
    catch_unwind(AssertUnwindSafe(f)).unwrap_or(-9)
}
fn ok() -> Reply {
    Reply {
        category: 0,
        sqlcode: 0,
        outcome: 1,
        payload: Vec::new(),
    }
}
fn failed(error: &crate::Error, outcome: u32) -> Reply {
    let (category, sqlcode) = match error {
        crate::Error::Sql(rusqlite::Error::SqliteFailure(e, _)) => (2, e.extended_code as u32),
        crate::Error::Sql(_) => (2, 0),
        crate::Error::Closed | crate::Error::Poisoned => (3, 0),
        _ => (4, 0),
    };
    Reply {
        category,
        sqlcode,
        outcome,
        payload: Vec::new(),
    }
}
fn aligned<T>(p: *const T) -> bool {
    !p.is_null() && (p as usize).is_multiple_of(std::mem::align_of::<T>())
}
#[no_mangle]
pub extern "C" fn smartvpn_store_version_v1() -> u32 {
    1
}
// One capability bit for each implemented opcode 0..15. Not production enablement.
#[no_mangle]
pub extern "C" fn smartvpn_store_capabilities_v1() -> u64 {
    0xffff
}

/// # Safety
/// path readable/aligned for len UTF16 units; output pointers valid, writable,
/// aligned and distinct from all input ranges. No pointer is retained.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_store_open_v1(
    path: *const u16,
    len: usize,
    store: *mut u64,
    result: *mut u64,
) -> i32 {
    guard(|| {
        if !aligned(store) || !aligned(result) || store == result {
            return -1;
        }
        *store = 0;
        *result = 0;
        if len == 0 || len > 32767 || !aligned(path) {
            return -1;
        }
        let units = std::slice::from_raw_parts(path, len);
        if units.contains(&0) {
            return -1;
        }
        #[cfg(windows)]
        let path = {
            use std::os::windows::ffi::OsStringExt;
            PathBuf::from(std::ffi::OsString::from_wide(units))
        };
        #[cfg(not(windows))]
        let path = match String::from_utf16(units) {
            Ok(p) => PathBuf::from(p),
            Err(_) => return -1,
        };
        let Ok(mut r) = registry().lock() else {
            return -8;
        };
        if r.stores.len() >= MAX_STORES || !r.reserve(2) {
            return -3;
        }
        match Store::open(path) {
            Ok(opened) => {
                let id = r.id();
                r.stores.insert(
                    id,
                    StoreEntry {
                        store: Arc::new(opened),
                        claimed: false,
                    },
                );
                *result = r.result(ok(), Some(id));
                *store = id;
            }
            Err(e) => {
                *result = r.result(failed(&e, 2), None);
            }
        };
        0
    })
}
/// # Safety
/// Handles must originate from this library instance.
#[no_mangle]
pub extern "C" fn smartvpn_store_claim_v1(store: u64, result: u64) -> i32 {
    guard(|| {
        let Ok(mut r) = registry().lock() else {
            return -8;
        };
        if r.results.get(&result).and_then(|v| v.pending) != Some(store) {
            return -4;
        }
        let Some(entry) = r.stores.get_mut(&store) else {
            return -4;
        };
        entry.claimed = true;
        r.results.get_mut(&result).unwrap().pending = None;
        0
    })
}
/// # Safety
/// input readable for len; result valid/writable/aligned, disjoint from input.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_store_call_v1(
    store: u64,
    op: u32,
    input: *const u8,
    len: usize,
    result: *mut u64,
) -> i32 {
    guard(|| {
        if !aligned(result) {
            return -1;
        }
        *result = 0;
        if len > MAX_INPUT || (len != 0 && input.is_null()) {
            return -1;
        }
        let data = if len == 0 {
            &[]
        } else {
            std::slice::from_raw_parts(input, len)
        };
        let Ok(mut r) = registry().lock() else {
            return -8;
        };
        if !r.reserve(1) {
            return -3;
        }
        let Some(entry) = r.stores.get(&store) else {
            return -4;
        };
        if !entry.claimed {
            return -4;
        }
        let owned = Arc::clone(&entry.store);
        // Registry lock reserves capacity and serializes close through the whole call.
        let reply = wire::execute(&owned, op, data);
        *result = r.result(reply, None);
        0
    })
}
/// # Safety
/// result valid/writable/aligned.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_store_close_v1(store: u64, result: *mut u64) -> i32 {
    guard(|| {
        if !aligned(result) {
            return -1;
        }
        *result = 0;
        let Ok(mut r) = registry().lock() else {
            return -8;
        };
        if !r.reserve(1) {
            return -3;
        }
        let Some(entry) = r.stores.get(&store) else {
            return -4;
        };
        if !entry.claimed {
            return -4;
        }
        let owned = Arc::clone(&entry.store);
        let reply = match owned.close() {
            Ok(()) => {
                r.stores.remove(&store);
                ok()
            }
            Err(e) => failed(&e, 2),
        };
        *result = r.result(reply, None);
        0
    })
}
/// # Safety
/// size valid/writable/aligned.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_store_result_size_v1(result: u64, size: *mut usize) -> i32 {
    guard(|| {
        if !aligned(size) {
            return -1;
        }
        *size = 0;
        let Ok(r) = registry().lock() else {
            return -8;
        };
        let Some(value) = r.results.get(&result) else {
            return -4;
        };
        *size = value.bytes.len();
        0
    })
}
/// # Safety
/// output writable for capacity; written valid/writable/aligned, disjoint.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_store_result_read_v1(
    result: u64,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    guard(|| {
        if !aligned(written) {
            return -1;
        }
        *written = 0;
        if capacity > 0 && output.is_null() {
            return -1;
        }
        let Ok(r) = registry().lock() else {
            return -8;
        };
        let Some(value) = r.results.get(&result) else {
            return -4;
        };
        *written = value.bytes.len();
        if capacity < value.bytes.len() {
            return -2;
        }
        std::ptr::copy_nonoverlapping(value.bytes.as_ptr(), output, value.bytes.len());
        0
    })
}
#[no_mangle]
pub extern "C" fn smartvpn_store_result_free_v1(result: u64) -> i32 {
    guard(|| {
        let Ok(mut r) = registry().lock() else {
            return -8;
        };
        let Some(value) = r.results.get(&result) else {
            return -4;
        };
        if let Some(id) = value.pending {
            if let Some(entry) = r.stores.get(&id) {
                if entry.store.close().is_err() {
                    return -5;
                }
            }
            r.stores.remove(&id);
        }
        let value = r.results.remove(&result).unwrap();
        r.bytes -= value.bytes.len();
        0
    })
}
