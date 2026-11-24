use super::state::PersistedState;
use serde_json::{json, Value};
use smartvpn_engine::config_bridge::build_router_from_clash_yaml;

async fn dial_and_measure_yaml(
    yaml: &str,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    timeout_ms: u64,
) -> Result<(i64, i64), String> {
    smartvpn_engine::dialer::set_socket_protect_hook(protect);
    let bundle = build_router_from_clash_yaml(yaml, None)
        .map_err(|e| format!("proxy config error: {e}"))?;
    let budget = std::time::Duration::from_millis(timeout_ms.clamp(1000, 15000));
    let first_ms = bundle
        .router
        .probe_http_204("www.google.com", 80, budget)
        .await
        .map_err(|e| format!("probe failed: {e}"))?;
    Ok((first_ms, (first_ms / 3).max(1)))
}

pub async fn probe_proxy_latency(
    proxy: &Value,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    timeout_ms: u64,
) -> Result<(i64, i64), String> {
    let name = proxy
        .get("name")
        .and_then(|n| n.as_str())
        .ok_or_else(|| "missing proxy name".to_owned())?;
    let yaml_doc = json!({
        "mixed-port": 0,
        "proxies": [proxy],
        "proxy-groups": [{
            "name": "SmartVPN",
            "type": "select",
            "proxies": [name]
        }],
        "rules": ["MATCH,SmartVPN"]
    });
    let yaml = serde_yaml_ng::to_string(&yaml_doc).map_err(|e| e.to_string())?;
    dial_and_measure_yaml(&yaml, protect, timeout_ms).await
}

pub async fn probe_chain_latency(
    _persisted: &PersistedState,
    proxies: &[Value],
    hops: &[String],
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    timeout_ms: u64,
) -> Result<(i64, i64), String> {
    if hops.is_empty() {
        return Err("empty proxy chain".to_owned());
    }
    if hops.len() == 1 {
        let p = proxies
            .iter()
            .find(|p| p.get("name").and_then(|n| n.as_str()) == Some(hops[0].as_str()))
            .ok_or_else(|| format!("node not found: {}", hops[0]))?;
        return probe_proxy_latency(p, protect, timeout_ms).await;
    }
    let mut cloned_proxies = Vec::new();
    let mut prev_name: Option<String> = None;
    for (idx, hop_name) in hops.iter().enumerate() {
        let base = proxies
            .iter()
            .find(|p| p.get("name").and_then(|n| n.as_str()) == Some(hop_name.as_str()))
            .ok_or_else(|| format!("hop not found: {hop_name}"))?;
        let mut clone = base.clone();
        let alias = format!("__probe_hop_{idx}");
        if let Some(obj) = clone.as_object_mut() {
            obj.insert("name".into(), json!(alias));
            if let Some(prev) = &prev_name {
                obj.insert("dialer-proxy".into(), json!(prev));
            }
        }
        cloned_proxies.push(clone);
        prev_name = Some(alias);
    }
    let last_alias = prev_name.unwrap();
    let yaml_doc = json!({
        "mixed-port": 0,
        "proxies": cloned_proxies,
        "proxy-groups": [{
            "name": "SmartVPN",
            "type": "select",
            "proxies": [last_alias]
        }],
        "rules": ["MATCH,SmartVPN"]
    });
    let yaml = serde_yaml_ng::to_string(&yaml_doc).map_err(|e| e.to_string())?;
    dial_and_measure_yaml(&yaml, protect, timeout_ms).await
}

pub fn infer_region_from_name(name: &str) -> &'static str {
    if name.contains("香港") || name.contains("HK") || name.contains("Hong Kong") {
        "HK"
    } else if name.contains("日本") || name.contains("JP") || name.contains("Tokyo") || name.contains("Osaka") {
        "JP"
    } else if name.contains("新加坡") || name.contains("SG") || name.contains("Singapore") {
        "SG"
    } else if name.contains("台湾") || name.contains("TW") || name.contains("Taiwan") {
        "TW"
    } else if name.contains("美国") || name.contains("US") || name.contains("America") {
        "US"
    } else if name.contains("韩国") || name.contains("KR") || name.contains("Korea") {
        "KR"
    } else {
        ""
    }
}
