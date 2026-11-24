use super::state::{PersistedState, RuntimeState, TunnelReport};
use serde_json::{json, Value};
use std::ffi::CString;

pub fn build_engine_yaml(persisted: &PersistedState, proxies: &[Value]) -> Result<String, String> {
    let mut active_proxies: Vec<Value> = proxies.to_vec();
    let mut main_target = persisted.selected_node.clone();

    if let Some(chain) = persisted
        .chains
        .iter()
        .find(|c| c.id == persisted.active_chain_id)
    {
        if !chain.hops.is_empty() {
            let mut prev_name: Option<String> = None;
            for (idx, hop_name) in chain.hops.iter().enumerate() {
                if let Some(base) = proxies
                    .iter()
                    .find(|p| p.get("name").and_then(|n| n.as_str()) == Some(hop_name.as_str()))
                {
                    let hop_alias = if idx == 0 {
                        hop_name.clone()
                    } else {
                        let alias = format!("__chain_hop_{idx}_{hop_name}");
                        let mut clone = base.clone();
                        if let Some(obj) = clone.as_object_mut() {
                            obj.insert("name".into(), json!(alias));
                            if let Some(prev) = &prev_name {
                                obj.insert("dialer-proxy".into(), json!(prev));
                            }
                        }
                        active_proxies.push(clone);
                        alias
                    };
                    prev_name = Some(hop_alias);
                }
            }
            if let Some(last) = prev_name {
                main_target = last;
            }
        }
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
            _ => continue,
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
    state.push_event("tunnel", "纯 Rust TUN 已启动", "libsmartvpn_engine.so 已接管流量", true);
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
