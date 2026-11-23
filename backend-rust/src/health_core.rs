//! Pure health transitions. Time, identity, probing and policy side effects stay in Go.
pub fn normalize(v: [i64; 6]) -> [i64; 6] {
    let ranges = [
        (1000, 15000),
        (10, 600),
        (1, 10),
        (10, 3600),
        (0, 600),
        (1, 10),
    ];
    std::array::from_fn(|i| v[i].clamp(ranges[i].0, ranges[i].1))
}
/// Input: prior state (0=other,1=cooldown,2=recovery), failures, successes,
/// observed latency, success flag, cooldown-active flag, failure/recovery thresholds.
/// Output: state (1 healthy,2 suspect,3 unhealthy,4 cooldown,5 recovery), counters, latency.
pub fn transition(v: [i64; 8]) -> Option<[i64; 4]> {
    if !(0..=2).contains(&v[0]) || !(0..=1).contains(&v[4]) || !(0..=1).contains(&v[5]) {
        return None;
    }
    if v[4] == 1 {
        let successes = v[2].wrapping_add(1);
        let state = if v[5] == 1 {
            4
        } else if v[0] != 0 && successes < v[7] {
            5
        } else {
            1
        };
        Some([state, 0, successes, v[3]])
    } else {
        let failures = v[1].wrapping_add(1);
        let state = if v[5] == 1 {
            4
        } else if failures >= v[6] {
            3
        } else {
            2
        };
        Some([state, failures, 0, 0])
    }
}
/// # Safety
/// input/output are aligned readable/writable arrays of 6 i64s. Buffers may overlap.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_health_normalize_v1(input: *const i64, output: *mut i64) -> i32 {
    if input.is_null() || output.is_null() {
        return -1;
    }
    let v: [i64; 6] = unsafe { std::slice::from_raw_parts(input, 6) }
        .try_into()
        .unwrap();
    let result = normalize(v);
    unsafe {
        std::ptr::copy_nonoverlapping(result.as_ptr(), output, 6);
    }
    0
}
/// # Safety
/// input is 8 aligned readable i64s; output is 4 writable i64s. May overlap.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_health_transition_v1(input: *const i64, output: *mut i64) -> i32 {
    if input.is_null() || output.is_null() {
        return -1;
    }
    let v: [i64; 8] = unsafe { std::slice::from_raw_parts(input, 8) }
        .try_into()
        .unwrap();
    let Some(result) = transition(v) else {
        return -1;
    };
    unsafe {
        std::ptr::copy_nonoverlapping(result.as_ptr(), output, 4);
    }
    0
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn clamps() {
        assert_eq!(normalize([i64::MIN; 6]), [1000, 10, 1, 10, 0, 1]);
        assert_eq!(normalize([i64::MAX; 6]), [15000, 600, 10, 3600, 600, 10]);
    }
    #[test]
    fn transitions() {
        assert_eq!(transition([1, 0, 1, 50, 1, 1, 2, 3]), Some([4, 0, 2, 50]));
        assert_eq!(transition([1, 0, 1, 50, 1, 0, 2, 3]), Some([5, 0, 2, 50]));
        assert_eq!(transition([2, 0, 2, 50, 1, 0, 2, 3]), Some([1, 0, 3, 50]));
        assert_eq!(transition([0, 0, 0, 50, 0, 0, 2, 3]), Some([2, 1, 0, 0]));
        assert_eq!(transition([0, 1, 0, 50, 0, 0, 2, 3]), Some([3, 2, 0, 0]));
        assert_eq!(
            transition([0, i64::MAX, 0, 50, 0, 0, 2, 3]),
            Some([2, i64::MIN, 0, 0])
        );
    }
    #[test]
    fn invalid_abi() {
        unsafe {
            assert_eq!(
                smartvpn_health_normalize_v1(std::ptr::null(), std::ptr::null_mut()),
                -1
            );
            let mut out = [99; 4];
            assert_eq!(
                smartvpn_health_transition_v1([0, 0, 0, 0, 2, 0, 1, 1].as_ptr(), out.as_mut_ptr()),
                -1
            );
            assert_eq!(out, [99; 4]);
        }
    }
}
