pub mod nodes;
pub mod state;
pub mod subs;
pub mod tunnel;

use bytes::Bytes;
use http_body_util::{BodyExt, Full, Limited};
use hyper::{
    body::Incoming,
    header::{AUTHORIZATION, CONTENT_TYPE},
    Method, Request, Response, StatusCode,
};
use hyper_util::rt::TokioIo;
use rand::RngCore;
use serde_json::{json, Value};
use state::{
    validate_app_groups, AppGroup, HealthParams, ManualNode, PathChecksConfig, PlatformBridge,
    ProxyChain, RuntimeState, SavedSubscription,
};
use std::{
    collections::BTreeMap,
    convert::Infallible,
    net::Ipv4Addr,
    path::PathBuf,
    sync::Arc,
};
use tokio::{
    net::TcpListener,
    sync::{oneshot, Mutex},
};

pub struct ControlPlane {
    pub port: u16,
    pub token: String,
    pub state: Arc<Mutex<RuntimeState>>,
    shutdown_tx: Mutex<Option<oneshot::Sender<()>>>,
}

impl ControlPlane {
    pub async fn start(home: PathBuf, bridge: Arc<dyn PlatformBridge>) -> Result<Arc<Self>, String> {
        smartvpn_engine::transport::tls::ensure_crypto_provider();
        let mut token_bytes = [0u8; 24];
        rand::rng().fill_bytes(&mut token_bytes);
        let token: String = token_bytes.iter().map(|b| format!("{b:02x}")).collect();

        let state = Arc::new(Mutex::new(RuntimeState::load(&home, bridge)));
        let listener = TcpListener::bind((Ipv4Addr::LOCALHOST, 0))
            .await
            .map_err(|e| format!("bind loopback failed: {e}"))?;
        let port = listener
            .local_addr()
            .map_err(|e| format!("local_addr failed: {e}"))?
            .port();

        let (shutdown_tx, mut shutdown_rx) = oneshot::channel::<()>();
        let cp = Arc::new(Self {
            port,
            token,
            state,
            shutdown_tx: Mutex::new(Some(shutdown_tx)),
        });

        let cp_srv = Arc::clone(&cp);
        tokio::spawn(async move {
            loop {
                tokio::select! {
                    _ = &mut shutdown_rx => break,
                    accepted = listener.accept() => {
                        let Ok((stream, _)) = accepted else { continue };
                        let cp_conn = Arc::clone(&cp_srv);
                        tokio::spawn(async move {
                            let svc = hyper::service::service_fn(move |req| {
                                let cp_req = Arc::clone(&cp_conn);
                                async move { Ok::<_, Infallible>(cp_req.handle(req).await) }
                            });
                            let _ = hyper::server::conn::http1::Builder::new()
                                .serve_connection(TokioIo::new(stream), svc)
                                .await;
                        });
                    }
                }
            }
        });

        Ok(cp)
    }

    pub async fn shutdown(&self) {
        {
            let mut st = self.state.lock().await;
            tunnel::stop_tun(&mut st);
            let _ = st.save();
        }
        if let Some(tx) = self.shutdown_tx.lock().await.take() {
            let _ = tx.send(());
        }
    }

    pub async fn set_tunnel(
        &self,
        authorized: bool,
        fd: i32,
        info_json: &str,
    ) -> Result<(), String> {
        let mut st = self.state.lock().await;
        tunnel::apply_tunnel_report(&mut st, authorized, fd, info_json)
    }

    async fn handle(&self, req: Request<Incoming>) -> Response<Full<Bytes>> {
        let auth_ok = req
            .headers()
            .get(AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .and_then(|s| s.strip_prefix("Bearer "))
            .is_some_and(|t| t == self.token);
        if !auth_ok {
            return text_resp(StatusCode::UNAUTHORIZED, "unauthorized\n");
        }

        let method = req.method().clone();
        let path = req.uri().path().to_owned();
        let query = req.uri().query().unwrap_or("").to_owned();
        let body = match Limited::new(req.into_body(), 2 << 20).collect().await {
            Ok(collected) => collected.to_bytes(),
            Err(_) => return text_resp(StatusCode::BAD_REQUEST, "body too large\n"),
        };

        match (method, path.as_str()) {
            (Method::GET, "/api/state") => {
                let st = self.state.lock().await;
                json_resp(StatusCode::OK, st.state_json())
            }
            (Method::PUT, "/api/settings") => self.handle_update_settings(&body).await,
            (Method::POST, "/api/connect") => self.handle_connect().await,
            (Method::POST, "/api/disconnect") => {
                let mut st = self.state.lock().await;
                tunnel::stop_tun(&mut st);
                json_resp(StatusCode::OK, json!({"ok": true}))
            }
            (Method::POST, "/api/shutdown") => {
                let mut st = self.state.lock().await;
                tunnel::stop_tun(&mut st);
                let _ = st.save();
                json_resp(StatusCode::OK, json!({"ok": true}))
            }
            (Method::POST, "/api/proxy/reapply") | (Method::POST, "/api/direct/refresh") => {
                json_resp(StatusCode::OK, json!({"ok": true}))
            }
            (Method::GET, "/api/tun") => {
                let st = self.state.lock().await;
                json_resp(StatusCode::OK, tunnel::tun_status_json(&st))
            }
            (Method::POST, "/api/tun/check") => {
                let st = self.state.lock().await;
                let ok = st.connected && st.tun_handle.is_some();
                json_resp(
                    StatusCode::OK,
                    json!({
                        "ok": ok,
                        "rustEngine": true,
                        "interface": st.tunnel_report.as_ref().map(|r| r.interface.as_str()).unwrap_or(""),
                        "message": if ok { "纯 Rust TUN 路径正常" } else { "未连接" }
                    }),
                )
            }
            (Method::GET, "/api/nodes") => {
                let st = self.state.lock().await;
                json_resp(StatusCode::OK, self.nodes_payload(&st))
            }
            (Method::POST, "/api/nodes/refresh") | (Method::POST, "/api/nodes/fetch") => {
                self.handle_nodes_fetch().await
            }
            (Method::PUT, "/api/nodes/select") => self.handle_select_node(&body).await,
            (Method::POST, "/api/nodes/manual") => self.handle_add_manual_node(&body).await,
            (Method::DELETE, "/api/nodes/manual") => self.handle_delete_manual_node(&query).await,
            (Method::PUT, "/api/nodes/chain") => self.handle_set_chain(&body).await,
            (Method::PUT, "/api/nodes/chain/profile") => self.handle_save_chain_profile(&body).await,
            (Method::POST, "/api/nodes/chain/probe") => self.handle_probe_chain(&body).await,
            (Method::GET, "/api/nodes/probes") => {
                let st = self.state.lock().await;
                let results: Vec<Value> = st.chain_probes.values().cloned().collect();
                json_resp(StatusCode::OK, json!({"results": results}))
            }
            (Method::POST, "/api/speedtest") => self.handle_speedtest(&body).await,
            (Method::POST, "/api/diagnose") => self.handle_diagnose().await,
            (Method::GET, "/api/subscriptions") => {
                let st = self.state.lock().await;
                json_resp(
                    StatusCode::OK,
                    json!({
                        "subscriptions": st.persisted.subscriptions,
                        "merged": true,
                        "autoMergeHours": st.persisted.auto_merge_hours,
                    }),
                )
            }
            (Method::POST, "/api/subscriptions/add") => self.handle_sub_add(&body).await,
            (Method::POST, "/api/subscriptions/update") => self.handle_sub_update(&body).await,
            (Method::DELETE, "/api/subscriptions") => self.handle_sub_delete(&query).await,
            (Method::POST, "/api/subscriptions/activate") => self.handle_sub_activate(&body).await,
            (Method::POST, "/api/subscriptions/merge") => self.handle_sub_merge().await,
            (Method::POST, "/api/subscriptions/auto-merge") => self.handle_sub_auto_merge(&body).await,
            (Method::POST, "/api/subscriptions/refresh") => self.handle_sub_refresh(&body).await,
            (Method::POST, "/api/subscriptions/quota") => self.handle_sub_quota(&body).await,
            (Method::GET, "/api/diagnostics/subscription") => {
                self.handle_sub_diagnostics().await
            }
            (Method::GET, "/api/subscription/policy") => {
                let st = self.state.lock().await;
                json_resp(
                    StatusCode::OK,
                    json!({"importPolicy": st.persisted.import_policy}),
                )
            }
            (Method::GET, "/api/regions") => {
                let st = self.state.lock().await;
                json_resp(StatusCode::OK, self.regions_payload(&st))
            }
            (Method::POST, "/api/regions/verify") => self.handle_regions_verify(&body).await,
            (Method::POST, "/api/region/lock") => self.handle_region_lock(&body).await,
            (Method::POST, "/api/health/patrol") => {
                let st = self.state.lock().await;
                json_resp(StatusCode::OK, json!({"ok": true, "health": st.state_json()["health"]}))
            }
            (Method::PUT, "/api/health/params") => self.handle_health_params(&body).await,
            (Method::GET, "/api/path-checks") => {
                let st = self.state.lock().await;
                json_resp(
                    StatusCode::OK,
                    json!({
                        "settings": st.persisted.path_checks,
                        "status": {"running": false},
                        "limits": {"minIntervalSec": 10}
                    }),
                )
            }
            (Method::PUT, "/api/path-checks") => self.handle_put_path_checks(&body).await,
            (Method::POST, "/api/path-checks/run") => self.handle_run_path_checks().await,
            (Method::POST, "/api/path-checks/recovery/cancel") => {
                json_resp(StatusCode::OK, json!({"ok": true}))
            }
            (Method::GET, "/api/traffic") => {
                let st = self.state.lock().await;
                json_resp(
                    StatusCode::OK,
                    json!({
                        "available": st.connected,
                        "reason": if st.connected { "" } else { "内核未运行：连接与流量监控只在连接期间有意义" },
                        "up": st.tx_bytes,
                        "down": st.rx_bytes,
                        "connections": []
                    }),
                )
            }
            (Method::GET, "/api/traffic/history") => {
                json_resp(
                    StatusCode::OK,
                    json!({
                        "upload": 0,
                        "download": 0,
                        "total": 0,
                        "connections": 0,
                        "apps": [],
                        "recent": []
                    }),
                )
            }
            (Method::GET, "/api/events") => {
                let st = self.state.lock().await;
                json_resp(
                    StatusCode::OK,
                    json!({
                        "events": st.persisted.events,
                        "logs": st.persisted.events,
                    }),
                )
            }
            _ => text_resp(StatusCode::NOT_FOUND, "not found\n"),
        }
    }

    async fn handle_update_settings(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(patch) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let Some(obj) = patch.as_object() else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let mut st = self.state.lock().await;
        if let Some(groups_val) = obj.get("appGroups") {
            let Ok(groups) = serde_json::from_value::<Vec<AppGroup>>(groups_val.clone()) else {
                return text_resp(StatusCode::BAD_REQUEST, "invalid appGroups\n");
            };
            match validate_app_groups(groups) {
                Ok(valid) => {
                    if st.connected {
                        return text_resp(StatusCode::CONFLICT, "disconnect before updating appGroups\n");
                    }
                    st.persisted.app_groups = valid;
                }
                Err(msg) => return text_resp(StatusCode::BAD_REQUEST, &format!("{msg}\n")),
            }
        }
        if let Some(v) = obj.get("subscriptionUrl").and_then(|v| v.as_str()) {
            st.persisted.subscription_url = v.trim().to_owned();
        }
        if let Some(v) = obj.get("importPolicy").and_then(|v| v.as_str()) {
            st.persisted.import_policy = v.to_owned();
        }
        if let Some(v) = obj.get("connectionMode").and_then(|v| v.as_str()) {
            st.persisted.connection_mode = v.to_owned();
        }
        if let Some(v) = obj.get("domesticWeb").and_then(|v| v.as_bool()) {
            st.persisted.domestic_web = v;
        }
        if let Some(v) = obj.get("webRTCProtection").and_then(|v| v.as_bool()) {
            st.persisted.webrtc_protection = v;
        }
        if let Some(v) = obj.get("dnsProtection").and_then(|v| v.as_bool()) {
            st.persisted.dns_protection = v;
        }
        if let Some(v) = obj.get("strictPrivacy").and_then(|v| v.as_bool()) {
            st.persisted.strict_privacy = v;
        }
        if let Some(v) = obj.get("socksPort").and_then(|v| v.as_u64()) {
            st.persisted.socks_port = v as u16;
        }
        if let Some(v) = obj.get("gameBypass").and_then(|v| v.as_array()) {
            st.persisted.game_bypass = v
                .iter()
                .filter_map(|x| x.as_str().map(str::trim).filter(|s| !s.is_empty()).map(str::to_owned))
                .collect();
        }
        if let Err(e) = st.save() {
            return text_resp(StatusCode::INTERNAL_SERVER_ERROR, &format!("{e}\n"));
        }
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_connect(&self) -> Response<Full<Bytes>> {
        let mut st = self.state.lock().await;
        if let Err(e) = tunnel::start_tun(&mut st) {
            return text_resp(StatusCode::BAD_REQUEST, &format!("{e}\n"));
        }
        json_resp(StatusCode::OK, json!({"ok": true, "connected": true}))
    }

    fn nodes_payload(&self, st: &RuntimeState) -> Value {
        let proxies = st.all_proxy_values();
        let list: Vec<Value> = proxies
            .iter()
            .filter_map(|p| {
                let name = p.get("name")?.as_str()?;
                let kind = p.get("type").and_then(|t| t.as_str()).unwrap_or("proxy");
                let delay = st.node_delays.get(name).copied().unwrap_or(0);
                let first_delay = st.node_first_delays.get(name).copied().unwrap_or(delay);
                let region = st
                    .node_regions
                    .get(name)
                    .map(String::as_str)
                    .unwrap_or_else(|| nodes::infer_region_from_name(name));
                Some(json!({
                    "name": name,
                    "type": kind,
                    "delay": delay,
                    "firstDelay": first_delay,
                    "region": region,
                    "manual": st.persisted.manual_nodes.iter().any(|m| m.name == name),
                    "source": "merged"
                }))
            })
            .collect();
        let chains_map: BTreeMap<&str, &Vec<String>> = st
            .persisted
            .chains
            .iter()
            .map(|c| (c.name.as_str(), &c.hops))
            .collect();
        let active_chain = st.active_chain();
        json!({
            "nodes": list,
            "selectedNode": st.persisted.selected_node,
            "chain": {
                "enabled": active_chain.is_some_and(|c| !c.hops.is_empty()),
                "name": active_chain.map(|c| c.name.as_str()).unwrap_or(""),
                "hops": active_chain.map(|c| c.hops.clone()).unwrap_or_default(),
            },
            "chains": chains_map,
        })
    }

    async fn handle_nodes_fetch(&self) -> Response<Full<Bytes>> {
        let (url, protect) = {
            let st = self.state.lock().await;
            (st.persisted.subscription_url.clone(), st.active_protect_hook())
        };
        if url.trim().is_empty() {
            return self.handle_sub_merge().await;
        }
        match subs::fetch_subscription_text(&url, protect).await {
            Ok((text, _)) => match subs::parse_proxies_from_payload(&text) {
                Ok(proxies) => {
                    let mut st = self.state.lock().await;
                    st.persisted.merged_proxies = proxies;
                    if st.persisted.selected_node.is_empty() {
                        if let Some(first) = st
                            .persisted
                            .merged_proxies
                            .first()
                            .and_then(|p| p.get("name").and_then(|n| n.as_str()))
                        {
                            st.persisted.selected_node = first.to_owned();
                        }
                    }
                    let _ = st.save();
                    json_resp(StatusCode::OK, self.nodes_payload(&st))
                }
                Err(e) => text_resp(StatusCode::BAD_REQUEST, &format!("{e}\n")),
            },
            Err(e) => text_resp(StatusCode::BAD_GATEWAY, &format!("{e}\n")),
        }
    }

    async fn handle_select_node(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let Some(name) = val.get("name").and_then(|n| n.as_str()) else {
            return text_resp(StatusCode::BAD_REQUEST, "missing name\n");
        };
        let mut st = self.state.lock().await;
        st.persisted.selected_node = name.to_owned();
        st.persisted.active_chain_id.clear();
        st.push_event("switch", "切换节点", name, true);
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_add_manual_node(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let uri = val.get("uri").and_then(|u| u.as_str()).unwrap_or("").trim();
        let Some(mut proxy) = subs::parse_proxy_uri(uri) else {
            return text_resp(StatusCode::BAD_REQUEST, "unsupported node URI\n");
        };
        if let Some(name) = val.get("name").and_then(|n| n.as_str()).filter(|s| !s.trim().is_empty()) {
            if let Some(obj) = proxy.as_object_mut() {
                obj.insert("name".into(), json!(name.trim()));
            }
        }
        let final_name = proxy
            .get("name")
            .and_then(|n| n.as_str())
            .unwrap_or("manual")
            .to_owned();
        let mut st = self.state.lock().await;
        st.persisted.manual_nodes.retain(|m| m.name != final_name);
        st.persisted.manual_nodes.push(ManualNode {
            name: final_name,
            uri: uri.to_owned(),
            proxy,
        });
        let _ = st.save();
        json_resp(StatusCode::OK, self.nodes_payload(&st))
    }

    async fn handle_delete_manual_node(&self, query: &str) -> Response<Full<Bytes>> {
        let name = query_param(query, "name").unwrap_or_default();
        let mut st = self.state.lock().await;
        st.persisted.manual_nodes.retain(|m| m.name != name);
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_set_chain(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let mut st = self.state.lock().await;
        if let Some(id) = val.get("id").and_then(|v| v.as_str()) {
            st.persisted.active_chain_id = id.to_owned();
        } else if let Some(hops) = val.get("hops").and_then(|v| v.as_array()) {
            let clean: Vec<String> = hops
                .iter()
                .filter_map(|h| h.as_str().map(str::to_owned))
                .collect();
            if clean.is_empty() {
                st.persisted.active_chain_id.clear();
            } else {
                let id = "default-chain".to_owned();
                st.persisted.chains.retain(|c| c.id != id);
                st.persisted.chains.push(ProxyChain {
                    id: id.clone(),
                    name: clean.join(" → "),
                    hops: clean,
                });
                st.persisted.active_chain_id = id;
            }
        }
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_save_chain_profile(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let name = val.get("name").and_then(|v| v.as_str()).unwrap_or("Chain").trim().to_owned();
        let hops: Vec<String> = val
            .get("hops")
            .and_then(|v| v.as_array())
            .map(|a| a.iter().filter_map(|x| x.as_str().map(str::to_owned)).collect())
            .unwrap_or_default();
        if hops.is_empty() {
            return text_resp(StatusCode::BAD_REQUEST, "chain hops required\n");
        }
        let id = val
            .get("id")
            .and_then(|v| v.as_str())
            .filter(|s| !s.is_empty())
            .map(str::to_owned)
            .unwrap_or_else(|| format!("chain-{}", chrono::Utc::now().timestamp_millis()));
        let mut st = self.state.lock().await;
        st.persisted.chains.retain(|c| c.id != id);
        st.persisted.chains.push(ProxyChain {
            id: id.clone(),
            name,
            hops,
        });
        st.persisted.active_chain_id = id;
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_probe_chain(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let hops: Vec<String> = val
            .get("hops")
            .and_then(|v| v.as_array())
            .map(|a| a.iter().filter_map(|x| x.as_str().map(str::to_owned)).collect())
            .unwrap_or_default();
        let (persisted, proxies, protect) = {
            let st = self.state.lock().await;
            (
                st.persisted.clone(),
                st.all_proxy_values(),
                st.active_protect_hook(),
            )
        };
        let key = hops.join(" → ");
        match nodes::probe_chain_latency(&persisted, &proxies, &hops, protect, 5000).await {
            Ok((first_ms, steady_ms)) => {
                let mut st = self.state.lock().await;
                if hops.len() == 1 {
                    st.node_first_delays.insert(hops[0].clone(), first_ms);
                    st.node_delays.insert(hops[0].clone(), steady_ms);
                }
                let probe_val = json!({
                    "ok": true,
                    "firstMs": first_ms,
                    "steadyMs": steady_ms,
                    "testedAt": chrono::Utc::now().timestamp()
                });
                st.chain_probes.insert(key.clone(), probe_val.clone());
                st.push_event(
                    "probe",
                    if hops.len() == 1 { "单节点" } else { "代理链" },
                    &format!("{key} · 首次 {first_ms} ms · 连接后 {steady_ms} ms"),
                    true,
                );
                let _ = st.save();
                json_resp(StatusCode::OK, probe_val)
            }
            Err(e) => {
                let mut st = self.state.lock().await;
                st.push_event("probe", "测速失败", &format!("{key}: {e}"), false);
                text_resp(StatusCode::BAD_GATEWAY, &format!("{e}\n"))
            }
        }
    }

    async fn handle_speedtest(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let _ = body;
        let (persisted, proxies, protect) = {
            let st = self.state.lock().await;
            (
                st.persisted.clone(),
                st.all_proxy_values(),
                st.active_protect_hook(),
            )
        };
        let hops = if let Some(c) = persisted.chains.iter().find(|c| c.id == persisted.active_chain_id) {
            c.hops.clone()
        } else if !persisted.selected_node.is_empty() {
            vec![persisted.selected_node.clone()]
        } else {
            return text_resp(StatusCode::BAD_REQUEST, "no node selected\n");
        };
        match nodes::probe_chain_latency(&persisted, &proxies, &hops, protect, 6000).await {
            Ok((first_ms, steady_ms)) => json_resp(
                StatusCode::OK,
                json!({
                    "ok": true,
                    "latencyMs": steady_ms,
                    "firstMs": first_ms,
                    "mbps": 0.0
                }),
            ),
            Err(e) => text_resp(StatusCode::BAD_GATEWAY, &format!("{e}\n")),
        }
    }

    async fn handle_diagnose(&self) -> Response<Full<Bytes>> {
        let st = self.state.lock().await;
        json_resp(
            StatusCode::OK,
            json!({
                "ok": true,
                "connected": st.connected,
                "rustEngine": true,
                "nodeCount": st.all_proxy_values().len(),
                "selectedNode": st.persisted.selected_node,
            }),
        )
    }

    async fn handle_sub_add(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let url = val.get("url").and_then(|u| u.as_str()).unwrap_or("").trim().to_owned();
        if url.is_empty() {
            return text_resp(StatusCode::BAD_REQUEST, "subscription url required\n");
        }
        let label = val
            .get("label")
            .or_else(|| val.get("name"))
            .and_then(|n| n.as_str())
            .filter(|s| !s.trim().is_empty())
            .map(|s| s.trim().to_owned())
            .unwrap_or_else(|| url.split("://").nth(1).unwrap_or(&url).split('/').next().unwrap_or("sub").to_owned());
        let protect = self.state.lock().await.active_protect_hook();
        let (proxies, user_info) = match subs::fetch_subscription_text(&url, protect).await {
            Ok((text, uinfo)) => {
                let proxies = subs::parse_proxies_from_payload(&text)
                    .map_err(|e| text_resp(StatusCode::BAD_REQUEST, &format!("{e}\n")));
                match proxies {
                    Ok(p) => (p, uinfo),
                    Err(resp) => return resp,
                }
            }
            Err(e) => return text_resp(StatusCode::BAD_GATEWAY, &format!("{e}\n")),
        };
        let now = chrono::Utc::now().timestamp();
        let id = format!("sub-{}", chrono::Utc::now().timestamp_millis());
        let mut st = self.state.lock().await;
        st.persisted.subscriptions.push(SavedSubscription {
            id,
            label,
            prefix: String::new(),
            url,
            sealed_url: String::new(),
            enabled: val.get("enabled").and_then(|v| v.as_bool()).unwrap_or(false),
            active: false,
            node_count: proxies.len(),
            updated_at: now,
            last_error: String::new(),
            quota_updated_at: now,
            quota_error: String::new(),
            user_info,
            raw_proxies: proxies,
        });
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_sub_update(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let id = val.get("id").and_then(|v| v.as_str()).unwrap_or("");
        let mut st = self.state.lock().await;
        let Some(sub) = st.persisted.subscriptions.iter_mut().find(|s| s.id == id) else {
            return text_resp(StatusCode::NOT_FOUND, "subscription not found\n");
        };
        if let Some(label) = val.get("label").or_else(|| val.get("name")).and_then(|v| v.as_str()) {
            sub.label = label.trim().to_owned();
        }
        if let Some(url) = val.get("url").and_then(|v| v.as_str()) {
            sub.url = url.trim().to_owned();
        }
        if let Some(enabled) = val.get("enabled").and_then(|v| v.as_bool()) {
            sub.enabled = enabled;
        }
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_sub_delete(&self, query: &str) -> Response<Full<Bytes>> {
        let id = query_param(query, "id").unwrap_or_default();
        let mut st = self.state.lock().await;
        st.persisted.subscriptions.retain(|s| s.id != id);
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_sub_activate(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let id = val.get("id").and_then(|v| v.as_str()).unwrap_or("");
        let url = val.get("url").and_then(|v| v.as_str()).unwrap_or("");
        let mut st = self.state.lock().await;
        if url == "__merged__" || id == "__merged__" || (id.is_empty() && url.is_empty()) {
            for sub in &mut st.persisted.subscriptions {
                sub.active = false;
            }
            st.persisted.subscription_url = "__merged__".to_owned();
            st.persisted.merged_proxies =
                subs::merge_enabled_subscriptions(&st.persisted.subscriptions);
            let _ = st.save();
            return json_resp(StatusCode::OK, json!({"ok": true}));
        }
        let mut found_proxies = None;
        let mut found_url = None;
        for sub in &mut st.persisted.subscriptions {
            sub.active = sub.id == id;
            if sub.active {
                found_proxies = Some(sub.raw_proxies.clone());
                found_url = Some(sub.url.clone());
            }
        }
        let Some(proxies) = found_proxies else {
            return text_resp(StatusCode::NOT_FOUND, "subscription not found\n");
        };
        st.persisted.merged_proxies = proxies;
        if let Some(u) = found_url {
            st.persisted.subscription_url = u;
        }
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_sub_merge(&self) -> Response<Full<Bytes>> {
        let (targets, protect) = {
            let st = self.state.lock().await;
            let targets: Vec<(String, String)> = st
                .persisted
                .subscriptions
                .iter()
                .filter(|s| s.enabled && !s.url.trim().is_empty())
                .map(|s| (s.id.clone(), s.url.clone()))
                .collect();
            (targets, st.active_protect_hook())
        };

        let mut tasks = tokio::task::JoinSet::new();
        for (id, url) in targets {
            tasks.spawn(async move {
                let res = match tokio::time::timeout(
                    std::time::Duration::from_secs(25),
                    subs::fetch_subscription_text(&url, protect),
                )
                .await
                {
                    Ok(r) => r,
                    Err(_) => Err("subscription fetch timeout".to_owned()),
                };
                (id, res)
            });
        }

        let mut fetched = Vec::new();
        while let Some(joined) = tasks.join_next().await {
            if let Ok(item) = joined {
                fetched.push(item);
            }
        }

        let now = chrono::Utc::now().timestamp();
        let mut st = self.state.lock().await;
        for (id, res) in fetched {
            if let Some(sub) = st.persisted.subscriptions.iter_mut().find(|s| s.id == id) {
                match res {
                    Ok((text, uinfo)) => match subs::parse_proxies_from_payload(&text) {
                        Ok(proxies) => {
                            sub.node_count = proxies.len();
                            sub.raw_proxies = proxies;
                            sub.updated_at = now;
                            sub.last_error.clear();
                            if uinfo.is_some() {
                                sub.user_info = uinfo;
                                sub.quota_updated_at = now;
                                sub.quota_error.clear();
                            }
                        }
                        Err(e) => {
                            sub.last_error = e;
                        }
                    },
                    Err(e) => {
                        sub.last_error = e;
                    }
                }
            }
        }

        let merged = subs::merge_enabled_subscriptions(&st.persisted.subscriptions);
        st.persisted.merged_proxies = merged;
        for sub in &mut st.persisted.subscriptions {
            sub.active = false;
        }
        st.persisted.subscription_url = "__merged__".to_owned();
        if st.persisted.selected_node.is_empty() {
            if let Some(first) = st
                .persisted
                .merged_proxies
                .first()
                .and_then(|p| p.get("name").and_then(|n| n.as_str()))
            {
                st.persisted.selected_node = first.to_owned();
            }
        }
        let _ = st.save();
        json_resp(
            StatusCode::OK,
            json!({
                "ok": true,
                "nodes": st.persisted.merged_proxies.len(),
                "nodeCount": st.persisted.merged_proxies.len()
            }),
        )
    }

    async fn handle_sub_auto_merge(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let hours = val.get("hours").and_then(|v| v.as_u64()).unwrap_or(0);
        let mut st = self.state.lock().await;
        st.persisted.auto_merge_hours = hours;
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_sub_refresh(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let id = val.get("id").and_then(|v| v.as_str()).unwrap_or("");
        let (url, protect) = {
            let st = self.state.lock().await;
            let Some(sub) = st.persisted.subscriptions.iter().find(|s| s.id == id) else {
                return text_resp(StatusCode::NOT_FOUND, "subscription not found\n");
            };
            (sub.url.clone(), st.active_protect_hook())
        };
        match subs::fetch_subscription_text(&url, protect).await {
            Ok((text, uinfo)) => match subs::parse_proxies_from_payload(&text) {
                Ok(proxies) => {
                    let now = chrono::Utc::now().timestamp();
                    let mut st = self.state.lock().await;
                    let mut updated_sub = None;
                    if let Some(sub) = st.persisted.subscriptions.iter_mut().find(|s| s.id == id) {
                        sub.node_count = proxies.len();
                        sub.raw_proxies = proxies;
                        sub.updated_at = now;
                        sub.last_error.clear();
                        if uinfo.is_some() {
                            sub.user_info = uinfo;
                            sub.quota_updated_at = now;
                            sub.quota_error.clear();
                        }
                        updated_sub = Some(sub.clone());
                    }
                    let merged = subs::merge_enabled_subscriptions(&st.persisted.subscriptions);
                    if !merged.is_empty() {
                        st.persisted.merged_proxies = merged;
                    }
                    let _ = st.save();
                    json_resp(StatusCode::OK, json!({"ok": true, "subscription": updated_sub}))
                }
                Err(e) => text_resp(StatusCode::BAD_REQUEST, &format!("{e}\n")),
            },
            Err(e) => text_resp(StatusCode::BAD_GATEWAY, &format!("{e}\n")),
        }
    }

    async fn handle_sub_quota(&self, body: &[u8]) -> Response<Full<Bytes>> {
        self.handle_sub_refresh(body).await
    }

    async fn handle_sub_diagnostics(&self) -> Response<Full<Bytes>> {
        let (url, protect) = {
            let st = self.state.lock().await;
            (st.persisted.subscription_url.clone(), st.active_protect_hook())
        };
        if url.trim().is_empty() {
            return text_resp(StatusCode::BAD_REQUEST, "invalid subscription URL\n");
        }
        match subs::fetch_subscription_text(&url, protect).await {
            Ok((text, uinfo)) => {
                let count = subs::parse_proxies_from_payload(&text)
                    .map(|p| p.len())
                    .unwrap_or(0);
                json_resp(
                    StatusCode::OK,
                    json!({"ok": true, "nodeCount": count, "userInfo": uinfo}),
                )
            }
            Err(e) => text_resp(StatusCode::BAD_GATEWAY, &format!("{e}\n")),
        }
    }

    fn regions_payload(&self, st: &RuntimeState) -> Value {
        let mut counts: BTreeMap<String, usize> = BTreeMap::new();
        for p in st.all_proxy_values() {
            if let Some(name) = p.get("name").and_then(|n| n.as_str()) {
                let r = st
                    .node_regions
                    .get(name)
                    .map(String::as_str)
                    .unwrap_or_else(|| nodes::infer_region_from_name(name));
                if !r.is_empty() {
                    *counts.entry(r.to_owned()).or_insert(0) += 1;
                }
            }
        }
        json!({
            "lockedRegion": st.persisted.locked_region,
            "regions": counts,
        })
    }

    async fn handle_regions_verify(&self, _body: &[u8]) -> Response<Full<Bytes>> {
        let st = self.state.lock().await;
        json_resp(StatusCode::OK, self.regions_payload(&st))
    }

    async fn handle_region_lock(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(val) = serde_json::from_slice::<Value>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let country = val.get("country").and_then(|v| v.as_str()).unwrap_or("").to_owned();
        let mut st = self.state.lock().await;
        st.persisted.locked_region = country;
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_health_params(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(params) = serde_json::from_slice::<HealthParams>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let mut st = self.state.lock().await;
        st.persisted.health_params = params.clone();
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true, "params": params}))
    }

    async fn handle_put_path_checks(&self, body: &[u8]) -> Response<Full<Bytes>> {
        let Ok(cfg) = serde_json::from_slice::<PathChecksConfig>(body) else {
            return text_resp(StatusCode::BAD_REQUEST, "invalid JSON\n");
        };
        let mut st = self.state.lock().await;
        st.persisted.path_checks = cfg;
        let _ = st.save();
        json_resp(StatusCode::OK, json!({"ok": true}))
    }

    async fn handle_run_path_checks(&self) -> Response<Full<Bytes>> {
        let (persisted, proxies, protect) = {
            let st = self.state.lock().await;
            (
                st.persisted.clone(),
                st.all_proxy_values(),
                st.active_protect_hook(),
            )
        };
        let hops = if let Some(c) = persisted.chains.iter().find(|c| c.id == persisted.active_chain_id) {
            c.hops.clone()
        } else if !persisted.selected_node.is_empty() {
            vec![persisted.selected_node.clone()]
        } else {
            return text_resp(StatusCode::BAD_REQUEST, "no node selected\n");
        };
        let res = nodes::probe_chain_latency(&persisted, &proxies, &hops, protect, 5000).await;
        let ok = res.is_ok();
        let detail = match &res {
            Ok((first_ms, steady_ms)) => format!("首次 {first_ms} ms · 连接后 {steady_ms} ms"),
            Err(e) => e.clone(),
        };
        let mut st = self.state.lock().await;
        st.push_event("path_check", "通路检测", &detail, ok);
        json_resp(StatusCode::OK, json!({"ok": ok, "detail": detail}))
    }
}

fn query_param(query: &str, key: &str) -> Option<String> {
    for pair in query.split('&') {
        if let Some((k, v)) = pair.split_once('=') {
            if k == key {
                return Some(v.to_owned());
            }
        }
    }
    None
}

fn text_resp(status: StatusCode, msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(status)
        .header(CONTENT_TYPE, "text/plain; charset=utf-8")
        .body(Full::new(Bytes::from(msg.as_bytes().to_vec())))
        .unwrap()
}

fn json_resp(status: StatusCode, val: Value) -> Response<Full<Bytes>> {
    let bytes = serde_json::to_vec(&val).unwrap_or_else(|_| b"{}".to_vec());
    Response::builder()
        .status(status)
        .header(CONTENT_TYPE, "application/json")
        .body(Full::new(Bytes::from(bytes)))
        .unwrap()
}

#[cfg(test)]
mod tests {
    use super::*;
    use state::NoopBridge;

    #[tokio::test]
    async fn control_plane_state_and_app_groups_contract() {
        let dir = std::env::temp_dir().join(format!(
            "smartvpn_cp_test_{}",
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let cp = ControlPlane::start(dir.clone(), Arc::new(NoopBridge))
            .await
            .unwrap();

        let stream = tokio::net::TcpStream::connect((Ipv4Addr::LOCALHOST, cp.port))
            .await
            .unwrap();
        let (mut tx, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
            .await
            .unwrap();
        tokio::spawn(async move {
            let _ = conn.await;
        });

        let req = Request::get("/api/state")
            .header(AUTHORIZATION, format!("Bearer {}", cp.token))
            .body(http_body_util::Empty::<Bytes>::new())
            .unwrap();
        let res = tx.send_request(req).await.unwrap();
        assert_eq!(res.status(), 200);
        let body = res.into_body().collect().await.unwrap().to_bytes();
        let json: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["rustCore"], true);
        assert_eq!(json["tunAvailable"], true);

        // Reject empty apps in appGroups with explicit validation message
        let bad_patch = json!({
            "appGroups": [{
                "id": "g1",
                "name": "Bad",
                "apps": [],
                "targetType": "direct",
                "enabled": true
            }]
        });
        let req2 = Request::put("/api/settings")
            .header(AUTHORIZATION, format!("Bearer {}", cp.token))
            .body(Full::new(Bytes::from(serde_json::to_vec(&bad_patch).unwrap())))
            .unwrap();
        let stream2 = tokio::net::TcpStream::connect((Ipv4Addr::LOCALHOST, cp.port))
            .await
            .unwrap();
        let (mut tx2, conn2) = hyper::client::conn::http1::handshake(TokioIo::new(stream2))
            .await
            .unwrap();
        tokio::spawn(async move {
            let _ = conn2.await;
        });
        let res2 = tx2.send_request(req2).await.unwrap();
        assert_eq!(res2.status(), 400);

        cp.shutdown().await;
        let _ = std::fs::remove_dir_all(&dir);
    }
}
