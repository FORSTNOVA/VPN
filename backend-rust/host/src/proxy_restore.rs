use serde_json::{json, Value};
use std::io::{self, Read};

fn restore_fields(current: &Value, address: &str) -> Vec<&'static str> {
    let expected = [
        (
            "ProxyEnable",
            json!({"present":true,"kind":"DWord","value":1}),
        ),
        (
            "ProxyServer",
            json!({"present":true,"kind":"String","value":address}),
        ),
        (
            "ProxyOverride",
            json!({"present":true,"kind":"String","value":"<local>"}),
        ),
        (
            "AutoConfigURL",
            json!({"present":false,"kind":"","value":null}),
        ),
        (
            "AutoDetect",
            json!({"present":true,"kind":"DWord","value":0}),
        ),
    ];
    expected
        .into_iter()
        .filter_map(|(name, record)| (current.get(name) == Some(&record)).then_some(name))
        .collect()
}

pub fn run() -> io::Result<()> {
    let mut buf = Vec::new();
    io::stdin().take(1_048_577).read_to_end(&mut buf)?;
    if buf.len() > 1_048_576 {
        return Err(io::Error::other("proxy state too large"));
    }
    let v: Value = serde_json::from_slice(&buf)?;
    let address = v["address"]
        .as_str()
        .ok_or_else(|| io::Error::other("missing proxy address"))?;
    println!(
        "{}",
        serde_json::to_string(&restore_fields(&v["current"], address))?
    );
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn restore_requires_current_type_value_and_presence() {
        let current = json!({
            "ProxyServer":{"value":"127.0.0.1:7","kind":"String","present":true},
            "ProxyEnable":{"value":"1","kind":"String","present":true},
            "ProxyOverride":{"value":"<LOCAL>","kind":"String","present":true},
            "AutoConfigURL":{"value":null,"kind":"","present":false}
        });
        assert_eq!(
            restore_fields(&current, "127.0.0.1:7"),
            ["ProxyServer", "AutoConfigURL"]
        );
        assert_eq!(restore_fields(&current, "127.0.0.1:8"), ["AutoConfigURL"]);
        assert!(restore_fields(&json!({}), "127.0.0.1:7").is_empty());
    }
}
