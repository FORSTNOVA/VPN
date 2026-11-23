use smartvpn_core::node_fields::*;
use std::collections::BTreeMap;
fn input<'a>(kind: &'a str, secret: &'a str, query: &'a BTreeMap<String, String>) -> NodeInput<'a> {
    NodeInput {
        kind,
        secret,
        query,
        server: "test.invalid",
        port: 443,
        name: "test",
    }
}
fn q(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}
fn s(v: &str) -> Value {
    Value::String(v.into())
}
#[test]
fn vmess_normalized_values_and_alpn_array_preserve_commas() {
    let query = q(&[
        ("__smartvpn_alter_id", "-1"),
        ("__smartvpn_cipher", " AUTO "),
        ("__smartvpn_tls", "yes"),
        ("__smartvpn_sni", " sni "),
        ("__smartvpn_host", " host "),
        ("__smartvpn_network", "grpc"),
        ("__smartvpn_path", " svc "),
        ("__smartvpn_fp", " chrome "),
        ("__smartvpn_alpn_count", "2"),
        ("__smartvpn_alpn_0", "h2,h3"),
        ("__smartvpn_alpn_1", "h2"),
    ]);
    let p = convert_node(input("vmess", " uuid ", &query)).unwrap();
    assert_eq!(p["uuid"], s("uuid"));
    assert_eq!(p["alterId"], Value::Int(-1));
    assert_eq!(p["cipher"], s("auto"));
    assert_eq!(p["servername"], s("sni"));
    assert_eq!(p["client-fingerprint"], s("chrome"));
    assert_eq!(p["alpn"], Value::Array(vec![s("h2,h3"), s("h2")]));
    assert_eq!(
        p["grpc-opts"],
        Value::Object(BTreeMap::from([("grpc-service-name".into(), s(" svc "))]))
    );
}
#[test]
fn vmess_false_tls_and_invalid_normalization() {
    let mut query = q(&[
        ("__smartvpn_alter_id", "0"),
        ("__smartvpn_alpn_count", "0"),
        ("__smartvpn_tls", " FALSE "),
        ("__smartvpn_cipher", " None "),
    ]);
    let p = convert_node(input("vmess", "uuid", &query)).unwrap();
    assert!(!p.contains_key("tls"));
    assert_eq!(p["cipher"], s("None"));
    query.insert("__smartvpn_alpn_count".into(), "1000000000".into());
    assert!(convert_node(input("vmess", "uuid", &query)).is_err());
    query.insert("__smartvpn_alpn_count".into(), "1".into());
    assert!(convert_node(input("vmess", "uuid", &query)).is_err());
}
#[test]
fn shadowsocks_preserves_whitespace_password_and_requires_cipher() {
    let query = q(&[("__smartvpn_cipher", " aes-128-gcm ")]);
    let p = convert_node(input("ss", " ", &query)).unwrap();
    assert_eq!(p["password"], s(" "));
    assert_eq!(p["cipher"], s("aes-128-gcm"));
    assert!(convert_node(input("ss", "", &query)).is_err());
    assert!(convert_node(input("ss", "pass", &q(&[]))).is_err());
}
#[test]
fn shadowsocksr_preserves_empty_credentials_and_raw_parameters() {
    let query = q(&[
        ("__smartvpn_cipher", " cipher "),
        ("__smartvpn_protocol", " protocol "),
        ("__smartvpn_obfs", " obfs "),
        ("__smartvpn_obfs_param", " param "),
    ]);
    let p = convert_node(input("ssr", "", &query)).unwrap();
    assert_eq!(p["password"], s(""));
    assert_eq!(p["cipher"], s("cipher"));
    assert_eq!(p["protocol"], s("protocol"));
    assert_eq!(p["obfs"], s("obfs"));
    assert_eq!(p["obfs-param"], s(" param "));
    assert!(!p.contains_key("protocol-param"));
}
