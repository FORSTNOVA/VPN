use smartvpn_core::node_fields::*;
use std::collections::BTreeMap;
fn query(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}
fn convert(
    kind: &str,
    secret: &str,
    pairs: &[(&str, &str)],
) -> Result<BTreeMap<String, Value>, ConversionError> {
    convert_node(NodeInput {
        kind,
        server: "test.invalid",
        port: 443,
        name: "test",
        secret,
        query: &query(pairs),
    })
}
fn s(v: &str) -> Value {
    Value::String(v.into())
}
#[test]
fn tuic_userinfo_and_secret_are_distinct() {
    let p = convert(
        "tuic",
        "ignored",
        &[
            ("__smartvpn_tuic_username", " uuid "),
            ("__smartvpn_tuic_password", " pass "),
        ],
    )
    .unwrap();
    assert_eq!(p["uuid"], s(" uuid "));
    assert_eq!(p["password"], s(" pass "));
    assert_eq!(p["udp"], Value::Bool(true));
    let p = convert("tuic", "ignored", &[("__smartvpn_tuic_password", "pass")]).unwrap();
    assert_eq!(p["uuid"], s("pass"));
    let p = convert(
        "tuic",
        "ignored",
        &[
            ("__smartvpn_tuic_username", " "),
            ("__smartvpn_tuic_password", "pass"),
        ],
    )
    .unwrap();
    assert_eq!(p["uuid"], s(" "));
    assert_eq!(
        convert("tuic", "not-used", &[]),
        Err(ConversionError::Message("节点链接缺少必要字段".into()))
    );
    assert!(convert(
        "tuic",
        "",
        &[
            ("__smartvpn_tuic_username", "\u{3000}"),
            ("__smartvpn_tuic_password", "\u{a0}")
        ]
    )
    .is_err());
}
#[test]
fn tuic_precedence_and_raw_relay_mode() {
    let p = convert(
        "tuic",
        "",
        &[
            ("__smartvpn_tuic_username", "uuid"),
            ("sni", " one "),
            ("server_name", "two"),
            ("allow_insecure", "0"),
            ("insecure", "1"),
            ("allowInsecure", "true"),
            ("congestion_controller", " "),
            ("congestion_control", " bbr "),
            ("cc", "cubic"),
            ("udp_relay_mode", " native "),
            ("alpn", "h3, h3, ,hq"),
            ("type", "bad"),
        ],
    )
    .unwrap();
    assert_eq!(p["sni"], s("one"));
    assert!(!p.contains_key("skip-cert-verify"));
    assert_eq!(p["congestion-controller"], s("bbr"));
    assert_eq!(p["udp-relay-mode"], s(" native "));
    assert!(!p.contains_key("network"));
    assert_eq!(p["alpn"], Value::Array(vec![s("h3"), s("h3"), s("hq")]));
    let p = convert(
        "tuic",
        "",
        &[
            ("__smartvpn_tuic_username", "uuid"),
            ("server_name", " alt "),
            ("insecure", " "),
            ("allowInsecure", " YES "),
            ("cc", " cubic "),
        ],
    )
    .unwrap();
    assert_eq!(p["sni"], s("alt"));
    assert_eq!(p["skip-cert-verify"], Value::Bool(true));
    assert_eq!(p["congestion-controller"], s("cubic"));
}
#[test]
fn hysteria_allows_no_auth_and_preserves_alias_precedence() {
    let p = convert("hysteria", "", &[]).unwrap();
    for k in ["auth_str", "up", "down", "udp", "password"] {
        assert!(!p.contains_key(k));
    }
    let p = convert(
        "hysteria",
        " secret ",
        &[
            ("auth", "other"),
            ("peer", " peer "),
            ("sni", "sni"),
            ("insecure", "false"),
            ("allowInsecure", "1"),
            ("obfs", " obfs "),
        ],
    )
    .unwrap();
    assert_eq!(p["auth_str"], s("secret"));
    assert_eq!(p["sni"], s("peer"));
    assert!(!p.contains_key("skip-cert-verify"));
    assert_eq!(p["obfs"], s(" obfs "));
    let p = convert(
        "hysteria",
        "\u{3000}",
        &[
            ("auth", " token "),
            ("peer", " "),
            ("sni", " sni "),
            ("allowInsecure", "true"),
        ],
    )
    .unwrap();
    assert_eq!(p["auth_str"], s("token"));
    assert_eq!(p["sni"], s("sni"));
    assert_eq!(p["skip-cert-verify"], Value::Bool(true));
}
#[test]
fn hysteria_rates_follow_go_decimal_port_rules() {
    for (raw, want) in [
        ("1", Some(1)),
        ("+1", Some(1)),
        ("00042", Some(42)),
        ("\u{3000}+65535\u{a0}", Some(65535)),
        ("0", None),
        ("65536", None),
        ("-1", None),
        ("1_000", None),
        ("１２", None),
        ("1.0", None),
        ("+", None),
        ("++1", None),
        ("0x10", None),
        ("18446744073709551616", None),
    ] {
        let p = convert("hysteria", "", &[("upmbps", raw), ("down", raw)]).unwrap();
        assert_eq!(
            p.get("up"),
            want.as_ref().map(|_| p.get("up").unwrap()),
            "{raw}"
        );
        match want {
            Some(v) => {
                assert_eq!(p["up"], Value::Int(v));
                assert_eq!(p["down"], Value::Int(v));
            }
            None => {
                assert!(!p.contains_key("up"), "{raw}");
                assert!(!p.contains_key("down"), "{raw}");
            }
        }
    }
    let p = convert(
        "hysteria",
        "",
        &[
            ("upmbps", "bad"),
            ("up", "100"),
            ("downmbps", " "),
            ("down", "200"),
        ],
    )
    .unwrap();
    assert!(!p.contains_key("up"));
    assert_eq!(p["down"], Value::Int(200));
}
