use smartvpn_core::smartvpn_subscription_v1;
#[test]
fn subscription_abi_contract() {
    let input = b"https://example.invalid/a|ss://x";
    let mut out = [0u8; 256];
    let mut written = 0usize;
    unsafe {
        assert_eq!(
            smartvpn_subscription_v1(
                6,
                input.as_ptr(),
                input.len(),
                out.as_mut_ptr(),
                1,
                &mut written
            ),
            -2
        );
        assert!(written > 1);
        assert_eq!(
            smartvpn_subscription_v1(
                6,
                input.as_ptr(),
                input.len(),
                out.as_mut_ptr(),
                256,
                &mut written
            ),
            0
        );
        assert_eq!(u64::from_le_bytes(out[..8].try_into().unwrap()), 1);
        assert_eq!(
            smartvpn_subscription_v1(
                99,
                input.as_ptr(),
                input.len(),
                out.as_mut_ptr(),
                256,
                &mut written
            ),
            -1
        );
        assert_eq!(
            smartvpn_subscription_v1(1, [255u8].as_ptr(), 1, out.as_mut_ptr(), 256, &mut written),
            -1
        );
        assert_eq!(
            smartvpn_subscription_v1(1, std::ptr::null(), 1, out.as_mut_ptr(), 256, &mut written),
            -1
        );
        assert_eq!(
            smartvpn_subscription_v1(1, std::ptr::null(), 0, out.as_mut_ptr(), 256, &mut written),
            0
        );
        assert_eq!(written, 0);
    }
}
