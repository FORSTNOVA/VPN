use super::state::PersistedState;
use serde_json::{json, Value};
use smartvpn_engine::config_bridge::build_router_from_clash_yaml;

async fn dial_and_measure_yaml(
    yaml: &str,
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
    timeout_ms: u64,
) -> Result<(i64, i64), String> {
    if tokio::runtime::Handle::try_current().is_err() {
        return Err("no tokio handle in nodes.rs".to_owned());
    }
    smartvpn_engine::dialer::set_socket_protect_hook(protect);
    let bundle =
        build_router_from_clash_yaml(yaml, None).map_err(|e| format!("proxy config error: {e}"))?;
    measure(&bundle.router, timeout_ms).await
}

pub async fn measure(
    router: &smartvpn_engine::router::Router,
    timeout_ms: u64,
) -> Result<(i64, i64), String> {
    let budget = std::time::Duration::from_millis(timeout_ms.clamp(1000, 15000));
    let first_ms = router
        .probe_http_204("www.gstatic.com", 80, budget)
        .await
        .map_err(|e| format!("probe failed: {e}"))?;
    let steady_ms = router
        .probe_http_204("www.gstatic.com", 80, budget)
        .await
        .map_err(|e| format!("repeat probe failed: {e}"))?;
    Ok((first_ms, steady_ms))
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

pub fn path_router(
    state: &super::state::RuntimeState,
    hops: &[String],
    backups: &std::collections::BTreeMap<String, Vec<String>>,
) -> Result<std::sync::Arc<smartvpn_engine::router::Router>, String> {
    use smartvpn_engine::{router::Router, rule::RuleSet};
    use std::sync::Arc;
    let proxies = state.all_proxy_values();
    let first = hops.first().ok_or("empty probe path")?;
    if let Some(handle) = state.tun_handle {
        let target = if hops.len() == 1 && backups.is_empty() {
            Some(first.as_str())
        } else if state
            .probe_chain
            .as_ref()
            .is_some_and(|c| c.hops == hops && &c.backups == backups)
        {
            Some("SmartVPN")
        } else {
            None
        };
        if let Some(target) = target.filter(|_| state.probe_nodes == proxies) {
            let live = smartvpn_engine::ffi::tun_router(handle).map_err(|e| e.to_string())?;
            let dialer = live
                .outbounds
                .get(target)
                .ok_or("probe outbound unavailable")?;
            let rules = RuleSet::from_lines(&["MATCH,Probe"]).map_err(|e| e.to_string())?;
            return Ok(Arc::new(Router::new(
                rules,
                [("Probe".into(), dialer.clone())].into(),
            )));
        }
    }
    if hops.len() == 1 {
        let proxy = proxies
            .iter()
            .find(|p| p["name"].as_str() == Some(first))
            .ok_or("unknown probe node")?;
        let yaml = serde_yaml_ng::to_string(
            &json!({"mixed-port":0,"proxies":[proxy],"proxy-groups":[{"name":"SmartVPN","type":"select","proxies":[first]}],"rules":["MATCH,SmartVPN"]}),
        )
        .map_err(|e| e.to_string())?;
        return build_router_from_clash_yaml(&yaml, None)
            .map(|b| b.router)
            .map_err(|e| e.to_string());
    }
    let mut probe = PersistedState::default();
    probe.active_chain_id = "probe".into();
    probe.chains.push(super::state::ProxyChain {
        id: probe.active_chain_id.clone(),
        name: "测速".into(),
        hops: hops.to_vec(),
        sessions: state.persisted.chains.iter().find(|c| c.id == state.persisted.active_chain_id && c.hops == hops && &c.backups == backups).map(|c| c.sessions.clone()).unwrap_or_default(),
        backups: backups.clone(),
    });
    let yaml = super::tunnel::build_engine_yaml(&probe, &proxies)?;
    build_router_from_clash_yaml(&yaml, None)
        .map(|b| b.router)
        .map_err(|e| e.to_string())
}

pub fn infer_region_from_name(name: &str) -> &'static str {
    if name.contains("香港") || name.contains("HK") || name.contains("Hong Kong") {
        "HK"
    } else if name.contains("日本")
        || name.contains("JP")
        || name.contains("Tokyo")
        || name.contains("Osaka")
    {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn probe_protocol_configs_return_error_not_panic() {
        let samples = vec![
            json!({
                "name": "hy2-sample",
                "type": "hysteria2",
                "server": "127.0.0.1",
                "port": 1,
                "password": "secret",
                "sni": "example.com",
                "skip-cert-verify": true
            }),
            json!({
                "name": "vless-sample",
                "type": "vless",
                "server": "127.0.0.1",
                "port": 1,
                "uuid": "11111111-1111-1111-1111-111111111111",
                "network": "tcp",
                "tls": true,
                "servername": "example.com",
                "reality-opts": {
                    "public-key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
                    "short-id": "01234567"
                }
            }),
            json!({
                "name": "anytls-sample",
                "type": "anytls",
                "server": "127.0.0.1",
                "port": 1,
                "password": "secret",
                "sni": "example.com",
                "skip-cert-verify": true
            }),
        ];
        for p in samples {
            let res = probe_proxy_latency(&p, None, 1500).await;
            assert!(
                res.is_err(),
                "closed local port should fail cleanly: {:?}",
                res
            );
        }
    }
}
