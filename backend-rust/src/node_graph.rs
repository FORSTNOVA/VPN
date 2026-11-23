// App-owned manual records and named chains. The legacy view exposes metadata only.
use std::{collections::BTreeMap, slice, sync::Mutex};

#[derive(Default)]
struct Graph {
    manual: Option<Vec<ManualRecord>>,
    undo_manual: Option<Option<Vec<ManualRecord>>>,
    chain_undo: Option<(Vec<u8>, Option<BTreeMap<Vec<u8>, Vec<u8>>>)>,
    active: Vec<u8>,
    profiles: Option<BTreeMap<Vec<u8>, Vec<u8>>>,
}

fn take<'a>(buf: &mut &'a [u8]) -> Option<&'a [u8]> {
    let (size, rest) = buf.split_at_checked(8)?;
    let n = usize::try_from(u64::from_le_bytes(size.try_into().ok()?)).ok()?;
    let (field, tail) = rest.split_at_checked(n)?;
    *buf = tail;
    Some(field)
}
fn field(out: &mut Vec<u8>, bytes: &[u8]) {
    out.extend_from_slice(&(bytes.len() as u64).to_le_bytes());
    out.extend_from_slice(bytes);
}

#[derive(Clone)]
struct ManualRecord {
    metadata: Vec<u8>,
    resource: Vec<u8>,
}

impl ManualRecord {
    fn read(mut bytes: &[u8], full: bool) -> Option<Self> {
        let metadata = if full { take(&mut bytes)? } else { bytes };
        Some(Self {
            metadata: metadata.to_vec(),
            resource: if full { bytes.to_vec() } else { Vec::new() },
        })
    }
}

impl Graph {
    fn update(&mut self, op: usize, idx: usize, mut b: &[u8]) -> Option<()> {
        match op {
            1 | 13 => {
                let (&present, mut tail) = b.split_first()?;
                if present > 1 || (present == 0 && !tail.is_empty()) {
                    return None;
                }
                let mut rows = Vec::new();
                while !tail.is_empty() {
                    rows.push(ManualRecord::read(take(&mut tail)?, op == 13)?)
                }
                self.manual = (present == 1).then_some(rows);
            }
            3 | 15 => {
                let row = ManualRecord::read(b, op == 15)?;
                self.manual.get_or_insert_with(Vec::new).push(row);
            }
            5 => {
                if idx > self.manual.as_ref().map_or(0, Vec::len) {
                    return None;
                }
                if let Some(rows) = &mut self.manual {
                    rows.truncate(idx);
                }
            }
            6 => {
                let active = take(&mut b)?;
                let (&present, mut tail) = b.split_first()?;
                if present > 1 || (present == 0 && !tail.is_empty()) {
                    return None;
                }
                let mut profiles = BTreeMap::new();
                while !tail.is_empty() {
                    let key = take(&mut tail)?;
                    profiles.insert(key.to_vec(), take(&mut tail)?.to_vec());
                }
                self.active = active.to_vec();
                self.profiles = (present == 1).then_some(profiles);
            }
            8 => self.active = b.to_vec(),
            9 => {
                let old = take(&mut b)?;
                let name = take(&mut b)?;
                let profiles = self.profiles.get_or_insert_with(BTreeMap::new);
                profiles.remove(old);
                profiles.insert(name.to_vec(), b.to_vec());
            }
            20 => {
                if self.chain_undo.is_some() {
                    return None;
                }
                self.chain_undo = Some((self.active.clone(), self.profiles.clone()));
            }
            21 => {
                (self.active, self.profiles) = self.chain_undo.take()?;
            }
            22 => {
                self.chain_undo.take()?;
            }
            17 => {
                self.manual = self.undo_manual.take()?;
            }
            18 => {
                self.undo_manual = None;
            }
            19 => {
                let row = ManualRecord::read(b, true)?;
                *self.manual.as_mut()?.get_mut(idx)? = row;
            }
            10 | 16 => {
                if op == 16 && self.undo_manual.is_some() {
                    return None;
                }
                let rows = self.manual.as_deref().unwrap_or_default();
                let mut kept = Vec::with_capacity(b.len() / 8);
                while !b.is_empty() {
                    let (word, rest) = b.split_at_checked(8)?;
                    let i = usize::try_from(u64::from_le_bytes(word.try_into().ok()?)).ok()?;
                    kept.push(rows.get(i)?.clone());
                    b = rest;
                }
                let previous = self.manual.replace(kept);
                if op == 16 {
                    self.undo_manual = Some(previous);
                }
            }
            _ => return None,
        }
        Some(())
    }
    fn snapshot_len(&self, op: usize) -> usize {
        if op == 2 || op == 14 {
            return 1 + self
                .manual
                .iter()
                .flatten()
                .map(|r| 8 + r.metadata.len() + if op == 14 { 8 + r.resource.len() } else { 0 })
                .sum::<usize>();
        }
        9 + self.active.len()
            + self
                .profiles
                .iter()
                .flatten()
                .map(|(name, hops)| 16 + name.len() + hops.len())
                .sum::<usize>()
    }
    fn snapshot(&self, op: usize) -> Vec<u8> {
        let mut out = Vec::with_capacity(self.snapshot_len(op));
        if op == 2 || op == 14 {
            out.push(self.manual.is_some() as u8);
            if let Some(rows) = &self.manual {
                for row in rows {
                    if op == 14 {
                        out.extend_from_slice(
                            &((8 + row.metadata.len() + row.resource.len()) as u64).to_le_bytes(),
                        );
                    }
                    field(&mut out, &row.metadata);
                    if op == 14 {
                        out.extend_from_slice(&row.resource)
                    }
                }
            }
        } else {
            field(&mut out, &self.active);
            out.push(self.profiles.is_some() as u8);
            if let Some(profiles) = &self.profiles {
                for (k, hops) in profiles {
                    field(&mut out, k);
                    field(&mut out, hops)
                }
            }
        }
        out
    }
}

// The app lock spans size/read, correlated Go map references and rollback.
// All handles are exclusive to one app and are destroyed after its last call.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_node_graph_v1(
    op: usize,
    handle: usize,
    index: usize,
    data: *mut u8,
    len: usize,
) -> usize {
    if op == 0 {
        return Box::into_raw(Box::new(Mutex::new(Graph::default()))) as usize;
    }
    let raw = handle as *mut Mutex<Graph>;
    if raw.is_null() || len > isize::MAX as usize || (len != 0 && data.is_null()) {
        return usize::MAX;
    }
    if op == 4 {
        drop(Box::from_raw(raw));
        return 0;
    }
    let mut state = (*raw).lock().unwrap();
    if op == 11 {
        return state.manual.as_ref().map_or(0, Vec::len);
    }
    if op == 12 {
        let mut active = state.active.as_slice();
        if take(&mut active).is_none() {
            return usize::MAX;
        }
        return active.first().map_or(usize::MAX, |v| usize::from(*v));
    }
    if op == 2 || op == 7 || op == 14 {
        let size = state.snapshot_len(op);
        if len < size {
            return size;
        }
        let bytes = state.snapshot(op);
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), data, bytes.len());
        return bytes.len();
    }
    let input = if len == 0 {
        &[]
    } else {
        slice::from_raw_parts(data, len)
    };
    state.update(op, index, input).map_or(usize::MAX, |()| 0)
}

/// Capability marker for full manual records on the existing per-app graph.
#[no_mangle]
pub extern "C" fn smartvpn_manual_record_version_v1() -> u32 {
    1
}

#[no_mangle]
pub extern "C" fn smartvpn_chain_transaction_version_v1() -> u32 {
    1
}
