use std::{collections::HashMap, ptr, sync::Mutex};

type Quotas = Mutex<HashMap<usize, [u8; 32]>>;

// Each payload contains upload, download, total and expiry as little-endian i64.
// The caller owns the handle and serializes destruction with outstanding calls.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_catalog_quota_v1(
    op: usize,
    handle: usize,
    id: usize,
    data: *mut u8,
    len: usize,
) -> usize {
    if op == 0 {
        return Box::into_raw(Box::new(Quotas::default())) as usize;
    }
    let raw = handle as *mut Quotas;
    if raw.is_null() {
        return usize::MAX;
    }
    if op == 4 {
        drop(Box::from_raw(raw));
        return 0;
    }
    if id == 0 {
        return usize::MAX;
    }
    let Ok(mut quotas) = (*raw).lock() else {
        return usize::MAX;
    };
    match op {
        1 if len == 32 && !data.is_null() => {
            let mut value = [0; 32];
            ptr::copy_nonoverlapping(data, value.as_mut_ptr(), 32);
            quotas.insert(id, value);
            0
        }
        2 if len == 32 && !data.is_null() => {
            let Some(value) = quotas.get(&id) else {
                return usize::MAX;
            };
            ptr::copy_nonoverlapping(value.as_ptr(), data, 32);
            32
        }
        3 => {
            quotas.remove(&id);
            0
        }
        _ => usize::MAX,
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn quotas_are_isolated_and_reject_partial_updates() {
        unsafe {
            let null = ptr::null_mut();
            let a = smartvpn_catalog_quota_v1(0, 0, 0, null, 0);
            let b = smartvpn_catalog_quota_v1(0, 0, 0, null, 0);
            let mut data = [127u8; 32];
            let p = data.as_mut_ptr();
            assert_eq!(smartvpn_catalog_quota_v1(1, a, 1, p, 32), 0);
            data.fill(0);
            assert_eq!(smartvpn_catalog_quota_v1(1, a, 1, p, 31), usize::MAX);
            assert_eq!(smartvpn_catalog_quota_v1(2, b, 1, p, 32), usize::MAX);
            assert_eq!(smartvpn_catalog_quota_v1(2, a, 1, p, 32), 32);
            assert_eq!(data, [127; 32]);
            assert_eq!(smartvpn_catalog_quota_v1(3, a, 1, null, 0), 0);
            assert_eq!(smartvpn_catalog_quota_v1(2, a, 1, p, 32), usize::MAX);
            for h in [a, b] {
                smartvpn_catalog_quota_v1(4, h, 0, null, 0);
            }
        }
    }
}
