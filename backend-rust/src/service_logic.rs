//! Pure log transformations and service takeover decision. No I/O or process control.
pub fn takeover(stale: bool, connected: bool, old_elevated: bool, elevated: bool) -> bool {
    if stale && !connected {
        return true;
    }
    if old_elevated && !elevated {
        return false;
    }
    elevated && !old_elevated
}

pub fn tail_start(body: &[u8], limit: usize) -> usize {
    if body.len() <= limit {
        return 0;
    }
    let start = body.len() - limit;
    body[..start]
        .iter()
        .rposition(|b| *b == b'\n')
        .map_or(start, |i| i + 1)
}

pub fn redact(body: &[u8], secret: &[u8]) -> Option<Vec<u8>> {
    // Empty needle has Go-specific UTF-8 rune semantics; let Go handle it.
    if secret.is_empty() {
        return None;
    }
    // KMP ensures linear work even for adversarial repeated-prefix needles.
    let mut prefix = vec![0; secret.len()];
    let mut matched = 0;
    for i in 1..secret.len() {
        while matched > 0 && secret[i] != secret[matched] {
            matched = prefix[matched - 1];
        }
        if secret[i] == secret[matched] {
            matched += 1;
        }
        prefix[i] = matched;
    }
    let mut out = Vec::new();
    let mut copied = 0;
    matched = 0;
    for (i, &byte) in body.iter().enumerate() {
        while matched > 0 && byte != secret[matched] {
            matched = prefix[matched - 1];
        }
        if byte == secret[matched] {
            matched += 1;
        }
        if matched == secret.len() {
            let start = i + 1 - secret.len();
            if out.len().checked_add(start - copied)?.checked_add(10)? > 16 * 1024 * 1024 {
                return None;
            }
            out.extend_from_slice(&body[copied..start]);
            out.extend_from_slice(b"<redacted>");
            copied = i + 1;
            // Go ReplaceAll uses non-overlapping matches.
            matched = 0;
        }
    }
    if out.len().checked_add(body.len() - copied)? > 16 * 1024 * 1024 {
        return None;
    }
    out.extend_from_slice(&body[copied..]);
    Some(out)
}

/// # Safety
/// Input pointer must reference len readable bytes, or may be null when len=0.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_log_tail_v1(input: *const u8, len: usize, limit: usize) -> usize {
    if (input.is_null() && len != 0) || len > 8 * 1024 * 1024 {
        return usize::MAX;
    }
    let body = if len == 0 {
        &[]
    } else {
        unsafe { std::slice::from_raw_parts(input, len) }
    };
    tail_start(body, limit)
}

/// # Safety
/// Inputs must be readable for their lengths and output/written writable.
/// Returns 0 success, -1 invalid/unsupported, -2 insufficient output capacity.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_log_redact_v1(
    input: *const u8,
    len: usize,
    secret: *const u8,
    secret_len: usize,
    output: *mut u8,
    cap: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null()
        || (input.is_null() && len != 0)
        || secret.is_null()
        || secret_len == 0
        || (output.is_null() && cap != 0)
        || len > 8 * 1024 * 1024
        || secret_len > 8 * 1024 * 1024
    {
        return -1;
    }
    unsafe {
        *written = 0;
    }
    let body = if len == 0 {
        &[]
    } else {
        unsafe { std::slice::from_raw_parts(input, len) }
    };
    let needle = unsafe { std::slice::from_raw_parts(secret, secret_len) };
    let Some(result) = redact(body, needle) else {
        return -1;
    };
    unsafe {
        *written = result.len();
    }
    if result.len() > cap {
        return -2;
    }
    if !result.is_empty() {
        unsafe {
            std::ptr::copy_nonoverlapping(result.as_ptr(), output, result.len());
        }
    }
    0
}

#[no_mangle]
pub extern "C" fn smartvpn_service_takeover_v1(flags: u32) -> i32 {
    if flags > 15 {
        return -1;
    }
    i32::from(takeover(
        flags & 1 != 0,
        flags & 2 != 0,
        flags & 4 != 0,
        flags & 8 != 0,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn decisions() {
        for f in 0..16 {
            assert_eq!(
                smartvpn_service_takeover_v1(f),
                i32::from((f & 1 != 0 && f & 2 == 0) || (f & 8 != 0 && f & 4 == 0))
            );
        }
        assert_eq!(smartvpn_service_takeover_v1(16), -1);
    }
    #[test]
    fn byte_redaction() {
        assert_eq!(redact(b"aaa", b"aa").unwrap(), b"<redacted>a");
        assert_eq!(
            redact(&[255, 0, 255], &[255]).unwrap(),
            b"<redacted>\0<redacted>"
        );
        assert!(redact(b"a", b"").is_none());
    }
    #[test]
    fn tails() {
        assert_eq!(tail_start(b"abc\ndef\nxyz", 4), 4);
        assert_eq!(tail_start(b"abc", 1), 2);
        assert_eq!(tail_start(b"abc\n", 0), 4);
        assert_eq!(tail_start(b"abc", 99), 0);
    }
    #[test]
    fn repeated_prefix_regression() {
        let mut body = vec![b'a'; 2 * 1024 * 1024];
        let mut secret = vec![b'a'; 512 * 1024];
        *secret.last_mut().unwrap() = b'b';
        // A naive starts_with at every byte does hundreds of GB of comparisons.
        assert_eq!(redact(&body, &secret).unwrap(), body);
        *body.last_mut().unwrap() = b'b';
        let actual = redact(&body, &secret).unwrap();
        let mut expected = vec![b'a'; body.len() - secret.len()];
        expected.extend_from_slice(b"<redacted>");
        assert_eq!(actual, expected);
        assert_eq!(redact(b"ababababa", b"ababa").unwrap(), b"<redacted>baba");
        assert_eq!(redact(b"aaaaaa", b"aaa").unwrap(), b"<redacted><redacted>");
    }

    #[test]
    fn boundary() {
        unsafe {
            let mut n = 999;
            assert_eq!(
                smartvpn_log_redact_v1(
                    b"x".as_ptr(),
                    1,
                    b"x".as_ptr(),
                    1,
                    std::ptr::null_mut(),
                    0,
                    &mut n
                ),
                -2
            );
            assert_eq!(n, 10);
            assert_eq!(smartvpn_log_tail_v1(std::ptr::null(), 1, 0), usize::MAX);
        }
    }
}
