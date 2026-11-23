fn socks_payload(packet: &[u8]) -> Result<usize, isize> {
    if packet.len() < 4 || packet[2] != 0 {
        return Err(-2);
    }
    let offset = match packet[3] {
        1 => 10,
        4 => 22,
        3 => 7 + usize::from(*packet.get(4).ok_or(-2isize)?),
        _ => return Err(-3),
    };
    (offset <= packet.len()).then_some(offset).ok_or(-2)
}

fn skip_name(message: &[u8], mut pos: usize) -> Result<usize, isize> {
    loop {
        let length = *message.get(pos).ok_or(-5isize)?;
        if length == 0 {
            return Ok(pos + 1);
        }
        if length & 0xc0 == 0xc0 {
            return message.get(pos + 1).map(|_| pos + 2).ok_or(-5);
        }
        pos += 1 + usize::from(length);
    }
}

/// Return the DNS payload offset for this transaction, or -2 to keep reading.
/// Matching deliberately precedes DNS record parsing, as in the original probe.
///
/// # Safety
/// Nonempty input must reference len readable bytes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_dns_relay_reply_v1(
    packet: *const u8,
    len: usize,
    id: usize,
) -> isize {
    let Ok(id) = u16::try_from(id) else { return -1 };
    select_relay_reply(packet, len, |reply| reply.starts_with(&id.to_be_bytes()))
}

/// # Safety
/// Nonempty packet must reference len readable bytes; id must reference 12 bytes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_stun_relay_reply_v1(
    packet: *const u8,
    len: usize,
    id: *const u8,
) -> isize {
    if id.is_null() {
        return -1;
    }
    let id = std::slice::from_raw_parts(id, 12);
    select_relay_reply(packet, len, |reply| {
        crate::udp_request::matches_stun_reply(id, reply)
    })
}

unsafe fn select_relay_reply(
    packet: *const u8,
    len: usize,
    matches: impl FnOnce(&[u8]) -> bool,
) -> isize {
    if len == 0 {
        return -2;
    }
    if packet.is_null() || len > isize::MAX as usize {
        return -1;
    }
    let packet = std::slice::from_raw_parts(packet, len);
    let Ok(offset) = socks_payload(packet) else {
        return -2;
    };
    if matches(&packet[offset..]) {
        offset as isize
    } else {
        -2
    }
}

fn first_a(message: &[u8]) -> Result<usize, isize> {
    if message.len() < 12 {
        return Err(-4);
    }
    let questions = u16::from_be_bytes([message[4], message[5]]);
    let answers = u16::from_be_bytes([message[6], message[7]]);
    let mut pos = 12;
    for _ in 0..questions {
        pos = skip_name(message, pos)? + 4;
        if pos > message.len() {
            return Err(-6);
        }
    }
    for _ in 0..answers {
        pos = skip_name(message, pos)?;
        let header = message.get(pos..pos + 10).ok_or(-6isize)?;
        let kind = u16::from_be_bytes([header[0], header[1]]);
        let size = usize::from(u16::from_be_bytes([header[8], header[9]]));
        pos += 10;
        if pos + size > message.len() {
            return Err(-6);
        }
        if kind == 1 && size == 4 {
            return Ok(pos);
        }
        pos += size;
    }
    Err(-7)
}

/// # Safety
/// A nonempty packet must point to a readable allocation of at least len bytes.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_udp_reply_offset_v1(
    op: usize,
    packet: *const u8,
    len: usize,
) -> isize {
    let bytes = if len == 0 {
        &[]
    } else if packet.is_null() || len > isize::MAX as usize {
        return -1;
    } else {
        std::slice::from_raw_parts(packet, len)
    };
    let result = match op {
        1 => socks_payload(bytes),
        2 => first_a(bytes),
        _ => return -1,
    };
    result.map_or_else(|error| error, |offset| offset as isize)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn stun_relay_requires_a_matching_success_header() {
        let id = [0xa5; 12];
        let mut packet = [0; 30];
        packet[3] = 1;
        packet[10..12].copy_from_slice(&[1, 1]);
        packet[18..30].copy_from_slice(&id);
        unsafe {
            assert_eq!(
                smartvpn_stun_relay_reply_v1(packet.as_ptr(), 30, id.as_ptr()),
                10
            );
            assert_eq!(
                smartvpn_stun_relay_reply_v1(packet.as_ptr(), 29, id.as_ptr()),
                -2
            );
            packet[29] ^= 1;
            assert_eq!(
                smartvpn_stun_relay_reply_v1(packet.as_ptr(), 30, id.as_ptr()),
                -2
            );
            assert_eq!(
                smartvpn_stun_relay_reply_v1(std::ptr::null(), 0, id.as_ptr()),
                -2
            );
            assert_eq!(
                smartvpn_stun_relay_reply_v1(std::ptr::null(), 1, id.as_ptr()),
                -1
            );
            assert_eq!(
                smartvpn_stun_relay_reply_v1(packet.as_ptr(), 30, std::ptr::null()),
                -1
            );
        }
    }
    #[test]
    fn dns_relay_matches_both_id_bytes_and_preserves_abi_errors() {
        let mut packet = [0, 0, 0, 1, 0, 0, 0, 0, 0, 53, 0, 0];
        for id in 0..=u16::MAX {
            packet[10..].copy_from_slice(&id.to_be_bytes());
            unsafe {
                assert_eq!(
                    smartvpn_dns_relay_reply_v1(packet.as_ptr(), 12, id.into()),
                    10
                );
                assert_eq!(
                    smartvpn_dns_relay_reply_v1(packet.as_ptr(), 12, (id ^ 0x0100).into()),
                    -2
                );
                assert_eq!(
                    smartvpn_dns_relay_reply_v1(packet.as_ptr(), 11, id.into()),
                    -2
                );
            }
        }
        unsafe {
            assert_eq!(smartvpn_dns_relay_reply_v1(std::ptr::null(), 0, 0), -2);
            assert_eq!(smartvpn_dns_relay_reply_v1(std::ptr::null(), 1, 0), -1);
            assert_eq!(smartvpn_dns_relay_reply_v1(packet.as_ptr(), 12, 65536), -1);
        }
    }
}
