use smartvpn_core::subscription::*;

#[test]
fn four_base64_encodings_and_noncanonical_tail_bits() {
    for s in [
        "Zg==",
        "Zg",
        "Zh==",
        "Zh",
        "Zg\n\t==",
        "\u{3000}Z\u{a0}g==\u{85}",
    ] {
        assert_eq!(decode_subscription_base64(s).unwrap(), b"f");
    }
    for s in ["Zm8=", "Zm8", "Zm9=", "Zm9"] {
        assert_eq!(decode_subscription_base64(s).unwrap(), b"fo");
    }
    for s in ["+/8=", "+/8", "-_8=", "-_8"] {
        assert_eq!(decode_subscription_base64(s).unwrap(), [251, 255]);
    }
    assert_eq!(decode_subscription_base64(" \n\u{205f}").unwrap(), b"");
    for s in [
        "Z",
        "Zg=",
        "Zg===",
        "=Zg=",
        "Z=g=",
        "Zg==Zg==",
        "+_8=",
        "Zg\u{200b}==",
        "!!!!",
    ] {
        assert_eq!(
            decode_subscription_base64(s),
            Err("invalid Base64 subscription"),
            "{s}"
        );
    }
}

#[test]
fn prefix_rules_preserve_case_and_anytls_difference() {
    assert!(is_proxy_uri_link("\u{3000}VMESS://x\u{a0}"));
    assert!(is_proxy_uri_link("HYSTERİA://x"));
    assert!(is_proxy_uri_link("anytls://x"));
    assert!(!has_proxy_uri("anytls://x"));
    assert!(!has_proxy_uri("VMESS://x"));
    assert!(!has_proxy_uri(" vmess://x"));
    assert!(!has_proxy_uri("a\rvmess://x"));
    assert!(has_proxy_uri("a\nvmess://x"));
    assert!(!has_proxy_uri("a\n vmess://x"));
    assert!(!is_proxy_uri_link("https://example.com"));
}

#[test]
fn announcement_markers_are_narrow_substrings() {
    assert!(is_informational_node("更新：剩余流量 10 GB"));
    assert!(is_informational_node("请到官网"));
    assert!(!is_informational_node("香港 Premium"));
    assert!(!is_informational_node("流量节点"));
}

#[test]
fn inline_classification_matches_existing_quirks() {
    for s in [
        "proxies: []",
        "other: value\nproxies: []",
        "{invalid json still classified",
        "vmess://x",
        "dm1lc3M6Ly94",
        "eyJ4IjoxfQ==",
        "/wp2bWVzczovL3g=",
    ] {
        assert!(is_inline_subscription(s), "{s}");
    }
    for s in [
        "",
        "\u{a0}",
        "VMESS://x",
        "anytls://x",
        "https://sub.example",
        "text\n proxies: []",
    ] {
        assert!(!is_inline_subscription(s), "{s}");
    }
}

#[test]
fn categorization_normalizes_lines_preserves_order_and_original_parts() {
    let (remote, inline) = categorize_subscription_input(
        " https://sub.example/a\r\nVMESS://x |\r anytls://y | junk | http://b | HTTPS://ignored",
    );
    assert_eq!(remote, ["https://sub.example/a", "http://b"]);
    assert_eq!(inline, ["VMESS://x", "anytls://y"]);
    let (remote, inline) = categorize_subscription_input("proxies:\n - name: example");
    assert!(remote.is_empty());
    assert_eq!(inline, ["proxies:"]);
    assert_eq!(
        categorize_subscription_input("https://a;https://b").0,
        ["https://a;https://b"]
    );
    assert_eq!(
        categorize_subscription_input("https://a|https://a").0,
        ["https://a", "https://a"]
    );
}
