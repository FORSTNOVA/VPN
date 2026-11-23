//! Project the fields used by subscription merge, rule preview and entry DNS.
use crate::profile_yaml::object_value;
use serde_json::{json, Value as Json};
use serde_yaml_ng::{Mapping, Value};

fn text(row: &Mapping, key: &str) -> Option<String> {
    match row.get(key) {
        None | Some(Value::Null) => Some(String::new()),
        Some(v) => Some(v.as_str()?.to_owned()),
    }
}

// Keep missing/null and explicitly empty lists distinct. yaml.v3 skips null
// items when decoding slices of non-pointer strings and structs.
fn list(v: Option<&Value>, project: impl Fn(&Value) -> Option<Json>) -> Option<Json> {
    match v {
        None | Some(Value::Null) => Some(Json::Null),
        Some(v) => v
            .as_sequence()?
            .iter()
            .filter(|v| !v.is_null())
            .map(project)
            .collect::<Option<Vec<_>>>()
            .map(Json::Array),
    }
}
fn strings(row: &Mapping, key: &str) -> Option<Json> {
    list(row.get(key), |v| Some(json!(v.as_str()?)))
}
fn group(v: &Value) -> Option<Json> {
    let row = v.as_mapping()?;
    let include = match row.get("include-all") {
        None | Some(Value::Null) => false,
        Some(Value::Bool(b)) => *b,
        Some(v) => match v.as_str()? {
            "y" | "Y" | "yes" | "Yes" | "YES" | "on" | "On" | "ON" => true,
            "n" | "N" | "no" | "No" | "NO" | "off" | "Off" | "OFF" => false,
            _ => return None,
        },
    };
    let mut extra = Mapping::new();
    for (k, v) in row {
        if !matches!(
            k.as_str()?,
            "name" | "type" | "include-all" | "proxies" | "use"
        ) {
            extra.insert(k.clone(), v.clone());
        }
    }
    let extra = if extra.is_empty() {
        Json::Null
    } else {
        object_value(&Value::Mapping(extra), true)?
    };
    Some(
        json!({"name":text(row,"name")?, "type":text(row,"type")?, "members":strings(row,"proxies")?,
        "providers":strings(row,"use")?, "includeAll":include, "extra":extra}),
    )
}
fn section(v: &Value) -> Option<Json> {
    let row = v.as_mapping()?;
    Some(
        json!({"label":text(row,"label")?, "groups":list(row.get("groups"),group)?, "rules":strings(row,"rules")?}),
    )
}
fn proxy(v: &Value) -> Option<Json> {
    let row = v.as_mapping()?;
    // Entry DNS decodes subscriptionProxy, including fields it doesn't read.
    text(row, "type")?;
    text(row, "network")?;
    if let Some(ws) = row.get("ws-opts").filter(|v| !v.is_null()) {
        let ws = ws.as_mapping()?;
        text(ws, "path")?;
        if let Some(headers) = ws.get("headers").filter(|v| !v.is_null()) {
            headers.as_mapping()?;
        }
    }
    Some(json!({"name":text(row,"name")?, "server":text(row,"server")?}))
}
pub(super) fn decode(input: &str) -> Option<Vec<u8>> {
    let doc = crate::profile_yaml_events::policy_document(input)?;
    let row = doc.as_mapping()?;
    let mut dns = Json::Null;
    if let Some(v) = row.get("dns").filter(|v| !v.is_null()) {
        if let Some(v) = v
            .as_mapping()?
            .get("nameserver-policy")
            .filter(|v| !v.is_null())
        {
            v.as_mapping()?;
            dns = object_value(v, true)?;
        }
    }
    serde_json::to_vec(&json!({
        "nodes":list(row.get("proxies"),proxy)?, "groups":list(row.get("proxy-groups"),group)?,
        "rules":strings(row,"rules")?, "mergedRules":strings(row,"smartvpn-source-rules")?,
        "sections":list(row.get("smartvpn-policy-sections"),section)?, "dns":dns
    }))
    .ok()
}
