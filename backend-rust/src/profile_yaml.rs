use std::collections::HashSet;

use serde_json::{json, Value as Json};
use serde_yaml_ng::{Mapping, Value};

// Metadata fields use the same null/empty defaults as the Go projection.
fn mapping(value: Option<&Value>) -> Option<Option<&Mapping>> {
    match value {
        None | Some(Value::Null) => Some(None),
        Some(v) => v.as_mapping().map(Some),
    }
}

fn text<'a>(row: Option<&'a Mapping>, key: &str) -> Option<&'a str> {
    match row.and_then(|m| m.get(key)) {
        None | Some(Value::Null) => Some(""),
        Some(v) => v.as_str(),
    }
}

pub(crate) fn nodes(input: &str) -> Option<Vec<u8>> {
    let root = crate::profile_yaml_events::document(input, true)?;
    nodes_from(&root)
}

// A query may also receive sing-box JSON, URI lists, or base64. Only claim a
// Clash document when the converter would select its Clash branch.
pub(crate) fn query_nodes(input: &str) -> Option<Vec<u8>> {
    if input.trim_start().starts_with('{') {
        return None;
    }
    let root = crate::profile_yaml_events::document(input, false)?;
    let rows = root.as_mapping()?.get("proxies")?.as_sequence()?;
    if !rows.iter().any(|row| {
        row.get("name").and_then(Value::as_str).is_some_and(|name| {
            !name.is_empty() && !crate::subscription::is_informational_node(name)
        })
    }) {
        return None;
    }
    nodes_from(&root)
}

pub(crate) fn repair_document(input: &str) -> Option<Vec<u8>> {
    let root = crate::profile_yaml_events::document(input, false)?;
    root.as_mapping()?;
    serde_json::to_vec(&object_value(&root, true)?).ok()
}

fn nodes_from(root: &Value) -> Option<Vec<u8>> {
    let profile = root.as_mapping()?;
    let rows = profile.get("proxies")?.as_sequence()?;
    let labels = mapping(profile.get("smartvpn-source-labels"))?;
    if let Some(labels) = labels {
        if labels
            .iter()
            .any(|(k, v)| k.as_str().is_none() || v.as_str().is_none())
        {
            return None;
        }
    }
    if rows.is_empty() {
        return Some(b"null".to_vec());
    }
    let mut nodes = Vec::new();
    let mut seen = HashSet::new();
    for value in rows {
        let row = Some(value.as_mapping()?);
        let name = text(row, "name")?;
        let kind = text(row, "type")?;
        let network = text(row, "network")?;
        text(row, "server")?;
        let ws = mapping(row.and_then(|m| m.get("ws-opts")))?;
        let path = text(ws, "path")?;
        let headers = mapping(ws.and_then(|m| m.get("headers")))?;
        let mut host = false;
        for (k, v) in headers.into_iter().flatten() {
            let key = k.as_str()?;
            if key.replace('\u{017f}', "s").eq_ignore_ascii_case("host")
                && v.as_str().is_some_and(|s| !s.trim().is_empty())
            {
                host = true;
            }
        }
        if name.is_empty()
            || kind.is_empty()
            || crate::subscription::is_informational_node(name)
            || !seen.insert(name)
        {
            continue;
        }
        let tls = match row.and_then(|m| m.get("tls")) {
            Some(Value::Bool(v)) => *v,
            Some(Value::String(v)) => !matches!(v.as_str(), "" | "0" | "false" | "none"),
            _ => false,
        };
        nodes.push(json!({"name":name,"type":kind,"network":network,"tls":tls,
            "wsHostConfigured":host,"wsPathConfigured":!path.is_empty(),"sourceLabel":text(labels,name)?}));
    }
    serde_json::to_vec(&nodes).ok()
}

// Keep extension fields and the distinction between integral and floating-point values.
const FLOAT_KEY: &str = "\0smartvpn.yaml.float";

pub(super) fn object_value(v: &Value, extended: bool) -> Option<Json> {
    match v {
        Value::Null => Some(Json::Null),
        Value::Bool(b) => Some(Json::Bool(*b)),
        Value::String(s) => Some(Json::String(s.clone())),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                return Some(Json::from(i));
            }
            if let Some(u) = n.as_u64() {
                return Some(Json::from(u));
            }
            let f = n.as_f64()?;
            if extended && !f.is_finite() {
                let spelling = if f.is_nan() {
                    ".nan"
                } else if f.is_sign_negative() {
                    "-.inf"
                } else {
                    ".inf"
                };
                return Some(json!({(FLOAT_KEY):spelling}));
            }
            serde_json::Number::from_f64(f).map(Json::Number)
        }
        Value::Sequence(rows) => rows
            .iter()
            .map(|v| object_value(v, extended))
            .collect::<Option<Vec<_>>>()
            .map(Json::Array),
        Value::Mapping(fields) => {
            let mut map = serde_json::Map::new();
            for (key, value) in fields {
                let k = key.as_str()?;
                if extended && k == FLOAT_KEY {
                    return None;
                }
                map.insert(k.into(), object_value(value, extended)?);
            }
            Some(Json::Object(map))
        }
        Value::Tagged(_) => None,
    }
}
pub(crate) fn objects(input: &str, extended: bool) -> Option<Vec<u8>> {
    let root = crate::profile_yaml_events::document(input, false)?;
    let rows = root.as_mapping()?.get("proxies")?.as_sequence()?;
    let mut result = Vec::with_capacity(rows.len());
    for row in rows {
        row.as_mapping()?;
        result.push(object_value(row, extended)?);
    }
    serde_json::to_vec(&result).ok()
}

// The bridge uses JSON-compatible scalar types. Quoting every nested string
// prevents YAML 1.1 readers from resolving timestamps, octal or boolean names.
// Each top-level sequence item occupies a separate line.
pub(crate) fn merged_yaml(input: &str, extended: bool) -> Option<Vec<u8>> {
    let doc: Json = serde_json::from_str(input).ok()?;
    let mut out = String::new();
    for (key, value) in doc.as_object()? {
        if !matches!(
            key.as_str(),
            "proxies"
                | "proxy-groups"
                | "dns"
                | "smartvpn-source-rules"
                | "smartvpn-source-labels"
                | "smartvpn-policy-sections"
        ) {
            return None;
        }
        out.push_str(key);
        out.push(':');
        match value {
            Json::Array(rows) if !rows.is_empty() => {
                out.push('\n');
                for row in rows {
                    out.push_str("  - ");
                    flow_value(row, &mut out, extended)?;
                    out.push('\n');
                }
            }
            _ => {
                out.push(' ');
                flow_value(value, &mut out, extended)?;
                out.push('\n');
            }
        }
    }
    Some(out.into_bytes())
}

fn flow_value(value: &Json, out: &mut String, extended: bool) -> Option<()> {
    if extended {
        match value {
            Json::Object(map) => {
                if let Some(float) = map.get(FLOAT_KEY) {
                    let token = float.as_str()?;
                    if map.len() != 1
                        || !(matches!(token, ".inf" | "-.inf" | ".nan")
                            || (token.bytes().all(|b| {
                                b.is_ascii_digit() || matches!(b, b'+' | b'-' | b'.' | b'e' | b'E')
                            }) && token.parse::<f64>().ok()?.is_finite()))
                    {
                        return None;
                    }
                    out.push_str(token);
                    return Some(());
                }
                out.push('{');
                for (i, (key, v)) in map.iter().enumerate() {
                    if i > 0 {
                        out.push(',')
                    }
                    flow_value(&Json::String(key.clone()), out, false)?;
                    out.push(':');
                    flow_value(v, out, true)?;
                }
                out.push('}');
                return Some(());
            }
            Json::Array(items) => {
                out.push('[');
                for (i, v) in items.iter().enumerate() {
                    if i > 0 {
                        out.push(',')
                    }
                    flow_value(v, out, true)?;
                }
                out.push(']');
                return Some(());
            }
            _ => {}
        }
    }

    let encoded = serde_json::to_string(value).ok()?;
    for c in encoded.chars() {
        if matches!(
            c,
            '\u{007f}'
                ..='\u{009f}' | '\u{2028}' | '\u{2029}' | '\u{feff}' | '\u{fffe}' | '\u{ffff}'
        ) {
            use std::fmt::Write;
            write!(out, "\\u{:04x}", c as u32).ok()?;
        } else {
            out.push(c);
        }
    }
    Some(())
}
