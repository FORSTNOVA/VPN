use super::state::{PersistedState, RuntimeState, TunnelReport};
use serde_json::{json, Value};
use std::ffi::CString;

pub fn build_engine_yaml(persisted: &PersistedState, proxies: &[Value]) -> Result<String, String> {
    engine_yaml(persisted, proxies, &[])
}

fn engine_yaml(
    persisted: &PersistedState,
    proxies: &[Value],
    ranges: &[String],
) -> Result<String, String> {
    let mut active_proxies: Vec<Value> = proxies.to_vec();
    let mut main_target = persisted.selected_node.clone();

    let mut policy_groups = Vec::new();
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
        if !chain.backups.is_empty() {
            let mut stages = Vec::new();
            // A later hop's first check may need to wait for earlier candidates to time out.
            let mut budget = 6;
            for (hop, name) in chain.hops.iter().enumerate() {
                let available: Vec<_> = std::iter::once(name)
                    .chain(chain.backups.get(name).into_iter().flatten())
                    .filter(|n| {
                        proxies
                            .iter()
                            .any(|p| p["name"].as_str() == Some(n.as_str()))
                    })
                    .collect();
                if available.is_empty() {
                    return Err(format!(
                        "代理链「{}」第 {} 跳没有可用节点",
                        chain.name,
                        hop + 1
                    ));
                }
                let stage = format!("__smartvpn_policy_{index}_{hop}");
                policy_groups.push(json!({"name":stage,"type":"ordered-failover","proxies":available,"hop-timeout":budget}));
                budget += 8 * available.len();
                stages.push(stage);
            }
            let exit = format!("__smartvpn_relay_{index}");
            policy_groups.push(json!({"name":exit,"type":"relay","proxies":stages}));
            chain_exits.insert(id.to_owned(), exit.clone());
            return Ok(exit);
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

    let mut rules: Vec<String> = persisted.site_routes.iter().map(|r| r.clash_rule()).collect();
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
    if persisted.import_policy == "enabled" {
        rules.extend(
            super::subs::active_rules(persisted)
                .filter_map(|r| super::subs::applied_rule(persisted, r)),
        );
    }
    if persisted.domestic_web && !ranges.is_empty() {
        rules.push("GEOIP,CN,DIRECT".to_owned());
    }
    rules.push("MATCH,SmartVPN".to_owned());

    let mut group_members: Vec<String> = active_proxies
        .iter()
        .filter_map(|p| p.get("name").and_then(|n| n.as_str()).map(str::to_owned))
        .collect();
    if policy_groups
        .iter()
        .any(|group| group["name"] == main_target)
    {
        group_members = vec![main_target.clone()];
    }
    if group_members.is_empty() {
        group_members.push("DIRECT".to_owned());
    }

    // Move selected main_target to front of SmartVPN select group so config_bridge picks it.
    if let Some(pos) = group_members.iter().position(|n| n == &main_target) {
        let item = group_members.remove(pos);
        group_members.insert(0, item);
    }

    policy_groups.insert(
        0,
        json!({"name":"SmartVPN","type":"select","proxies":group_members}),
    );
    let doc = json!({
        "smartvpn-china-ranges": ranges,
        "smartvpn-block-stun": persisted.webrtc_protection,
        "mixed-port": 0,
        "dns": {
            "enable": persisted.dns_protection,
            "listen": "0.0.0.0:1053"
        },
        "proxies": active_proxies,
        "proxy-groups": policy_groups,
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
    let ranges = if state.persisted.domestic_web {
        &state.china.rows[..]
    } else {
        &[]
    };
    let mut profile = state.persisted.clone();
    if !profile.locked_region.is_empty() {
        let pool = super::regions::pool(state, &profile.locked_region);
        if let Some(chain) = state.active_chain() {
            let exit = chain.hops.last().ok_or("代理链缺少出口")?;
            if std::iter::once(exit)
                .chain(chain.backups.get(exit).into_iter().flatten())
                .any(|n| !pool.contains(n))
            {
                return Err("代理链出口或其备用节点未通过锁定地区验证".into());
            }
        } else if !pool.contains(&profile.selected_node) {
            profile.selected_node = pool.into_iter().next().ok_or("锁定地区没有已验证节点")?;
            state.persisted.selected_node = profile.selected_node.clone();
        }
    }
    let yaml = engine_yaml(&profile, &proxies, ranges)?;
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
    let router = smartvpn_engine::ffi::tun_router(handle).map_err(|e| e.to_string())?;
    state.traffic.attach(router.traffic.clone());
    state.china.active = !ranges.is_empty();
    state.connected = true;
    state.push_event(
        "tunnel",
        "纯 Rust TUN 已启动",
        "libsmartvpn_engine.so 已接管流量",
        true,
    );
    Ok(())
}

pub fn stop_tun(state: &mut RuntimeState) -> Result<(), String> {
    state.china.active = false;
    if let Some(handle) = state.tun_handle.take() {
        let rc = smartvpn_engine::ffi::smartvpn_engine_tun_stop_v1(handle);
        if rc != 0 {
            return Err(format!("failed to stop TUN: {rc}"));
        }
    }
    if state.connected {
        state.connected = false;
        state.push_event("tunnel", "代理已断开", "纯 Rust TUN 已停止", true);
    }
    state.traffic.finish()?;
    state.traffic_snapshot = json!({"available":false,"reason":"未连接"});
    Ok(())
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
        return stop_tun(state);
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
    fn website_routes_match_host_boundaries_and_child_exceptions() {
        use smartvpn_engine::rule::{RuleAction, RuleContext, RuleSet};
        let mut state = PersistedState::default();
        state.site_routes = super::super::site_routes::normalize(serde_json::from_value(json!([
            {"host":"EXAMPLE.org","action":"direct","includeSubdomains":true},
            {"host":"api.example.org","action":"proxy","includeSubdomains":false}
        ])).unwrap()).unwrap();
        let proxies = [json!({"name":"exit","type":"socks5","server":"127.0.0.1","port":1080})];
        state.selected_node = "exit".into();
        state.domestic_web = false;
        let yaml = build_engine_yaml(&state, &proxies).unwrap();
        let doc: Value = serde_yaml_ng::from_str(&yaml).unwrap();
        let lines: Vec<_> = doc["rules"].as_array().unwrap().iter().map(|v| v.as_str().unwrap()).collect();
        let rules = RuleSet::from_lines(&lines).unwrap();
        for (domain, direct) in [("example.org",true),("img.example.org",true),
            ("api.example.org",false),("x.api.example.org",true),
            ("notexample.org",false),("external.example",false)] {
            let expected = if direct { RuleAction::Direct } else { RuleAction::Proxy("SmartVPN".into()) };
            assert_eq!(rules.evaluate(&RuleContext { domain: Some(domain), ..Default::default() }),Some(&expected),"{domain}");
        }
    }
    #[test]
    fn saved_hop_policies_drive_global_and_app_rules_after_reload() {
        let mut state = PersistedState::default();
        state.chains = serde_json::from_value(json!([{
            "id": "policy", "name": "route", "hops": ["A", "B"],
            "backups": {"A": ["C"], "B": ["D"]}
        }]))
        .unwrap();
        state.active_chain_id = "policy".into();
        state.app_groups = serde_json::from_value(json!([{
            "id": "app", "name":"app", "apps":["app.test"], "enabled":true,
            "targetType":"chain", "targetChainId":"policy"
        }]))
        .unwrap();
        let restored = serde_json::from_slice(&serde_json::to_vec(&state).unwrap()).unwrap();
        let proxies: Vec<_> = ["A", "B", "C", "D"].iter().map(|name| json!({
            "name": name, "type":"trojan", "server":"localhost", "port":443, "password":"test"
        })).collect();
        let yaml = build_engine_yaml(&restored, &proxies).unwrap();
        let doc: Value = serde_yaml_ng::from_str(&yaml).unwrap();
        let groups = &doc["proxy-groups"];
        assert_eq!(groups[1]["proxies"], json!(["A", "C"]));
        assert_eq!(groups[2]["proxies"], json!(["B", "D"]));
        assert_eq!(
            groups[3]["proxies"],
            json!([groups[1]["name"], groups[2]["name"]])
        );
        assert_eq!(groups[0]["proxies"], json!([groups[3]["name"]]));
        assert_eq!(
            doc["rules"][0],
            format!(
                "PROCESS-NAME,app.test,{}",
                groups[3]["name"].as_str().unwrap()
            )
        );
        smartvpn_engine::config_bridge::build_router_from_clash_yaml(&yaml, None).unwrap();
        assert!(build_engine_yaml(&restored, &proxies[..1]).is_err());
    }

    #[test]
    fn app_groups_share_chain_and_preserve_global_route() {
        let chain = |id: &str, hops: &[&str]| ProxyChain {
            backups: Default::default(),
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
        state.domestic_web = true;
        let yaml = engine_yaml(&state, &proxies, &["223.5.5.0/24".into()]).unwrap();
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
        let domestic = "223.5.5.5".parse().unwrap();
        for (process, expected) in [
            (
                "app.a",
                RuleAction::Proxy(nodes[5]["name"].as_str().unwrap().into()),
            ),
            ("other.app", RuleAction::Direct),
        ] {
            let context = RuleContext {
                process_name: Some(process),
                ip: Some(domestic),
                domain: None,
            };
            assert_eq!(engine.router.rules.evaluate(&context), Some(&expected));
        }
        state.chains[1].hops[1] = "missing".into();
        assert!(build_engine_yaml(&state, &proxies)
            .unwrap_err()
            .contains("missing"));
    }
}
