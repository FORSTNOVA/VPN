//! Traffic history counters and UI ranking. No clock, database or network I/O.
pub fn counter_delta(current: i64, previous: i64) -> i64 {
    if current < 0 {
        0
    } else if current < previous {
        current
    } else {
        current.wrapping_sub(previous)
    }
}
pub fn rates(
    previous_up: i64,
    previous_down: i64,
    current_up: i64,
    current_down: i64,
    elapsed_ns: i64,
    first: bool,
) -> Option<(i64, i64)> {
    if first || elapsed_ns <= 0 || current_up < previous_up || current_down < previous_down {
        return Some((0, 0));
    }
    let seconds = (elapsed_ns / 1_000_000_000) as f64 + (elapsed_ns % 1_000_000_000) as f64 / 1e9;
    let convert = |value: i64| {
        let value = value as f64 / seconds;
        // Rust float-to-int saturation differs from Go on out-of-range values.
        // Keep that platform-specific conversion with the reference backend.
        if value >= i64::MIN as f64 && value < 9223372036854775808.0 {
            Some(value as i64)
        } else {
            None
        }
    };
    Some((
        convert(current_up.wrapping_sub(previous_up))?,
        convert(current_down.wrapping_sub(previous_down))?,
    ))
}
pub fn ranking(totals: &[i64]) -> Vec<usize> {
    let mut indices: Vec<usize> = (0..totals.len()).collect();
    indices.sort_by_key(|&i| std::cmp::Reverse(totals[i]));
    indices
}
/// # Safety
/// input references 7 i64s: previous up/down, current up/down, elapsed ns,
/// first sample (0/1), operation (0=delta current_up/previous_up, 1=rates).
/// output references two writable i64s. Returns -1 for unsupported input.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_traffic_values_v1(input: *const i64, output: *mut i64) -> i32 {
    if input.is_null() || output.is_null() {
        return -1;
    }
    let v = unsafe { std::slice::from_raw_parts(input, 7) };
    let result = match v[6] {
        0 => Some((counter_delta(v[2], v[0]), 0)),
        1 if v[5] == 0 || v[5] == 1 => rates(v[0], v[1], v[2], v[3], v[4], v[5] != 0),
        _ => None,
    };
    let Some((up, down)) = result else {
        return -1;
    };
    unsafe {
        *output = up;
        *output.add(1) = down;
    }
    0
}
/// # Safety
/// totals references len readable i64s, output len writable size_t indices.
/// Null buffers allowed only for zero length; maximum 65536 rows.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_traffic_ranking_v1(
    totals: *const i64,
    len: usize,
    output: *mut usize,
) -> i32 {
    if len > 65536 || (len != 0 && (totals.is_null() || output.is_null())) {
        return -1;
    }
    if len == 0 {
        return 0;
    }
    let result = ranking(unsafe { std::slice::from_raw_parts(totals, len) });
    unsafe {
        std::ptr::copy_nonoverlapping(result.as_ptr(), output, len);
    }
    0
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn counters() {
        assert_eq!(counter_delta(-1, 4), 0);
        assert_eq!(counter_delta(5, 8), 5);
        assert_eq!(counter_delta(i64::MAX, -1), i64::MIN);
    }
    #[test]
    fn rate_boundaries() {
        assert_eq!(rates(10, 10, 12, 14, 2_000_000_000, false), Some((1, 2)));
        assert_eq!(rates(10, 10, 9, 14, 1, false), Some((0, 0)));
        assert_eq!(rates(0, 0, i64::MAX, 1, 1, false), None);
    }
    #[test]
    fn stable_ranking() {
        assert_eq!(ranking(&[2, 4, 4, -1, 2]), vec![1, 2, 0, 4, 3]);
    }
    #[test]
    fn abi_rejects_invalid() {
        unsafe {
            assert_eq!(
                smartvpn_traffic_values_v1(std::ptr::null(), std::ptr::null_mut()),
                -1
            );
            assert_eq!(
                smartvpn_traffic_ranking_v1(std::ptr::null(), 0, std::ptr::null_mut()),
                0
            );
            assert_eq!(
                smartvpn_traffic_ranking_v1(std::ptr::null(), 1, std::ptr::null_mut()),
                -1
            );
        }
    }
}
