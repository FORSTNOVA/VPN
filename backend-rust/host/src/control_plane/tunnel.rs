use super::state::{PersistedState, RuntimeState, TunnelReport};
use serde_json::{json, Value};
use std::ffi::CString;

pub fn build_engine_yaml(persisted: &PersistedState, proxies: &[Value]) -> Result<String, String> {
    let mut active_proxies: Vec<Value> = proxies.to_vec();
    let mut main_target = persisted.selected_node.clone();

    let mut chain_exits = std::collections::HashMap::new();
    let mut chain_exit = |id: &str| -> Result<String, String> {
        if let Some(exit) = chain_exits.get(id) {
            return Ok(String::clone(exit));
        }
        let (index, chain) = persisted
            .chains
            .iter()
            .enumerate()
            .find(|(_, c)| c.id == id)
            .ok_or_else(|| format!("找不到代理链 {id}"))?;
        if !(2..=8).contains(&chain.hops.len()) {
            return Err(format!("代理链「{}」需要 2 至 8 跳", chain.name));
        }
        let mut previous = chain.hops[0].clone();
        for (hop, name) in chain.hops.iter().enumerate() {
            let base = proxies
                .iter()
                .find(|p| p["name"].as_str() == Some(name))
                .ok_or_else(|| {
                    format!(
                        "代理链「{}」缺少第 {} 跳节点「{name}」",
                        chain.name,
                        hop + 1
                    )
                })?;
            if hop > 0 {
                let alias = format!("__smartvpn_chain_{index}_hop_{hop}");
                if active_proxies.iter().any(|p| p["name"] == alias) {
                    return Err(format!("代理链内部节点名冲突：{alias}"));
                }
                let mut copy = base.clone();
                copy["name"] = json!(alias);
                copy["dialer-proxy"] = json!(previous);
                active_proxies.push(copy);
                previous = alias;
            }
        }
        chain_exits.insert(id.to_owned(), previous.clone());
        Ok(previous)
    };
    if !persisted.active_chain_id.is_empty() {
        main_target = chain_exit(&persisted.active_chain_id)?;
    }

    if main_target.is_empty() {
        main_target = proxies
            .first()
            .and_then(|p| p.get("name").and_then(|n| n.as_str()))
            .unwrap_or("DIRECT")
            .to_owned();
    }

    let mut rules: Vec<String> = Vec::new();
    for g in persisted.app_groups.iter().filter(|g| g.enabled) {
        let target = match g.target_type.as_str() {
            "direct" => "DIRECT".to_owned(),
            "reject" => "REJECT".to_owned(),
            "node" if !g.target_node.trim().is_empty() => g.target_node.clone(),
            "chain" => chain_exit(&g.target_chain_id)?,
            other => return Err(format!("分组「{}」的路由目标无效：{other}", g.name)),
        };
        for app in &g.apps {
            rules.push(format!("PROCESS-NAME,{app},{target}"));
        }
    }
    for game in &persisted.game_bypass {
        let g = game.trim();
        if !g.is_empty() {
            rules.push(format!("PROCESS-NAME,{g},DIRECT"));
        }
    }
    if persisted.domestic_web {
        rules.push("GEOIP,CN,DIRECT".to_owned());
    }
    rules.push("MATCH,SmartVPN".to_owned());

    let mut group_members: Vec<String> = active_proxies
        .iter()
        .filter_map(|p| p.get("name").and_then(|n| n.as_str()).map(str::to_owned))
        .collect();
    if group_members.is_empty() {
        group_members.push("DIRECT".to_owned());
    }

    // Move selected main_target to front of SmartVPN select group so config_bridge picks it.
    if let Some(pos) = group_members.iter().position(|n| n == &main_target) {
        let item = group_members.remove(pos);
        group_members.insert(0, item);
    }

    let doc = json!({
        "mixed-port": 0,
        "dns": {
            "enable": persisted.dns_protection,
            "listen": "0.0.0.0:1053"
        },
        "proxies": active_proxies,
        "proxy-groups": [{
            "name": "SmartVPN",
            "type": "select",
            "proxies": group_members
        }],
        "rules": rules
    });

    serde_yaml_ng::to_string(&doc).map_err(|e| format!("YAML serialize failed: {e}"))
}

pub fn start_tun(state: &mut RuntimeState) -> Result<(), String> {
    let Some(fd) = state.tun_fd else {
        return Err("VPN tunnel is not authorized yet".to_owned());
    };
    if state.tun_handle.is_some() {
        return Ok(());
    }
    let proxies = state.all_proxy_values();
    if proxies.is_empty() {
        return Err("no proxy nodes available; add or refresh a subscription first".to_owned());
    }
    let yaml = build_engine_yaml(&state.persisted, &proxies)?;
    let c_yaml = CString::new(yaml).map_err(|_| "invalid YAML NUL byte".to_owned())?;
    let protect = state.bridge.protect_hook();
    let resolve = state.bridge.resolve_hook();
    let handle = unsafe {
        smartvpn_engine::ffi::smartvpn_engine_tun_start_v1(fd, c_yaml.as_ptr(), protect, resolve)
    };
    if handle <= 0 {
        return Err(format!("pure-Rust TUN engine start failed (code {handle})"));
    }
    state.tun_handle = Some(handle);
    state.connected = true;
    state.push_event(
        "tunnel",
        "纯 Rust TUN 已启动",
        "libsmartvpn_engine.so 已接管流量",
        true,
    );
    Ok(())
}

pub fn stop_tun(state: &mut RuntimeState) {
    if let Some(handle) = state.tun_handle.take() {
        smartvpn_engine::ffi::smartvpn_engine_tun_stop_v1(handle);
    }
    if state.connected {
        state.connected = false;
        state.push_event("tunnel", "代理已断开", "纯 Rust TUN 已停止", true);
    }
}

pub fn apply_tunnel_report(
    state: &mut RuntimeState,
    authorized: bool,
    fd: i32,
    info_json: &str,
) -> Result<(), String> {
    if !authorized || fd < 0 {
        state.tun_fd = None;
        state.tunnel_report = None;
        stop_tun(state);
        return Ok(());
    }
    let report: TunnelReport = serde_json::from_str(info_json).unwrap_or_default();
    state.tun_fd = Some(fd);
    state.tunnel_report = Some(report);
    if state.connected && state.tun_handle.is_none() {
        start_tun(state)?;
    }
    Ok(())
}

pub fn tun_status_json(state: &RuntimeState) -> Value {
    json!({
        "available": true,
        "active": state.connected && state.tun_handle.is_some(),
        "reason": "",
        "mode": state.persisted.connection_mode,
        "rustEngine": true,
        "interface": state.tunnel_report.as_ref().map(|r| r.interface.as_str()).unwrap_or(""),
        "routes": state.tunnel_report.as_ref().map(|r| &r.routes).cloned().unwrap_or_default(),
        "dns": state.tunnel_report.as_ref().map(|r| &r.dns).cloned().unwrap_or_default(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::control_plane::state::ProxyChain;

    #[test]
    fn app_groups_share_chain_and_preserve_global_route() {
        let chain = |id: &str, hops: &[&str]| ProxyChain {
            id: id.into(),
            name: id.into(),
            hops: hops.iter().map(|s| s.to_string()).collect(),
        };
        let mut state = PersistedState::default();
        state.active_chain_id = "global".into();
        state.chains = vec![
            chain("global", &["A", "C"]),
            chain("apps", &["B", "A", "C"]),
        ];
        state.app_groups = ["app.a", "app.b"]
            .iter()
            .map(|app| {
                serde_json::from_value(json!({
                    "id": app, "name": app, "enabled": true, "apps": [app],
                    "targetType": "chain", "targetChainId": "apps"
                }))
                .unwrap()
            })
            .collect();
        let proxies: Vec<_> = ["A", "B", "C"].iter().map(|name| json!({
            "name": name, "type": "trojan", "server": "localhost", "port": 443, "password": "fixture"
        })).collect();
        let yaml = build_engine_yaml(&state, &proxies).unwrap();
        let engine =
            smartvpn_engine::config_bridge::build_router_from_clash_yaml(&yaml, None).unwrap();
        let doc: Value = serde_yaml_ng::from_str(&yaml).unwrap();
        let nodes = doc["proxies"].as_array().unwrap();
        assert_eq!(nodes.len(), 6);
        assert_eq!(nodes[3]["dialer-proxy"], "A");
        assert_eq!(nodes[4]["dialer-proxy"], "B");
        assert_eq!(nodes[5]["dialer-proxy"], nodes[4]["name"]);
        assert_eq!(doc["proxy-groups"][0]["proxies"][0], nodes[3]["name"]);
        for (i, app) in ["app.a", "app.b"].iter().enumerate() {
            assert_eq!(
                doc["rules"][i],
                format!("PROCESS-NAME,{app},{}", nodes[5]["name"].as_str().unwrap())
            );
        }
        use smartvpn_engine::rule::{RuleAction, RuleContext};
        for (process, target) in [
            ("app.a", nodes[5]["name"].as_str().unwrap()),
            ("app.b", nodes[5]["name"].as_str().unwrap()),
            ("other.app", "SmartVPN"),
        ] {
            let context = RuleContext {
                process_name: Some(process),
                ..Default::default()
            };
            assert_eq!(
                engine.router.rules.evaluate(&context),
                Some(&RuleAction::Proxy(target.into()))
            );
        }
        state.chains[1].hops[1] = "missing".into();
        assert!(build_engine_yaml(&state, &proxies)
            .unwrap_err()
            .contains("missing"));
    }
}
