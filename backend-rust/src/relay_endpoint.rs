use std::net::{Ipv4Addr, Ipv6Addr};

// A positive result asks the stream adapter for the missing bytes. On
// completion return the negative endpoint length biased by ENDPOINT_BASE.
const ENDPOINT_BASE: isize = 512;

fn decode(packet: &[u8], output: &mut [u8]) -> isize {
    if packet.len() < 4 {
        return (4 - packet.len()) as isize;
    }
    if packet[0] != 5 {
        return -2;
    }
    if packet[1] != 0 {
        return -isize::from(packet[1]) - 256;
    }
    let (start, size) = match packet[3] {
        1 => (4, 4),
        4 => (4, 16),
        3 if packet.len() == 4 => return 1,
        3 => (5, usize::from(packet[4])),
        _ => return -3,
    };
    let end = start + size;
    // Keep the original ReadFull boundary between address and port, including
    // the case where a reader returns the final address bytes with an error.
    if packet.len() < end {
        return (end - packet.len()) as isize;
    }
    if packet.len() < end + 2 {
        return (end + 2 - packet.len()) as isize;
    }
    if packet.len() != end + 2 {
        return -1;
    }
    let raw = &packet[start..end];
    let ip;
    let host = match packet[3] {
        1 => {
            ip = Ipv4Addr::from(<[u8; 4]>::try_from(raw).unwrap()).to_string();
            ip.as_bytes()
        }
        4 => {
            let addr = Ipv6Addr::from(<[u8; 16]>::try_from(raw).unwrap());
            ip = match addr.to_ipv4_mapped() {
                Some(v4) => v4.to_string(),
                None => addr.to_string(),
            };
            ip.as_bytes()
        }
        _ => raw,
    };
    let host = match host {
        b"" | b"0.0.0.0" | b"::" => &b"127.0.0.1"[..],
        _ => host,
    };
    let port = u16::from_be_bytes([packet[end], packet[end + 1]]).to_string();
    let bracket = host.contains(&b':');
    let len = host.len() + usize::from(bracket) * 2 + 1 + port.len();
    if output.len() < len {
        return -1;
    }
    let mut offset = 0;
    if bracket {
        output[0] = b'[';
        offset += 1;
    }
    output[offset..offset + host.len()].copy_from_slice(host);
    offset += host.len();
    if bracket {
        output[offset] = b']';
        offset += 1;
    }
    output[offset] = b':';
    output[offset + 1..len].copy_from_slice(port.as_bytes());
    -ENDPOINT_BASE - len as isize
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn requests_exact_prefix_and_publishes_only_a_complete_endpoint() {
        let packet = b"\x05\x00\xff\x03\x03a:b\xff\xff";
        let mut dst = [0xa5; 11];
        for (end, count) in [(0, 4), (3, 1), (4, 1), (5, 3), (8, 2), (9, 1)] {
            assert_eq!(decode(&packet[..end], &mut dst), count);
            assert_eq!(dst, [0xa5; 11]);
        }
        assert_eq!(decode(packet, &mut dst[..10]), -1);
        assert_eq!(dst, [0xa5; 11]);
        assert_eq!(decode(packet, &mut dst), -523);
        assert_eq!(&dst, b"[a:b]:65535");
    }
}

/// # Safety
/// Caller provides disjoint readable input and writable output buffers.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_relay_endpoint_v1(
    input: *const u8,
    len: usize,
    output: *mut u8,
    cap: usize,
) -> isize {
    if len > 262 || (len != 0 && input.is_null()) || output.is_null() || cap > isize::MAX as usize {
        return -1;
    }
    let packet = if len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(input, len)
    };
    decode(packet, std::slice::from_raw_parts_mut(output, cap))
}
