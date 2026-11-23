use smartvpn_core::settings_core::*;
#[test]
fn policy() {
    assert!(android_package("com.Game_app"));
    for n in ["com", "a.1", "a..b", "a.b-1", "中.a"] {
        assert!(!android_package(n));
    }
    assert_eq!(
        game_rule("g.exe", "g.exe", true, false),
        "PROCESS-NAME,g.exe,DIRECT"
    );
    assert_eq!(
        game_rule("x", r"C:\a,b.exe", false, true),
        r"PROCESS-PATH-REGEX,^C:\\a\x2cb\.exe$,DIRECT"
    );
    assert!(game_rule("a?b.exe", "x", false, true).is_empty());
}
#[test]
fn normalization() {
    let v = vec![
        ("A.exe".into(), "a.exe".into(), true),
        ("a.exe".into(), "a.exe".into(), true),
    ];
    assert_eq!(normalize(&v, false).unwrap(), vec!["A.exe"]);
    assert!(normalize(&[("bad".into(), "bad".into(), true)], true).is_err());
    assert!(normalize(&[], false).unwrap().is_empty());
}
#[test]
fn socks() {
    for n in [-1, 0, 1, 65535, 65536] {
        assert_eq!(valid_socks(n, true), (0..=65535).contains(&n));
        assert!(!valid_socks(n, false));
    }
    assert_eq!(smartvpn_valid_socks_v1(1, 2), -1);
}
#[test]
fn abi() {
    unsafe {
        let mut out = [55u8; 10];
        let mut n = 99;
        assert_eq!(
            smartvpn_settings_v1(2, std::ptr::null(), 1, out.as_mut_ptr(), 10, &mut n),
            -1
        );
        assert_eq!(n, 0);
        assert_eq!(out, [55u8; 10]);
        assert_eq!(
            smartvpn_settings_v1(2, std::ptr::null(), 0, std::ptr::null_mut(), 0, &mut n),
            0
        );
        assert_eq!(
            smartvpn_settings_v1(1, [0u8; 7].as_ptr(), 7, out.as_mut_ptr(), 10, &mut n),
            -1
        );
    }
}
