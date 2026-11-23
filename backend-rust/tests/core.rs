use smartvpn_core::*;
#[test]
fn throughput_matches_go_cases() {
    for (bytes, ns, expected) in [
        (1_000_000, 1_000_000_000, 8.0),
        (10_000_000, 10_000_000_000, 8.0),
        (1_000_000, 500_000_000, 16.0),
        (0, 1, 0.0),
        (1, 0, 0.0),
        (-1, 1, 0.0),
        (1, -1, 0.0),
    ] {
        assert_eq!(throughput_mbps(bytes, ns), expected);
    }
}
#[test]
fn request_wire_format() {
    let id = [7; 12];
    let msg = build_stun_request(id);
    assert_eq!(&msg[..8], &[0, 1, 0, 0, 0x21, 0x12, 0xa4, 0x42]);
    assert_eq!(&msg[8..], &id);
}
#[test]
fn go_oracle_fixtures() {
    for line in include_str!("stun-fixtures.tsv").lines() {
        let (hex, expected) = line.split_once('\t').unwrap();
        let bytes: Vec<u8> = (0..hex.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&hex[i..i + 2], 16).unwrap())
            .collect();
        let actual = match parse_stun_address(&bytes) {
            Ok(v) => v,
            Err(e) => format!("ERR:{e}"),
        };
        assert_eq!(actual, expected, "input={hex}");
    }
}
#[test]
fn arbitrary_inputs_do_not_panic() {
    let mut seed = 42u64;
    for len in 0..512 {
        let mut bytes = vec![0; len];
        for b in &mut bytes {
            seed = seed.wrapping_mul(6364136223846793005).wrapping_add(1);
            *b = (seed >> 32) as u8;
        }
        let _ = parse_stun_address(&bytes);
        if len >= 20 {
            bytes[0..2].copy_from_slice(&[1, 1]);
            bytes[4..8].copy_from_slice(&[0x21, 0x12, 0xa4, 0x42]);
            let _ = parse_stun_address(&bytes);
        }
    }
}

#[test]
fn ffi_contract() {
    let mut output = [0u8; 256];
    let mut written = 999usize;
    unsafe {
        assert_eq!(
            smartvpn_parse_stun_v1(
                std::ptr::null(),
                0,
                output.as_mut_ptr(),
                output.len(),
                &mut written
            ),
            1
        );
        assert_eq!(
            std::str::from_utf8(&output[..written]).unwrap(),
            "STUN 应答过短"
        );
        assert_eq!(
            smartvpn_parse_stun_v1(
                std::ptr::null(),
                1,
                output.as_mut_ptr(),
                output.len(),
                &mut written
            ),
            -1
        );
        assert_eq!(
            smartvpn_parse_stun_v1(std::ptr::null(), 0, output.as_mut_ptr(), 0, &mut written),
            -1
        );
        assert_eq!(written, 0);
        let mut rate = 0.0;
        assert_eq!(smartvpn_throughput_v1(1_000_000, 500_000_000, &mut rate), 0);
        assert_eq!(rate, 16.0);
        assert_eq!(smartvpn_throughput_v1(1, 1, std::ptr::null_mut()), -1);
    }
}

#[test]
fn large_duration_preserves_go_rounding() {
    assert_eq!(
        throughput_mbps(1_000_000, 1_026_847_926_404_610_462),
        7.79083230757555e-9
    );
}
