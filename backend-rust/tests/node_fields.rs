use smartvpn_core::node_fields::*;
use std::collections::BTreeMap;
fn map(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}
fn convert(
    kind: &str,
    query: &BTreeMap<String, String>,
) -> Result<BTreeMap<String, Value>, ConversionError> {
    convert_node(NodeInput {
        kind,
        server: "example.invalid",
        port: 443,
        name: "测试节点",
        secret: " secret ",
        query,
    })
}
fn s(v: &str) -> Value {
    Value::String(v.into())
}
#[test]
fn vless_reality_ws_trims_only_the_go_fields() {
    let p = convert(
        "vless",
        &map(&[
            ("security", " ReAlItY "),
            ("pbk", " key "),
            ("sid", " id "),
            ("sni", " sni.invalid "),
            ("fp", " chrome "),
            ("allowInsecure", " YES "),
            ("flow", " flow "),
            ("type", "websocket"),
            ("path", " /path "),
        ]),
    )
    .unwrap();
    assert_eq!(p["uuid"], s(" secret "));
    assert_eq!(p["tls"], Value::Bool(true));
    assert_eq!(p["servername"], s("sni.invalid"));
    assert_eq!(p["flow"], s("flow"));
    assert_eq!(p["client-fingerprint"], s("chrome"));
    assert_eq!(p["skip-cert-verify"], Value::Bool(true));
    assert_eq!(
        p["reality-opts"],
        Value::Object(BTreeMap::from([
            ("public-key".into(), s("key")),
            ("short-id".into(), s("id"))
        ]))
    );
    assert_eq!(
        p["ws-opts"],
        Value::Object(BTreeMap::from([
            ("path".into(), s(" /path ")),
            (
                "headers".into(),
                Value::Object(BTreeMap::from([("Host".into(), s("sni.invalid"))]))
            )
        ]))
    );
}
#[test]
fn vless_errors_and_plain_omissions() {
    assert_eq!(
        convert("vless", &map(&[("security", "reality")])),
        Err(ConversionError::Message(
            "vless reality 链接缺少 public key（pbk）".into()
        ))
    );
    assert_eq!(
        convert("vless", &map(&[("security", " bad ")])),
        Err(ConversionError::Message(
            "暂不支持 vless 的 security=bad".into()
        ))
    );
    let p = convert(
        "vless",
        &map(&[
            ("security", "none"),
            ("sni", "x"),
            ("fp", "x"),
            ("allowInsecure", "1"),
            ("type", "tcp"),
        ]),
    )
    .unwrap();
    for key in [
        "tls",
        "servername",
        "client-fingerprint",
        "skip-cert-verify",
        "network",
    ] {
        assert!(!p.contains_key(key));
    }
}
#[test]
fn trojan_first_nonempty_flags_alpn_and_untrimmed_host() {
    let p = convert(
        "trojan",
        &map(&[
            ("sni", "  "),
            ("peer", " peer "),
            ("allowInsecure", "0"),
            ("insecure", "true"),
            ("alpn", " h2, ,http/1.1,h2 "),
            ("type", "http"),
            ("host", " host "),
        ]),
    )
    .unwrap();
    assert_eq!(p["sni"], s("peer"));
    assert!(!p.contains_key("skip-cert-verify"));
    assert_eq!(
        p["alpn"],
        Value::Array(vec![s("h2"), s("http/1.1"), s("h2")])
    );
    assert_eq!(
        p["h2-opts"],
        Value::Object(BTreeMap::from([(
            "host".into(),
            Value::Array(vec![s(" host ")])
        )]))
    );
}
#[test]
fn transports_preserve_unknown_value_and_omit_empty_options() {
    for (kind, expected) in [
        ("ws", "ws"),
        ("websocket", "ws"),
        ("grpc", "grpc"),
        ("h2", "h2"),
        ("http", "h2"),
    ] {
        let p = convert("trojan", &map(&[("type", kind)])).unwrap();
        assert_eq!(p["network"], s(expected));
        for key in ["ws-opts", "grpc-opts", "h2-opts"] {
            assert!(!p.contains_key(key));
        }
    }
    let p = convert(
        "trojan",
        &map(&[("type", " GRPC "), ("serviceName", " svc ")]),
    )
    .unwrap();
    assert_eq!(
        p["grpc-opts"],
        Value::Object(BTreeMap::from([("grpc-service-name".into(), s(" svc "))]))
    );
    assert_eq!(
        convert("trojan", &map(&[("type", " Bad\n")])),
        Err(ConversionError::UnsupportedTransport(" Bad\n".into()))
    );
}
#[test]
fn hysteria_ignores_transport_and_obfs_password_without_obfs() {
    let p = convert(
        "hysteria2",
        &map(&[
            ("type", "unsupported"),
            ("obfs-password", "secret"),
            ("allowInsecure", "1"),
            ("sni", " sni "),
            ("alpn", "h3"),
        ]),
    )
    .unwrap();
    for key in ["network", "udp", "obfs-password", "skip-cert-verify"] {
        assert!(!p.contains_key(key));
    }
    assert_eq!(p["sni"], s("sni"));
    assert_eq!(p["alpn"], Value::Array(vec![s("h3")]));
    let p = convert(
        "hysteria2",
        &map(&[
            ("obfs", " salamander "),
            ("obfs-password", " pass "),
            ("insecure", " TRUE "),
        ]),
    )
    .unwrap();
    assert_eq!(p["obfs"], s("salamander"));
    assert_eq!(p["obfs-password"], s("pass"));
    assert_eq!(p["skip-cert-verify"], Value::Bool(true));
}
#[test]
fn blank_secret_and_unknown_kind() {
    let q = BTreeMap::new();
    assert_eq!(
        convert_node(NodeInput {
            kind: "vless",
            server: "s",
            port: 443,
            name: "n",
            secret: "\u{a0}\u{3000}",
            query: &q
        }),
        Err(ConversionError::Message("节点链接缺少必要字段".into()))
    );
    assert!(matches!(
        convert("unknown", &q),
        Err(ConversionError::Message(_))
    ));
}
