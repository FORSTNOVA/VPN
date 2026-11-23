// Per-app cache. Strings on the wire are length-prefixed bytes, so Go strings
// round-trip even if a subscription supplied non-UTF-8 data.
use std::{slice, sync::Mutex};

struct Row {
    label: Vec<u8>,
    // Three length-prefixed strings (name, type, network) and the flag byte.
    details: Vec<u8>,
}

#[derive(Default)]
struct Cache {
    present: bool,
    rows: Vec<Row>,
    wire_len: usize,
}

fn field<'a>(input: &mut &'a [u8]) -> Option<&'a [u8]> {
    let (prefix, tail) = input.split_at_checked(8)?;
    let len = usize::try_from(u64::from_le_bytes(prefix.try_into().ok()?)).ok()?;
    let (value, rest) = tail.split_at_checked(len)?;
    *input = rest;
    Some(value)
}

impl Cache {
    fn decode(input: &[u8]) -> Option<Self> {
        let (&shape, mut tail) = input.split_first()?;
        if shape > 1 || (shape == 0 && !tail.is_empty()) {
            return None;
        }
        let mut rows = Vec::new();
        while !tail.is_empty() {
            let label = field(&mut tail)?;
            let details = tail;
            for _ in 0..3 {
                field(&mut tail)?;
            }
            let (&flags, rest) = tail.split_first()?;
            if flags & !63 != 0 {
                return None;
            }
            rows.push(Row {
                label: label.to_vec(),
                details: details[..details.len() - rest.len()].to_vec(),
            });
            tail = rest;
        }
        Some(Self {
            present: shape == 1,
            rows,
            wire_len: input.len(),
        })
    }

    fn write(&self, out: &mut [u8]) {
        out[0] = u8::from(self.present);
        let mut pos = 1;
        for row in &self.rows {
            for part in [
                &(row.label.len() as u64).to_le_bytes()[..],
                &row.label,
                &row.details,
            ] {
                out[pos..pos + part.len()].copy_from_slice(part);
                pos += part.len();
            }
        }
    }
}

// Ops: create, replace, read, update one label, destroy. The caller owns the
// handle and excludes destruction while any call is active. Each replace
// publishes only after parsing the entire input. A short read returns the
// required length without writing any bytes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_cached_nodes_v1(
    op: usize,
    handle: usize,
    index: usize,
    data: *mut u8,
    len: usize,
) -> usize {
    if op == 0 {
        return Box::into_raw(Box::new(Mutex::new(Cache {
            wire_len: 1,
            ..Cache::default()
        }))) as usize;
    }
    let raw = handle as *mut Mutex<Cache>;
    if raw.is_null() || (len != 0 && data.is_null()) || len > isize::MAX as usize {
        return usize::MAX;
    }
    if op == 4 {
        drop(Box::from_raw(raw));
        return 0;
    }
    let store = &*raw;
    let input = || {
        if len == 0 {
            &[]
        } else {
            slice::from_raw_parts(data, len)
        }
    };
    match op {
        1 => {
            let Some(next) = Cache::decode(input()) else {
                return usize::MAX;
            };
            *store.lock().unwrap() = next;
        }
        2 => {
            let cache = store.lock().unwrap();
            if len >= cache.wire_len {
                cache.write(slice::from_raw_parts_mut(data, cache.wire_len));
            }
            return cache.wire_len;
        }
        3 => {
            let mut cache = store.lock().unwrap();
            let Some(old) = cache.rows.get(index) else {
                return usize::MAX;
            };
            let Some(size) = (cache.wire_len - old.label.len()).checked_add(len) else {
                return usize::MAX;
            };
            cache.rows[index].label = input().to_vec();
            cache.wire_len = size;
        }
        _ => return usize::MAX,
    }
    0
}
