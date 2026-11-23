/// # Safety
/// Nonempty input and output must be valid for their stated sizes and disjoint.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_udp_request_v1(
    op: usize,
    input: *const u8,
    len: usize,
    value: usize,
    output: *mut u8,
    capacity: usize,
) -> isize {
    let Ok(value) = u16::try_from(value) else {
        return -1;
    };
    if op == 0 && len > 253 {
        return -2;
    }
    if len > 255 || (len != 0 && input.is_null()) || output.is_null() {
        return -1;
    }
    let data = if len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(input, len)
    };
    let mut packet = Vec::with_capacity(len + 18);
    match op {
        0 => {
            // Go's rune iteration replaces malformed UTF-8. Delegate these rare
            // inputs so replacement boundaries and expanded lengths match Go.
            if std::str::from_utf8(data).is_err() {
                return -1;
            }
            packet.extend(value.to_be_bytes());
            packet.extend([1, 0, 0, 1, 0, 0, 0, 0, 0, 0]);
            for label in data.split(|b| *b == b'.') {
                if label.is_empty() || label.len() > 63 {
                    return -2;
                }
                packet.push(label.len() as u8);
                packet.extend_from_slice(label);
            }
            packet.extend([0, 0, 1, 0, 1]);
        }
        2 => {
            let Ok(id) = data.try_into() else { return -1 };
            if value != 0 {
                return -1;
            }
            packet.extend(crate::build_stun_request(id));
        }
        1 | 3 | 4 => {
            if value == 0 || len == 0 || (op == 1 && len != 4) || (op == 4 && len != 16) {
                return -1;
            }
            packet.extend([0, 0, 0, op as u8]);
            if op == 3 {
                packet.push(len as u8);
            }
            packet.extend_from_slice(data);
            packet.extend(value.to_be_bytes());
        }
        5 if value == 0 && data.is_empty() => packet.extend([5, 1, 0]),
        6 if value == 0 => {
            if data != [5, 0] {
                return -3;
            }
            packet.extend([5, 3, 0, 1, 0, 0, 0, 0, 0, 0]);
        }
        _ => return -1,
    }
    if capacity < packet.len() {
        return -1;
    }
    std::ptr::copy_nonoverlapping(packet.as_ptr(), output, packet.len());
    packet.len() as isize
}

/// Match the reply before parsing its attributes. The address parser checks
/// the magic cookie after the relay selects a matching transaction.
///
/// # Safety
/// Non-null pointers must reference 12 readable ID bytes and 20 readable header bytes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_stun_match_v1(id: *const u8, header: *const u8) -> usize {
    if id.is_null() || header.is_null() {
        return usize::MAX;
    }
    let header = std::slice::from_raw_parts(header, 20);
    usize::from(matches_stun_reply(
        std::slice::from_raw_parts(id, 12),
        header,
    ))
}

pub(crate) fn matches_stun_reply(id: &[u8], reply: &[u8]) -> bool {
    reply.starts_with(&[1, 1]) && reply.get(8..20) == Some(id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_no_auth_socks5_can_emit_associate() {
        for pair in 0..=u16::MAX {
            let method = pair.to_be_bytes();
            let mut dst = [0xa5; 10];
            let n =
                unsafe { smartvpn_udp_request_v1(6, method.as_ptr(), 2, 0, dst.as_mut_ptr(), 10) };
            if method == [5, 0] {
                assert_eq!(n, 10);
                assert_eq!(dst, [5, 3, 0, 1, 0, 0, 0, 0, 0, 0]);
            } else {
                assert_eq!(n, -3);
                assert_eq!(dst, [0xa5; 10]);
            }
        }
    }

    #[test]
    fn rejected_calls_leave_the_output_untouched() {
        let name = b"localhost";
        let mut output = [0xa5; 32];
        for (op, len, value, capacity, expected) in [
            (0, name.len(), 0, 1, -1),      // short buffer
            (0, 0, 0, 32, -2),              // empty name
            (4, name.len(), 53, 32, -1),    // wrong address width
            (3, name.len(), 0, 32, -1),     // zero port
            (0, name.len(), 65536, 32, -1), // out-of-range ID
        ] {
            let result = unsafe {
                smartvpn_udp_request_v1(
                    op,
                    name.as_ptr(),
                    len,
                    value,
                    output.as_mut_ptr(),
                    capacity,
                )
            };
            assert_eq!(result, expected);
            assert_eq!(output, [0xa5; 32]);
        }
    }

    #[test]
    fn stun_encoder_and_matcher_boundaries() {
        let id = [0xa7; 12];
        let mut header = [0; 20];
        for (len, value, cap) in [(11, 0, 20), (12, 1, 20), (12, 0, 19)] {
            let n = unsafe {
                smartvpn_udp_request_v1(2, id.as_ptr(), len, value, header.as_mut_ptr(), cap)
            };
            assert_eq!(n, -1);
            assert_eq!(header, [0; 20]);
        }
        let n = unsafe { smartvpn_udp_request_v1(2, id.as_ptr(), 12, 0, header.as_mut_ptr(), 20) };
        assert_eq!(n, 20);
        assert_eq!(header, crate::build_stun_request(id));
        unsafe {
            assert_eq!(smartvpn_stun_match_v1(id.as_ptr(), header.as_ptr()), 0);
            header[0] = 1;
            assert_eq!(smartvpn_stun_match_v1(id.as_ptr(), header.as_ptr()), 1);
            assert_eq!(
                smartvpn_stun_match_v1(std::ptr::null(), header.as_ptr()),
                usize::MAX
            );
            assert_eq!(
                smartvpn_stun_match_v1(id.as_ptr(), std::ptr::null()),
                usize::MAX
            );
        }
    }
}
