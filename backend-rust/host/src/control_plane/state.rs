use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    collections::{BTreeMap, HashSet},
    fs,
    path::{Path, PathBuf},
    sync::Arc,
};

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TunnelReport {
    #[serde(default)]
    pub interface: String,
    #[serde(default)]
    pub routes: Vec<String>,
    #[serde(default)]
    pub dns: Vec<String>,
}

pub trait PlatformBridge: Send + Sync {
    fn seal(&self, plain: &str) -> Result<String, String>;
    fn open(&self, sealed: &str) -> Result<String, String>;
    fn drop_tunnel(&self);
    fn describe_tunnel(&self) -> Option<TunnelReport>;
    fn protect_hook(&self) -> Option<smartvpn_engine::dialer::SocketProtectHook>;
    fn resolve_hook(&self) -> Option<smartvpn_engine::ffi::OwnerCallback>;
}

#[derive(Default)]
pub struct NoopBridge;

impl PlatformBridge for NoopBridge {
    fn seal(&self, plain: &str) -> Result<String, String> {
        Ok(plain.to_owned())
    }
    fn open(&self, sealed: &str) -> Result<String, String> {
        Ok(sealed.to_owned())
    }
    fn drop_tunnel(&self) {}
    fn describe_tunnel(&self) -> Option<TunnelReport> {
        None
    }
    fn protect_hook(&self) -> Option<smartvpn_engine::dialer::SocketProtectHook> {
        None
    }
    fn resolve_hook(&self) -> Option<smartvpn_engine::ffi::OwnerCallback> {
        None
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct AppGroup {
    pub id: String,
    pub name: String,
    pub apps: Vec<String>,
    pub target_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub target_node: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub target_chain_id: String,
    pub enabled: bool,
}

#[derive(Clone, Debug, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct QuotaInfo {
    pub upload: i64,
    pub download: i64,
    pub total: i64,
    pub expire: i64,
}

#[derive(Clone, Debug, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct SavedSubscription {
    pub id: String,
    #[serde(alias = "name")]
    pub label: String,
    #[serde(default)]
    pub prefix: String,
    #[serde(default)]
    pub url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sealed_url: String,
    #[serde(default)]
    pub enabled: bool,
    #[serde(default)]
    pub active: bool,
    #[serde(default)]
    pub node_count: usize,
    #[serde(default)]
    pub updated_at: i64,
    #[serde(default)]
    pub last_error: String,
    #[serde(default)]
    pub quota_updated_at: i64,
    #[serde(default)]
    pub quota_error: String,
    #[serde(default)]
    pub user_info: Option<QuotaInfo>,
    #[serde(default)]
    pub raw_proxies: Vec<Value>,
}

#[derive(Clone, Debug, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct ManualNode {
    pub name: String,
    pub uri: String,
    #[serde(default)]
    pub proxy: Value,
}

#[derive(Clone, Debug, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct ProxyChain {
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub backups: BTreeMap<String, Vec<String>>,
    pub id: String,
    pub name: String,
    pub hops: Vec<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct HealthParams {
    pub interval_sec: u64,
    pub timeout_ms: u64,
    pub max_delay_ms: u64,
    pub cooldown_sec: u64,
}

impl Default for HealthParams {
    fn default() -> Self {
        Self {
            interval_sec: 120,
            timeout_ms: 4000,
            max_delay_ms: 2500,
            cooldown_sec: 180,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PathChecksConfig {
    pub enabled: bool,
    pub interval_sec: u64,
    pub targets: Vec<String>,
}

impl Default for PathChecksConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            interval_sec: 120,
            targets: vec![
                "https://www.google.com/generate_204".to_owned(),
                "https://www.youtube.com/generate_204".to_owned(),
            ],
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EventEntry {
    pub id: u64,
    pub kind: String,
    pub time: String,
    pub title: String,
    pub detail: String,
    pub ok: bool,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PersistedState {
    #[serde(default)]
    pub subscription_url: String,
    #[serde(default)]
    pub sealed_subscription_url: String,
    #[serde(default)]
    pub import_policy: String,
    #[serde(default)]
    pub selected_node: String,
    #[serde(default)]
    pub selection_mode: String,
    #[serde(default)]
    pub locked_region: String,
    #[serde(default)]
    pub locked_node: String,
    #[serde(default = "default_connection_mode")]
    pub connection_mode: String,
    #[serde(default = "default_true")]
    pub domestic_web: bool,
    #[serde(default = "default_true")]
    pub webrtc_protection: bool,
    #[serde(default = "default_true")]
    pub dns_protection: bool,
    #[serde(default)]
    pub strict_privacy: bool,
    #[serde(default)]
    pub socks_port: u16,
    #[serde(default)]
    pub game_bypass: Vec<String>,
    #[serde(default)]
    pub app_groups: Vec<AppGroup>,
    #[serde(default)]
    pub auto_merge_hours: u64,
    #[serde(default)]
    pub subscriptions: Vec<SavedSubscription>,
    #[serde(default)]
    pub merged_proxies: Vec<Value>,
    #[serde(default)]
    pub manual_nodes: Vec<ManualNode>,
    #[serde(default)]
    pub chains: Vec<ProxyChain>,
    #[serde(default)]
    pub active_chain_id: String,
    #[serde(default)]
    pub health_params: HealthParams,
    #[serde(default)]
    pub path_checks: PathChecksConfig,
    #[serde(default)]
    pub events: Vec<EventEntry>,
}

fn default_connection_mode() -> String {
    if cfg!(target_os = "android") {
        "vpn".to_owned()
    } else {
        "tun".to_owned()
    }
}
fn default_true() -> bool {
    true
}

impl Default for PersistedState {
    fn default() -> Self {
        Self {
            subscription_url: String::new(),
            sealed_subscription_url: String::new(),
            import_policy: "default".to_owned(),
            selected_node: String::new(),
            selection_mode: "manual".to_owned(),
            locked_region: String::new(),
            locked_node: String::new(),
            connection_mode: default_connection_mode(),
            domestic_web: true,
            webrtc_protection: true,
            dns_protection: true,
            strict_privacy: false,
            socks_port: 0,
            game_bypass: Vec::new(),
            app_groups: Vec::new(),
            auto_merge_hours: 0,
            subscriptions: Vec::new(),
            merged_proxies: Vec::new(),
            manual_nodes: Vec::new(),
            chains: Vec::new(),
            active_chain_id: String::new(),
            health_params: HealthParams::default(),
            path_checks: PathChecksConfig::default(),
            events: Vec::new(),
        }
    }
}

pub struct RuntimeState {
    pub(super) china: super::china::China,
    pub home: PathBuf,
    pub bridge: Arc<dyn PlatformBridge>,
    pub persisted: PersistedState,
    pub connected: bool,
    pub tun_fd: Option<i32>,
    pub tun_handle: Option<i64>,
    pub tunnel_report: Option<TunnelReport>,
    pub started_at: String,
    pub node_delays: BTreeMap<String, i64>,
    pub node_first_delays: BTreeMap<String, i64>,
    pub node_regions: BTreeMap<String, String>,
    pub chain_probes: BTreeMap<String, Value>,
    pub next_event_id: u64,
    pub rx_bytes: u64,
    pub tx_bytes: u64,
}

impl RuntimeState {
    pub fn load(home: &Path, bridge: Arc<dyn PlatformBridge>) -> Self {
        let _ = fs::create_dir_all(home);
        let path = home.join("rust_host_state.json");
        let mut persisted: PersistedState = fs::read(&path)
            .ok()
            .and_then(|bytes| serde_json::from_slice(&bytes).ok())
            .unwrap_or_default();
        if persisted.subscription_url.starts_with("enc:v1:") {
            if let Some(opened) = open_maybe_sealed(&*bridge, &persisted.subscription_url) {
                persisted.subscription_url = opened;
            }
        } else if persisted.subscription_url.is_empty() && !persisted.sealed_subscription_url.is_empty() {
            if let Some(opened) = open_maybe_sealed(&*bridge, &persisted.sealed_subscription_url) {
                persisted.subscription_url = opened;
            }
        }
        for sub in &mut persisted.subscriptions {
            if sub.url.starts_with("enc:v1:") {
                if let Some(opened) = open_maybe_sealed(&*bridge, &sub.url) {
                    sub.sealed_url = sub.url.clone();
                    sub.url = opened;
                }
            } else if sub.url.is_empty() && !sub.sealed_url.is_empty() {
                if let Some(opened) = open_maybe_sealed(&*bridge, &sub.sealed_url) {
                    sub.url = opened;
                }
            }
        }
        if !path.exists() || persisted.subscriptions.is_empty() {
            migrate_legacy_go_state(home, &*bridge, &mut persisted);
        }
        if cfg!(target_os = "android")
            && (persisted.connection_mode == "system-proxy"
                || persisted.connection_mode.trim().is_empty())
        {
            persisted.connection_mode = "vpn".to_owned();
        }
        let next_event_id = persisted.events.iter().map(|e| e.id).max().unwrap_or(0) + 1;
        Self {
            china: super::china::China::load(home),
            home: home.to_path_buf(),
            bridge,
            persisted,
            connected: false,
            tun_fd: None,
            tun_handle: None,
            tunnel_report: None,
            started_at: chrono::Utc::now().to_rfc3339(),
            node_delays: BTreeMap::new(),
            node_first_delays: BTreeMap::new(),
            node_regions: BTreeMap::new(),
            chain_probes: BTreeMap::new(),
            next_event_id,
            rx_bytes: 0,
            tx_bytes: 0,
        }
    }

    pub fn save(&mut self) -> Result<(), String> {
        let _ = fs::create_dir_all(&self.home);
        if !self.persisted.subscription_url.is_empty() {
            if let Ok(sealed) = self.bridge.seal(&self.persisted.subscription_url) {
                self.persisted.sealed_subscription_url = sealed;
            }
        }
        for sub in &mut self.persisted.subscriptions {
            if !sub.url.is_empty() {
                if let Ok(sealed) = self.bridge.seal(&sub.url) {
                    sub.sealed_url = sealed;
                }
            }
        }
        let mut disk = self.persisted.clone();
        // Avoid storing plaintext subscription URLs if sealed copy exists.
        if !disk.sealed_subscription_url.is_empty() {
            disk.subscription_url.clear();
        }
        for sub in &mut disk.subscriptions {
            if !sub.sealed_url.is_empty() {
                sub.url.clear();
            }
        }
        let bytes = serde_json::to_vec_pretty(&disk).map_err(|e| e.to_string())?;
        let tmp = self.home.join("rust_host_state.json.tmp");
        let dst = self.home.join("rust_host_state.json");
        fs::write(&tmp, &bytes).map_err(|e| e.to_string())?;
        fs::rename(&tmp, &dst).map_err(|e| e.to_string())?;
        Ok(())
    }

    pub fn active_protect_hook(&self) -> Option<smartvpn_engine::dialer::SocketProtectHook> {
        if self.connected && self.tun_handle.is_some() {
            self.bridge.protect_hook()
        } else {
            None
        }
    }

    pub fn push_event(&mut self, kind: &str, title: &str, detail: &str, ok: bool) {
        let id = self.next_event_id;
        self.next_event_id += 1;
        let time = chrono::Local::now().format("%Y-%m-%d %H:%M:%S").to_string();
        self.persisted.events.insert(
            0,
            EventEntry {
                id,
                kind: kind.to_owned(),
                time,
                title: title.to_owned(),
                detail: detail.to_owned(),
                ok,
            },
        );
        if self.persisted.events.len() > 200 {
            self.persisted.events.truncate(200);
        }
    }

    pub fn all_proxy_values(&self) -> Vec<Value> {
        let mut out = self.persisted.merged_proxies.clone();
        let mut seen: HashSet<String> = out
            .iter()
            .filter_map(|v| v.get("name").and_then(|n| n.as_str()).map(str::to_owned))
            .collect();
        for m in &self.persisted.manual_nodes {
            if seen.insert(m.name.clone()) && m.proxy.is_object() {
                out.push(m.proxy.clone());
            }
        }
        out
    }

    pub fn active_chain(&self) -> Option<&ProxyChain> {
        self.persisted
            .chains
            .iter()
            .find(|c| c.id == self.persisted.active_chain_id)
    }

    pub fn state_json(&self) -> Value {
        let active_chain = self.active_chain();
        let active_chain_name = active_chain.map(|c| c.name.as_str()).unwrap_or("");
        let active_chain_hops: Vec<String> = active_chain
            .map(|c| c.hops.clone())
            .unwrap_or_default();
        let chains_map: BTreeMap<&str, &Vec<String>> = self
            .persisted
            .chains
            .iter()
            .map(|c| (c.name.as_str(), &c.hops))
            .collect();
        let active_node = if !active_chain_hops.is_empty() {
            active_chain_hops.join(" → ")
        } else {
            self.persisted.selected_node.clone()
        };
        let total_nodes = self.all_proxy_values().len();
        json!({
            "connected": self.connected,
            "subscriptionUrl": self.persisted.subscription_url,
            "mihomoPath": "pure-rust-engine",
            "mihomoResolved": "libsmartvpn_engine.so",
            "home": self.home.to_string_lossy(),
            "portable": false,
            "proxyEnabled": false,
            "mixedPort": 0,
            "socksPort": self.persisted.socks_port,
            "startedAt": self.started_at,
            "selectedNode": self.persisted.selected_node,
            "activeNode": active_node,
            "chain": {
                "enabled": !active_chain_hops.is_empty(),
                "name": active_chain_name,
                "hops": active_chain_hops,
            },
            "chains": chains_map,
            "chainBackups": self.persisted.chains.iter().map(|c| (&c.name, &c.backups)).collect::<BTreeMap<_, _>>(),
            "activeChainId": self.persisted.active_chain_id,
            "activeRegion": self.persisted.locked_region,
            "lockedRegion": self.persisted.locked_region,
            "blocked": false,
            "blockReason": "",
            "health": {
                "counts": {"healthy": total_nodes, "cooldown": 0, "unhealthy": 0},
                "params": self.persisted.health_params,
                "lockedRegion": self.persisted.locked_region,
                "poolSize": total_nodes,
                "sweepNote": ""
            },
            "regionJob": {"running": false, "done": 0, "total": 0},
            "connectionMode": self.persisted.connection_mode,
            "strictPrivacy": self.persisted.strict_privacy,
            "domesticWeb": self.persisted.domestic_web,
            "appGroups": self.persisted.app_groups,
            "appGroupChains": self.persisted.chains.iter().map(|c| (&c.id, &c.name)).collect::<BTreeMap<_, _>>(),
            "gameBypass": self.persisted.game_bypass,
            "webRTCProtection": self.persisted.webrtc_protection,
            "dnsProtection": self.persisted.dns_protection,
            "wintunPath": "",
            "elevated": true,
            "tunAvailable": true,
            "tunReason": "",
            "tunActive": self.connected && self.tun_handle.is_some(),
            "chinaDirect": self.china.status(self.persisted.domestic_web),
            "rustCore": true,
            "rustEngineActive": self.tun_handle.is_some()
        })
    }
}

pub fn valid_app_group_text(s: &str) -> bool {
    !s.trim().is_empty() && !s.contains([',', '*']) && !s.chars().any(char::is_control)
}

pub fn validate_app_groups(groups: Vec<AppGroup>, chains: &[ProxyChain]) -> Result<Vec<AppGroup>, String> {
    let mut out = Vec::with_capacity(groups.len());
    for (i, mut g) in groups.into_iter().enumerate() {
        if !valid_app_group_text(&g.id) || !valid_app_group_text(&g.name) || g.apps.is_empty() {
            return Err(format!(
                "appGroups[{i}] requires valid id, name, apps and enabled"
            ));
        }
        g.id = g.id.trim().to_owned();
        g.name = g.name.trim().to_owned();
        match g.target_type.as_str() {
            "node" => {
                if g.target_node.trim().is_empty() {
                    return Err(format!("appGroups[{i}] requires a target node"));
                }
            }
            "chain" => {
                if g.target_chain_id.is_empty() || (g.enabled && !chains.iter().any(|c| c.id == g.target_chain_id)) {
                    return Err(format!("分组「{}」请选择可用的代理链", g.name));
                }
            }
            "direct" | "reject" => {}
            other => return Err(format!("appGroups[{i}] has invalid targetType {other:?}")),
        }
        let mut seen = HashSet::new();
        let mut clean_apps = Vec::with_capacity(g.apps.len());
        for app in g.apps {
            if !valid_app_group_text(&app) {
                return Err(format!("appGroups[{i}] has invalid application {app:?}"));
            }
            let trimmed = app.trim().to_owned();
            if seen.insert(trimmed.clone()) {
                clean_apps.push(trimmed);
            }
        }
        g.apps = clean_apps;
        out.push(g);
    }
    Ok(out)
}

fn open_maybe_sealed(bridge: &dyn PlatformBridge, value: &str) -> Option<String> {
    if let Some(raw) = value.strip_prefix("enc:v1:") {
        bridge.open(raw).ok()
    } else if !value.is_empty() && !value.contains("://") {
        bridge.open(value).ok()
    } else if !value.is_empty() {
        Some(value.to_owned())
    } else {
        None
    }
}

fn migrate_legacy_go_state(
    home: &Path,
    bridge: &dyn PlatformBridge,
    persisted: &mut PersistedState,
) {
    // 1. Import settings.json if present.
    if let Ok(bytes) = fs::read(home.join("settings.json")) {
        if let Ok(v) = serde_json::from_slice::<Value>(&bytes) {
            if persisted.subscription_url.is_empty() {
                if let Some(u) = v.get("subscriptionUrl").and_then(|s| s.as_str()) {
                    if let Some(opened) = open_maybe_sealed(bridge, u) {
                        persisted.subscription_url = opened;
                    }
                } else if let Some(s) = v.get("sealedSubscriptionUrl").and_then(|s| s.as_str()) {
                    if let Some(opened) = open_maybe_sealed(bridge, s) {
                        persisted.subscription_url = opened;
                    }
                }
            }
            if persisted.selected_node.is_empty() {
                if let Some(n) = v.get("selectedNode").and_then(|s| s.as_str()) {
                    persisted.selected_node = n.to_owned();
                }
            }
            if let Some(b) = v.get("domesticWeb").and_then(|x| x.as_bool()) {
                persisted.domestic_web = b;
            }
            if let Some(b) = v.get("webRTCProtection").and_then(|x| x.as_bool()) {
                persisted.webrtc_protection = b;
            }
            if let Some(b) = v.get("dnsProtection").and_then(|x| x.as_bool()) {
                persisted.dns_protection = b;
            }
            if let Some(h) = v.get("autoMergeHours").and_then(|x| x.as_u64()) {
                persisted.auto_merge_hours = h;
            }
            if persisted.app_groups.is_empty() {
                if let Some(g) = v.get("appGroups") {
                    if let Ok(groups) = serde_json::from_value::<Vec<AppGroup>>(g.clone()) {
                        persisted.app_groups = groups;
                    }
                }
            }
        }
    }

    // 2. Import saved subscriptions from smartvpn.db (settings_kv["subscriptions"]).
    let db_path = home.join("smartvpn.db");
    if db_path.exists() && persisted.subscriptions.is_empty() {
        if let Ok(store) = smartvpn_store::Store::open(&db_path) {
            if let Ok(Some(raw)) = store.get_value("subscriptions") {
                if let Ok(mut subs) = serde_json::from_str::<Vec<SavedSubscription>>(&raw) {
                    for sub in &mut subs {
                        if let Some(opened) = open_maybe_sealed(bridge, &sub.url) {
                            if sub.url.starts_with("enc:v1:") {
                                sub.sealed_url = sub.url.clone();
                            }
                            sub.url = opened;
                        }
                        let cache_path = home.join("subscriptions").join(format!("{}.yaml", sub.id));
                        if let Ok(yaml) = fs::read_to_string(&cache_path) {
                            if let Ok(proxies) = super::subs::parse_proxies_from_payload(&yaml) {
                                sub.node_count = proxies.len();
                                sub.raw_proxies = proxies;
                            }
                        }
                    }
                    if !subs.is_empty() {
                        persisted.merged_proxies = super::subs::merge_enabled_subscriptions(&subs);
                        persisted.subscriptions = subs;
                    }
                }
            }
            let _ = store.close();
        }
    }
}
