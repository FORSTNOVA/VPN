#[cfg(any(target_os = "android", test))]
mod android_dns;
mod checks;
pub mod config;
mod direct;
mod history;
mod http;
mod metrics;
mod monitor;
pub mod probe;
mod regions;
pub use regions::region;
mod node;
mod store;
mod subscriptions;
use anyhow::{anyhow, ensure, Context, Result};
use serde::Serialize;
use serde_json::{json, Value};
use std::net::TcpListener;
use std::path::Path;
use std::{
    io::Read,
    sync::{Arc, Mutex},
    thread,
    time::Duration,
};
use store::Store;
use tiny_http::{Header, Request, Response, Server};

pub const VERSION: &str = env!("CARGO_PKG_VERSION");

// Android's VpnService shares a process with the runtime. All connections to
// the WAL database must participate in the same SQLite library's file locks.
#[cfg(any(target_os = "android", test))]
pub fn android_game_bypass(dir: &Path) -> Result<Vec<String>> {
    let db = store::existing_connection(dir, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    let value: String = db.query_row(
        "SELECT value FROM settings WHERE key='gameBypass'",
        [],
        |row| row.get(0),
    )?;
    Ok(serde_json::from_str(&value)?)
}

pub fn token() -> Result<String> {
    let mut bytes = [0u8; 32];
    getrandom::getrandom(&mut bytes).map_err(|e| anyhow!("OS random: {e}"))?;
    Ok(bytes.iter().map(|b| format!("{b:02x}")).collect())
}

pub type ProbeJob = Box<dyn FnOnce() -> Result<(u64, u64)> + Send>;

pub trait TunnelDataPlane: Send {
    fn status(&mut self) -> Result<PlaneStatus>;
    fn connect(&mut self, body: &[u8], stored: &[Value]) -> Result<()>;
    fn disconnect(&mut self) -> Result<()>;
    fn controller(&self) -> Result<&Controller>;
    fn connections(&self) -> Result<Value> {
        self.controller()?.json("/connections")
    }
    fn region(&self, _nodes: Vec<Value>) -> Result<Value> {
        anyhow::bail!("region probe unavailable")
    }

    fn probe_job(&self, _nodes: Vec<Value>, _url: String) -> Result<ProbeJob> {
        anyhow::bail!("probe unavailable")
    }
    #[cfg(target_os = "android")]
    fn tunnel_interface(&self) -> Result<String>;
    #[cfg(target_os = "android")]
    fn running_policy(&self) -> Option<&Value>;
}
#[derive(Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PlaneStatus {
    pub connected: bool,
    pub mixed_port: u16,
    pub controller_port: u16,
    pub tunnel_authorized: bool,
    pub tunnel_fd: Option<i32>,
}
#[derive(Clone)]
pub struct Controller {
    pub port: u16,
    pub secret: String,
}
impl Controller {
    pub fn request(&self, method: &str, path: &str, body: &[u8]) -> Result<(u16, Vec<u8>)> {
        let agent = http::agent();
        let req = agent
            .request(method, &format!("http://127.0.0.1:{}{path}", self.port))
            .set("Authorization", &format!("Bearer {}", self.secret))
            .set("Content-Type", "application/json");
        let result = if body.is_empty() {
            req.call()
        } else {
            req.send_bytes(body)
        };
        let resp = match result {
            Ok(r) | Err(ureq::Error::Status(_, r)) => r,
            Err(e) => return Err(http::error(e)),
        };
        let code = resp.status();
        let mut bytes = http::body(resp)?;
        if method == "GET" && code == 200 && path == "/connections" {
            let mut data: Value = serde_json::from_slice(&bytes)?;
            normalize_connections(&mut data)?;
            bytes = serde_json::to_vec(&data)?;
        }
        Ok((code, bytes))
    }
}

fn normalize_connections(data: &mut Value) -> Result<()> {
    for conn in data["connections"]
        .as_array_mut()
        .context("expected connections array")?
    {
        let meta = &mut conn["metadata"];
        let net = meta["network"].as_str().context("expected network")?;
        meta["network"] = json!(net.to_ascii_lowercase());
        for field in ["sourcePort", "destinationPort"] {
            let v = &meta[field];
            let port = if let Some(s) = v.as_str() {
                s.parse::<u16>()?
            } else {
                u16::try_from(v.as_u64().context("expected integer port")?)?
            };
            meta[field] = json!(port.to_string());
        }
    }
    Ok(())
}
#[derive(Serialize)]
pub struct Bootstrap {
    pub address: String,
    pub port: u16,
    pub token: String,
    pub version: &'static str,
    pub edition: &'static str,
}

pub struct Runtime {
    pub bootstrap: Bootstrap,
    server: Arc<Server>,
    stop_flag: Arc<std::sync::atomic::AtomicBool>,
    worker: thread::JoinHandle<Result<()>>,
}
impl Runtime {
    pub fn start<P: TunnelDataPlane + 'static>(plane: Arc<Mutex<P>>, dir: &Path) -> Result<Self> {
        let store = Store::open(dir)?;
        let db = store::existing_connection(dir, rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE)?;
        Self::run(plane, store, Some(db))
    }
    #[cfg(test)]
    fn with_store<P: TunnelDataPlane + 'static>(
        plane: Arc<Mutex<P>>,
        store: Store,
    ) -> Result<Self> {
        Self::run(plane, store, None)
    }
    fn run<P: TunnelDataPlane + 'static>(
        plane: Arc<Mutex<P>>,
        mut store: Store,
        sampler: Option<rusqlite::Connection>,
    ) -> Result<Self> {
        let server =
            Arc::new(Server::http("127.0.0.1:0").map_err(|e| anyhow!("bind loopback: {e}"))?);
        let address = server
            .server_addr()
            .to_ip()
            .context("expected TCP address")?;
        let bootstrap = Bootstrap {
            address: address.to_string(),
            port: address.port(),
            token: token()?,
            version: VERSION,
            edition: "rust-pure",
        };
        let worker_server = server.clone();
        let auth = format!("Bearer {}", bootstrap.token);
        let stop_flag = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let worker_stop = stop_flag.clone();
        let worker = thread::spawn(move || -> Result<()> {
            let (stop, stopped) = std::sync::mpsc::channel::<()>();
            let sampling_plane = plane.clone();
            let sampling_error = Arc::new(Mutex::new(None));
            let last_error = sampling_error.clone();
            let sampler = sampler.map(|mut db| {
                thread::spawn(move || -> Result<()> {
                    while matches!(
                        stopped.recv_timeout(Duration::from_secs(1)),
                        Err(std::sync::mpsc::RecvTimeoutError::Timeout)
                    ) {
                        let mut plane = sampling_plane.lock().unwrap();
                        let result = (|| -> Result<()> {
                            if plane.status()?.connected {
                                history::record(&mut db, &plane.connections()?)?;
                            }
                            Ok(())
                        })();
                        *last_error.lock().unwrap() = result
                            .err()
                            .map(|e| http::redact(&format!("traffic sampler: {e:#}")));
                    }
                    Ok(())
                })
            });
            let mut http_responses = std::collections::BTreeMap::<u16, u64>::new();

            let (pending, workers, completed) = {
                type Task = (Request, Box<dyn FnOnce() -> Result<Vec<u8>> + Send>);
                let (tx, rx) = std::sync::mpsc::sync_channel::<Task>(4);
                let rx = Arc::new(Mutex::new(rx));
                let (done, completed) = std::sync::mpsc::channel();
                let workers = (0..4)
                    .map(|_| {
                        let rx = rx.clone();
                        let done = done.clone();
                        thread::spawn(move || loop {
                            let task = rx.lock().unwrap().recv();
                            let Ok((req, job)) = task else { break };
                            let (code, body) = http_reply(job().map(|body| (200, body)));
                            respond(req, code, body);
                            done.send(code).expect("probe server stopped");
                        })
                    })
                    .collect::<Vec<_>>();
                (tx, workers, completed)
            };
            let mut last_attempt = None;
            let mut refresh: Option<thread::JoinHandle<Result<()>>> = None;
            let mut maintenance = std::time::Instant::now();
            loop {
                if maintenance.elapsed() >= Duration::from_secs(1) {
                    maintenance = std::time::Instant::now();
                    let now = chrono::Utc::now();
                    let hours = store.pref("mergeHours")?.as_i64().unwrap();
                    if refresh.as_ref().is_some_and(|job| job.is_finished()) {
                        if let Err(error) = refresh
                            .take()
                            .unwrap()
                            .join()
                            .map_err(|_| anyhow!("subscription refresh worker panicked"))?
                        {
                            eprintln!(
                                "scheduled merge failed: {}",
                                http::redact(&error.to_string())
                            );
                        }
                    }
                    if refresh.is_none()
                        && last_attempt.is_none_or(|t: std::time::Instant| {
                            t.elapsed() >= Duration::from_secs(60)
                        })
                        && subscriptions::due(
                            hours,
                            store.pref("mergeAt")?.as_i64().unwrap(),
                            now.timestamp(),
                        )
                    {
                        last_attempt = Some(std::time::Instant::now());
                        refresh = Some(subscriptions::start_refresh(store.dir.clone()));
                    }
                    let paths = store.pref("paths")?;
                    if paths["enabled"] == true {
                        let mut kernel = plane.lock().unwrap();
                        let status = kernel.status()?;
                        let last = store.pref("pathStatus")?;
                        let last_at = last["at"]
                            .as_str()
                            .map(chrono::DateTime::parse_from_rfc3339)
                            .transpose()?;
                        if status.connected
                            && last_at.is_none_or(|t| {
                                now.timestamp() - t.timestamp()
                                    >= paths["intervalSec"].as_i64().unwrap()
                            })
                        {
                            if let Err(e) = store.run_paths(kernel.controller()?, status.mixed_port)
                            {
                                store.set_pref("pathStatus",&json!({"at":now.to_rfc3339(),"note":http::redact(&e.to_string()),"failures":["monitor"]}))?;
                            }
                        }
                    }
                }
                let Some(mut req) = worker_server.recv_timeout(Duration::from_secs(1))? else {
                    if worker_stop.load(std::sync::atomic::Ordering::Acquire) {
                        break;
                    }
                    continue;
                };

                if req.method().as_str() == "POST"
                    && req.url().split('?').next() == Some("/api/nodes/chain/probe")
                    && req
                        .headers()
                        .iter()
                        .any(|h| h.field.equiv("Authorization") && h.value.as_str() == auth)
                {
                    let task = prepare_probe(&mut req, &store, &plane);
                    match task {
                        Ok(job) => match pending.try_send((req, Box::new(job))) {
                            Ok(()) => (),
                            Err(std::sync::mpsc::TrySendError::Full((req, _))) => {
                                *http_responses.entry(429).or_default() += 1;
                                respond(req, 429, br#"{"error":"probe queue full"}"#.to_vec());
                            }
                            Err(std::sync::mpsc::TrySendError::Disconnected(_)) => {
                                anyhow::bail!("probe workers exited")
                            }
                        },
                        Err(e) => {
                            let (code, body) = http_reply(Err(e));
                            *http_responses.entry(code).or_default() += 1;
                            respond(req, code, body);
                        }
                    }
                    continue;
                }

                for code in completed.try_iter() {
                    *http_responses.entry(code).or_default() += 1;
                }
                let shutdown = req.method().as_str() == "POST"
                    && req.url().split('?').next() == Some("/api/shutdown");
                let reply = route(
                    &mut req,
                    &auth,
                    &plane,
                    &mut store,
                    &sampling_error,
                    &http_responses,
                );
                let (code, body) = http_reply(reply);
                *http_responses.entry(code).or_default() += 1;
                respond(req, code, body);
                if shutdown && code == 200 {
                    break;
                }
            }

            drop(pending);

            for p in workers {
                p.join().map_err(|_| anyhow!("probe worker panicked"))?;
            }
            if let Some(job) = refresh {
                if let Err(error) = job
                    .join()
                    .map_err(|_| anyhow!("subscription refresh worker panicked"))?
                {
                    eprintln!(
                        "scheduled merge failed: {}",
                        http::redact(&error.to_string())
                    );
                }
            }
            drop(stop);
            if let Some(s) = sampler {
                s.join()
                    .map_err(|_| anyhow!("traffic sampler panicked"))??
            }
            plane.lock().unwrap().disconnect()
        });
        Ok(Self {
            bootstrap,
            stop_flag,
            server,
            worker,
        })
    }
    pub fn is_finished(&self) -> bool {
        self.worker.is_finished()
    }
    pub fn stop(self) -> Result<()> {
        self.stop_flag
            .store(true, std::sync::atomic::Ordering::Release);
        self.server.unblock();
        self.wait()
    }
    pub fn wait(self) -> Result<()> {
        self.worker
            .join()
            .map_err(|_| anyhow!("loopback worker panicked"))?
    }
}

fn http_reply(result: Result<(u16, Vec<u8>)>) -> (u16, Vec<u8>) {
    match result {
        Ok(v) => v,
        Err(e) => (
            400,
            json!({"error":http::redact(&format!("{e:#}"))})
                .to_string()
                .into_bytes(),
        ),
    }
}
fn respond(req: Request, code: u16, body: Vec<u8>) {
    let response = Response::from_data(body)
        .with_status_code(code)
        .with_header(Header::from_bytes("Content-Type", "application/json").unwrap());
    if let Err(e) = req.respond(response) {
        eprintln!("loopback response: {e}");
    }
}

fn prepare_probe<P: TunnelDataPlane>(
    req: &mut Request,
    store: &Store,
    plane: &Mutex<P>,
) -> Result<impl FnOnce() -> Result<Vec<u8>> + Send + 'static> {
    let mut bytes = Vec::new();
    req.as_reader()
        .take(1024 * 1024 + 1)
        .read_to_end(&mut bytes)?;
    ensure!(bytes.len() <= 1024 * 1024, "request exceeds 1 MiB");
    let args: Value = serde_json::from_slice(&bytes)?;
    let hops: Vec<String> = serde_json::from_value(args["hops"].clone())?;
    let chain = config::chain_nodes(store.configs()?, &hops)?;
    let measured = chrono::Utc::now().timestamp_millis();
    let target = match args.get("url") {
        Some(url) => store::web_url(url.as_str().context("invalid probe URL")?)?.to_string(),
        None => checks::target(&store.dir)?,
    };
    let job = plane.lock().unwrap().probe_job(chain, target.clone())?;
    let db = store::existing_connection(&store.dir, rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE)?;
    Ok(move || {
        let result = job();
        let mut row = json!({"hops":hops,"measured":measured,"target":target});
        match result {
            Ok((first, warm)) => {
                row["delay"] = json!(first);
                row["warmDelay"] = json!(warm);
            }
            Err(e) => row["error"] = json!(http::redact(&e.to_string())),
        }
        db.execute("INSERT OR REPLACE INTO probe_results(name,latency_ms,error,timestamp,report) VALUES(?,?,?,?,?)",rusqlite::params![serde_json::to_vec(&hops)?,row["delay"].as_i64(),row["error"].as_str(),measured/1000,row.to_string()])?;
        Ok(serde_json::to_vec(&row)?)
    })
}
fn active_node_label(
    plane: &impl TunnelDataPlane,
    store: &Store,
    connected: bool,
) -> Result<Option<String>> {
    if !connected {
        return Ok(None);
    }
    if store.chain()?["enabled"] == true {
        return Ok(Some("SmartVPNChain".into()));
    }
    let group = plane.controller()?.json("/proxies/SmartVPN")?;
    Ok(Some(
        group["now"]
            .as_str()
            .context("missing active proxy")?
            .to_owned(),
    ))
}

fn route<P: TunnelDataPlane>(
    req: &mut Request,
    auth: &str,
    plane: &Mutex<P>,
    store: &mut Store,
    sampling_error: &Mutex<Option<String>>,
    http_responses: &std::collections::BTreeMap<u16, u64>,
) -> Result<(u16, Vec<u8>)> {
    let method = req.method().as_str().to_owned();
    let path = req.url().split('?').next().unwrap().to_owned();
    let json_reply = |code, value: Value| Ok((code, serde_json::to_vec(&value)?));
    let public = method == "GET" && path == "/api/identify";
    if !public
        && !req
            .headers()
            .iter()
            .any(|h| h.field.equiv("Authorization") && h.value.as_str() == auth)
    {
        return json_reply(401, json!({"error":"Bearer token required"}));
    }
    let mut plane = plane.lock().unwrap();
    let mut body = Vec::new();
    if method == "POST" || method == "PUT" {
        req.as_reader()
            .take(1024 * 1024 + 1)
            .read_to_end(&mut body)?;
        ensure!(body.len() <= 1024 * 1024, "request exceeds 1 MiB");
    }
    let target = url::Url::parse(&format!("http://localhost{}", req.url()))?;
    let query = |key: &str| {
        target
            .query_pairs()
            .find(|(k, _)| k == key)
            .map(|(_, v)| v.into_owned())
    };
    let data = || serde_json::from_slice::<Value>(&body);
    if matches!(
        (method.as_str(), path.as_str()),
        ("POST" | "PUT", "/api/nodes/select") | ("PUT", "/api/proxies/SmartVPN")
    ) && store.chain()?["enabled"] == true
    {
        return json_reply(
            409,
            json!({"error":"disable the active chain before selecting a single node"}),
        );
    }
    match (method.as_str(), path.as_str()) {
        ("GET", "/api/state") => {
            let settings = store.settings()?;
            let mut state = settings.clone();
            let nodes = store.nodes()?;
            state
                .as_object_mut()
                .unwrap()
                .extend(nodes.as_object().unwrap().clone());
            let status = plane.status()?;
            state["health"] = json!({"params":store.pref("health")?});
            state["tunAvailable"] = json!(cfg!(target_os = "android"));
            state["chinaDirect"] = store.direct.state(&store.dir, status.connected)?;
            state["lockedRegion"] = store.pref("lockedRegion")?;
            state["tunActive"] = json!(status.connected && status.tunnel_authorized);
            if let Some(name) = active_node_label(&*plane, store, status.connected)? {
                state["activeNode"] = json!(name);
            }
            state.as_object_mut().unwrap().extend(json!({"connected":status.connected,"mixedPort":status.mixed_port,"settings":settings,
            "trafficPolicy":{"domesticWeb":settings["domesticWeb"],"dnsProtection":settings["dnsProtection"],"webRTCProtection":settings["webRTCProtection"]},
            "edition":"rust-pure","kernel":"clash-rs","version":VERSION,"dataDir":store.dir,"home":store.dir}).as_object().unwrap().clone());
            json_reply(200, state)
        }
        ("POST", "/api/direct/refresh") => {
            store.direct.refresh(&store.dir, true)?;
            json_reply(
                200,
                store.direct.state(&store.dir, plane.status()?.connected)?,
            )
        }
        ("GET", "/api/settings") => json_reply(200, store.settings()?),
        ("POST" | "PUT", "/api/settings") => {
            let patch = data()?;
            if plane.status()?.connected {
                let settings = store.settings()?;
                let changes_runtime = patch
                    .as_object()
                    .context("expected settings object")?
                    .iter()
                    .any(|(key, value)| key != "subscriptionUrl" && settings[key] != *value);
                if changes_runtime {
                    return json_reply(
                        409,
                        json!({"error":"disconnect before changing tunnel settings"}),
                    );
                }
            }
            json_reply(200, store.update(&patch)?)
        }
        ("GET", "/api/nodes") => json_reply(200, store.nodes()?),
        ("GET", "/api/nodes/manual") => json_reply(
            200,
            json!({"nodes":store.manual_nodes()?,"running":plane.status()?.connected}),
        ),
        ("POST", "/api/nodes/manual") => json_reply(200, store.add_node(&data()?)?),
        ("DELETE", "/api/nodes/manual") => {
            store.remove_node(&query("name").context("missing name")?)?;
            json_reply(200, json!({"ok":true}))
        }
        ("POST" | "PUT", "/api/nodes/select") => {
            let data = data()?;
            let name = data["name"].as_str().context("missing node name")?;
            {
                let lock = store.pref("lockedRegion")?;
                if lock != "" {
                    ensure!(
                        store
                            .region_pool(lock.as_str().unwrap())?
                            .iter()
                            .any(|n| n == name),
                        "outside locked region"
                    );
                }
            }
            if plane.status()?.connected {
                let (code, bytes) =
                    plane
                        .controller()?
                        .request("PUT", "/proxies/SmartVPN", &body)?;
                if code != 202 {
                    return Ok((code, bytes));
                }
            } else {
                ensure!(
                    store.configs()?.iter().any(|n| n["name"] == name),
                    "unknown node"
                );
            }
            if let Some(previous) = store.selected()? {
                if previous != name {
                    store.record_switch(&previous, name, "manual")?;
                }
            }
            store.select(name)?;
            json_reply(200, store.nodes()?)
        }
        ("GET", "/api/subscription/policy") => {
            let doc = store.active_catalog()?;
            let rules: Vec<_> = doc
                .rules
                .iter()
                .map(|r| json!({"source":r,"applied":r}))
                .collect();
            json_reply(
                200,
                json!({"rules":rules,"running":plane.status()?.connected,"enabled":store.settings()?["importPolicy"]}),
            )
        }
        ("POST", "/api/health/patrol") => {
            let target = checks::target(&store.dir)?;
            let timeout = store.pref("health")?["connectTimeoutMs"].as_u64().unwrap();
            json_reply(
                200,
                metrics::latency(plane.controller()?, store, &target, timeout as u32)?,
            )
        }
        ("POST", "/api/regions/verify") => {
            let refresh = data()?["refresh"]
                .as_bool()
                .context("invalid refresh flag")?;
            store.verify_regions(&*plane, refresh)?;
            json_reply(200, store.region_report()?)
        }
        ("POST", "/api/region/lock") => {
            let args = data()?;
            let country = args["country"].as_str().context("missing country")?;
            let chain = store.chain()?;
            if !country.is_empty() && chain["enabled"] == true {
                let hops: Vec<String> = serde_json::from_value(chain["hops"].clone())?;
                let nodes = config::chain_nodes(store.configs()?, &hops)?;
                ensure!(
                    plane.region(nodes)?["country"] == country,
                    "chain exit outside locked region"
                );
            } else if !country.is_empty() {
                let pool = store.region_pool(country)?;
                ensure!(!pool.is_empty(), "no verified nodes in region");
                if plane.status()?.connected {
                    let c = plane.controller()?;
                    let current = c.active_leaf()?;
                    if !pool.contains(&current) {
                        let (code, _) = c.request(
                            "PUT",
                            "/proxies/SmartVPN",
                            &serde_json::to_vec(&json!({"name":pool[0]}))?,
                        )?;
                        ensure!(code == 202, "region switch HTTP {code}");
                        store.record_switch(&current, &pool[0], "region_lock")?;
                    }
                }
            }
            store.set_pref("lockedRegion", &json!(country))?;
            json_reply(200, store.region_report()?)
        }
        ("GET", "/api/regions") => json_reply(200, store.region_report()?),
        ("GET", "/api/events") => json_reply(
            200,
            json!({"events":store.events(query("limit").as_deref().unwrap_or("50").parse()?)?}),
        ),
        ("GET", "/api/health/params") => json_reply(200, store.pref("health")?),
        ("PUT" | "POST", "/api/health/params") => {
            let v = data()?;
            for key in store.pref("health")?.as_object().unwrap().keys() {
                ensure!(
                    v[key].as_u64().is_some_and(|n| n > 0 && n <= 86400),
                    "invalid {key}"
                );
            }
            store.set_pref("health", &v)?;
            json_reply(200, v)
        }
        ("GET", "/api/path-checks") => json_reply(200, store.path_report()?),
        ("PUT", "/api/path-checks") => {
            store.save_paths(data()?)?;
            json_reply(200, store.path_report()?)
        }
        ("POST", "/api/path-checks/run") => {
            let status = plane.status()?;
            json_reply(
                200,
                store.run_paths(plane.controller()?, status.mixed_port)?,
            )
        }
        ("POST", "/api/path-checks/recovery/cancel") => {
            let mut status = store.pref("pathStatus")?;
            status["retryAt"] = json!("");
            status["retryMode"] = json!("");
            store.set_pref("pathStatus", &status)?;
            json_reply(200, store.path_report()?)
        }
        ("POST", "/api/subscriptions/auto-merge") => {
            let hours = data()?["hours"]
                .as_u64()
                .context("invalid auto-merge hours")?;
            ensure!(hours <= 8760, "auto-merge interval too large");
            store.set_pref("mergeHours", &json!(hours))?;
            json_reply(200, json!({"autoMergeHours":hours}))
        }
        ("GET", "/api/nodes/probes") => json_reply(200, json!({"results":store.probes()?})),
        ("GET", "/api/latency") => {
            let c = plane.controller()?;
            let name = c.active_leaf()?;
            let (overall, delay) = c.delay(
                &name,
                query("url")
                    .as_deref()
                    .unwrap_or(&checks::target(&store.dir)?),
            )?;
            json_reply(
                200,
                json!({"node":name,"overall":overall,"delay":delay,"latency":delay,"latencyMs":delay}),
            )
        }
        ("POST", "/api/speedtest") => json_reply(
            200,
            checks::speed(
                &store.dir,
                plane.status()?.mixed_port,
                data()?["megaBytes"]
                    .as_u64()
                    .context("invalid speed test size")?,
            )?,
        ),
        ("POST", "/api/diagnose") => {
            let status = plane.status()?;
            json_reply(
                200,
                checks::diagnose(plane.controller()?, status, &store.dir)?,
            )
        }
        #[cfg(windows)]
        ("GET", "/api/tun") | ("POST", "/api/tun/check") => json_reply(
            200,
            json!({"mode":"external","available":false,"active":false,"checks":[{"name":"Windows TUN","state":"unknown","detail":"Managed externally"}]}),
        ),
        #[cfg(target_os = "android")]
        ("GET", "/api/tun") | ("POST", "/api/tun/check") => {
            let s = plane.status()?;
            let iface = plane.tunnel_interface()?;
            json_reply(
                200,
                json!({"mode":"vpn","available":true,"active":s.connected,"interface":iface,"mtu":1500,"policy":plane.running_policy(),
                "checks":[{"name":"VpnService fd","state":if s.tunnel_fd.is_some(){"ok"}else{"warn"},"detail":if s.connected{"connected"}else{"disconnected"}}]}),
            )
        }
        ("GET" | "POST", "/api/nodes/latency") => {
            let target = match query("url") {
                Some(url) => url,
                None => checks::endpoint(
                    &store.dir,
                    "latency-target.txt",
                    "https://www.gstatic.com/generate_204",
                )?,
            };
            let timeout = query("timeout")
                .map(|v| v.parse())
                .transpose()?
                .unwrap_or(checks::DEFAULT_PROBE_TIMEOUT_MS);
            json_reply(
                200,
                metrics::latency(plane.controller()?, store, &target, timeout)?,
            )
        }
        ("GET", "/api/traffic/history") => {
            let mut history = history::query(
                store,
                query("granularity")
                    .or_else(|| query("period"))
                    .as_deref()
                    .unwrap_or("day"),
                query("date").as_deref(),
                query("group").as_deref().unwrap_or("connection"),
                query("app").as_deref(),
                query("offset").as_deref().unwrap_or("0").parse()?,
                query("limit").as_deref().unwrap_or("100").parse()?,
            )?;
            history["samplingError"] = json!(*sampling_error.lock().unwrap());
            json_reply(200, history)
        }
        ("DELETE", "/api/nodes/chain/profile") => {
            if plane.status()?.connected {
                return json_reply(409, json!({"error":"disconnect before changing chain"}));
            }
            store.delete_profile(&query("name").context("missing profile name")?)?;
            json_reply(200, json!({"ok":true}))
        }
        ("PUT", "/api/nodes/chain/profile") => {
            if plane.status()?.connected {
                return json_reply(409, json!({"error":"disconnect before changing chain"}));
            }
            json_reply(200, store.save_profile(&data()?)?)
        }
        ("GET", "/api/nodes/chain") => json_reply(200, store.chain()?),
        ("POST" | "PUT", "/api/nodes/chain") => {
            if plane.status()?.connected {
                return json_reply(409, json!({"error":"disconnect before changing chain"}));
            }
            json_reply(200, store.set_chain(&data()?)?)
        }
        (
            "POST",
            "/api/subscriptions/quota"
            | "/api/subscriptions/refresh"
            | "/api/subscriptions/activate"
            | "/api/subscriptions/fetch"
            | "/api/nodes/fetch"
            | "/api/nodes/refresh"
            | "/api/subscriptions/merge",
        ) => {
            let connected = plane.status()?.connected;
            drop(plane);
            if connected
                && !matches!(
                    path.as_str(),
                    "/api/subscriptions/quota"
                        | "/api/subscriptions/refresh"
                        | "/api/subscriptions/fetch"
                        | "/api/nodes/fetch"
                        | "/api/nodes/refresh"
                )
            {
                return json_reply(
                    409,
                    json!({"error":"disconnect before changing active subscription"}),
                );
            }
            if path.ends_with("/merge") {
                return json_reply(200, store.merge_sources(true)?);
            }
            let args = if body.is_empty() { json!({}) } else { data()? };
            let id = store.source_id(
                args.get("id")
                    .map(|v| v.as_str().context("invalid subscription id"))
                    .transpose()?,
            )?;
            if path.ends_with("/quota") {
                json_reply(200, store.quota(&id)?)
            } else {
                let mut result = if id == "__merged__" && path != "/api/subscriptions/activate" {
                    store.merge_sources(true)?
                } else {
                    store.fetch(&id, path != "/api/subscriptions/refresh")?
                };
                if path.starts_with("/api/nodes/") {
                    result["nodes"] = store.nodes()?["nodes"].take()
                }
                if path == "/api/subscriptions/refresh" && id != "__merged__" {
                    result["subscription"] = store
                        .subscriptions()?
                        .into_iter()
                        .find(|s| s["id"] == id)
                        .context("missing subscription")?;
                }
                json_reply(200, result)
            }
        }
        ("GET", "/api/subscriptions") => json_reply(
            200,
            json!({"subscriptions":store.subscriptions()?,"autoMergeHours":store.pref("mergeHours")?,"running":plane.status()?.connected}),
        ),
        ("GET", "/api/diagnostics/subscription") => {
            let doc = store.active_catalog()?;
            json_reply(
                200,
                json!({"userAgent":"clash.meta","appliedFormat":doc.applied_format,"cache":{"format":"clash","selectableNodes":doc.proxies.len(),"rules":doc.rules.len()},"running":plane.status()?.connected}),
            )
        }
        ("POST", "/api/subscriptions/update") => {
            #[derive(serde::Deserialize)]
            #[serde(deny_unknown_fields)]
            struct Update {
                id: String,
                label: Option<String>,
                prefix: Option<String>,
                enabled: Option<bool>,
            }
            let patch: Update = serde_json::from_slice(&body)?;
            ensure!(
                patch.label.is_some() || patch.prefix.is_some() || patch.enabled.is_some(),
                "empty subscription update"
            );
            ensure!(store.db.execute("UPDATE subscriptions SET name=coalesce(?1,name),prefix=coalesce(?2,prefix),enabled=coalesce(?3,enabled) WHERE id=?4",rusqlite::params![patch.label,patch.prefix,patch.enabled,patch.id])?==1,"unknown subscription");
            json_reply(200, json!({"ok":true}))
        }
        ("POST", "/api/subscriptions/add") => json_reply(200, store.add_sub(&data()?)?),
        ("PUT", "/api/subscriptions") => {
            #[derive(serde::Deserialize)]
            struct Rename {
                id: String,
                label: String,
            }
            let rename: Rename = serde_json::from_slice(&body)?;
            let label = rename.label.trim();
            ensure!(
                (1..=40).contains(&label.chars().count()),
                "invalid subscription label"
            );
            if store.db.execute(
                "UPDATE subscriptions SET name=? WHERE id=?",
                rusqlite::params![label, rename.id],
            )? == 0
            {
                return json_reply(404, json!({"error":"unknown subscription"}));
            }
            json_reply(200, json!({"ok":true}))
        }
        ("DELETE", "/api/subscriptions") => {
            ensure!(
                store.db.execute(
                    "DELETE FROM subscriptions WHERE id=?",
                    [query("id").context("missing id")?]
                )? == 1,
                "unknown subscription"
            );
            json_reply(200, json!({"ok":true}))
        }
        ("GET", "/api/traffic") => {
            if !plane.status()?.connected {
                return json_reply(200, json!({"available":false,"reason":"disconnected"}));
            }
            json_reply(200, metrics::traffic(plane.connections()?, store)?)
        }
        ("POST", "/api/shutdown") => {
            plane.disconnect()?;
            json_reply(200, json!({"ok":true}))
        }

        ("GET", "/api/identify") => json_reply(
            200,
            json!({"app":"SmartVPN","edition":"rust-pure","version":VERSION,"kernel":"clash-rs","running":plane.status()?.connected}),
        ),
        ("GET", "/api/status") => {
            let snapshot = plane.status()?;
            let active = active_node_label(&*plane, store, snapshot.connected)?;
            let mut status = serde_json::to_value(snapshot)?;
            if let Some(name) = active {
                status["activeNode"] = json!(name);
            }
            status["version"] = json!(VERSION);
            status["kernel"] = json!("clash-rs");
            status["proxyEnabled"] = json!(false);
            status["httpResponses"] = json!(http_responses);
            json_reply(200, status)
        }
        ("POST", "/api/connect") => {
            let mut auto_switched = false;
            if body.is_empty() {
                let mut nodes = store.configs()?;
                for n in &mut nodes {
                    n.as_object_mut().unwrap().remove("smartvpn-source");
                }
                ensure!(!nodes.is_empty(), "add a manual node before connecting");
                let settings = store.settings()?;
                let selected = store.selected()?;
                let chain = store.chain()?;
                let mut choices: Vec<_> = nodes
                    .iter()
                    .map(|n| n["name"].as_str().unwrap().to_owned())
                    .collect();
                if chain["enabled"] == true {
                    let hops: Vec<String> = serde_json::from_value(chain["hops"].clone())?;
                    nodes = config::chain_nodes(nodes, &hops)?;
                    choices = vec![hops.last().unwrap().clone()];
                } else if let Some(name) = selected.as_deref() {
                    let i = choices
                        .iter()
                        .position(|n| *n == name)
                        .context("selected node absent from profile")?;
                    choices.swap(0, i);
                }
                {
                    let lock = store.pref("lockedRegion")?;
                    if lock != "" && chain["enabled"] == true {
                        ensure!(
                            plane.region(nodes.clone())?["country"] == lock,
                            "chain exit outside locked region"
                        );
                    } else if lock != "" {
                        store.verify_regions(&*plane, false)?;
                        let pool = store.region_pool(lock.as_str().unwrap())?;
                        choices.retain(|n| pool.contains(n));
                        ensure!(!choices.is_empty(), "locked region has no verified proxy");
                    }
                }
                let mixed = TcpListener::bind((
                    "127.0.0.1",
                    settings["socksPort"].as_u64().unwrap() as u16,
                ))?;
                let ctrl = TcpListener::bind(("127.0.0.1", 0))?;
                let mut providers = json!({});
                let downloaded = store.dir.join("providers/china-ip.txt");
                let use_downloaded = downloaded.try_exists()?;
                if use_downloaded {
                    providers["SmartVPNChina"] = json!(downloaded);
                } else if settings["domesticWeb"] == true || settings["importPolicy"] == true {
                    let dir = store.dir.join("providers");
                    std::fs::create_dir_all(&dir)?;
                    let path = dir.join("china-bundled.txt");
                    std::fs::write(&path, include_bytes!("../assets/china-ip.txt"))?;
                    providers["SmartVPNChina"] = json!(path);
                }
                let catalog = store.active_catalog()?;
                let profile = json!({"mixedPort":mixed.local_addr()?.port(),"controllerPort":ctrl.local_addr()?.port(),"proxies":[],"choices":choices,
                    "proxyProviders":{},"ruleProviders":providers,"webRtcProtection":settings["webRTCProtection"],"importPolicy":settings["importPolicy"],"subscriptionRuleProviders":catalog.rule_providers,"subscriptionRules":catalog.rules,"domesticDirect":settings["domesticWeb"],"preventDnsLeaks":settings["dnsProtection"]});
                drop((mixed, ctrl));
                #[cfg(target_os = "windows")]
                let profile = json!({"directory":store.dir,"config":profile});
                store.direct.mounted = if use_downloaded
                    && (settings["domesticWeb"] == true
                        || (settings["dnsProtection"] == false
                            && settings["webRTCProtection"] == false))
                {
                    Some(std::fs::metadata(store.dir.join("providers/china-ip.txt"))?.modified()?)
                } else {
                    None
                };
                if settings["domesticWeb"] == true {
                    store.direct.refresh(&store.dir, false)?;
                }
                plane.connect(&serde_json::to_vec(&profile)?, &nodes)?;
                {
                    let selection = (|| -> Result<bool> {
                        if chain["enabled"] == true {
                            return Ok(false);
                        }
                        let c = plane.controller()?;
                        let from = c.active_leaf()?;
                        let target = checks::target(&store.dir)?;
                        let Some(best) =
                            metrics::connect_choice(&from, &choices, store, |name, timeout| {
                                Ok(metrics::probe_delay(c, name, &target, timeout)?.0)
                            })?
                        else {
                            return Ok(false);
                        };
                        let (code, _) = c.request(
                            "PUT",
                            "/proxies/SmartVPN",
                            &serde_json::to_vec(&json!({"name":best}))?,
                        )?;
                        ensure!(code == 202, "controller selection HTTP {code}");
                        store.record_switch(&from, best, "unhealthy")?;
                        Ok(true)
                    })();
                    match selection {
                        Ok(changed) => auto_switched = changed,
                        Err(e) => {
                            plane.disconnect()?;
                            return Err(e);
                        }
                    }
                }
            } else {
                if store.chain()?["enabled"] == true {
                    return json_reply(
                        409,
                        json!({"error":"custom connect profile conflicts with active chain"}),
                    );
                }
                plane.connect(&body, &[])?;
                store.direct.mounted = None;
            }
            store.sample = None;
            json_reply(200, json!({"connected":true,"autoSwitched":auto_switched}))
        }
        ("POST", "/api/disconnect") => {
            plane.disconnect()?;
            json_reply(200, json!({"connected":false}))
        }
        ("GET", "/api/proxies" | "/api/connections") | ("PUT", "/api/proxies/SmartVPN") => {
            plane.controller()?.request(&method, &path[4..], &body)
        }
        _ => json_reply(404, json!({"error":"unknown route"})),
    }
}

#[cfg(test)]
struct TestPlane;
#[cfg(test)]
impl TunnelDataPlane for TestPlane {
    fn status(&mut self) -> Result<PlaneStatus> {
        Ok(PlaneStatus::default())
    }
    fn connect(&mut self, _: &[u8], _: &[Value]) -> Result<()> {
        anyhow::bail!("test plane has no kernel")
    }
    fn disconnect(&mut self) -> Result<()> {
        Ok(())
    }
    fn controller(&self) -> Result<&Controller> {
        anyhow::bail!("no running controller")
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    struct OnlinePlane(Controller);
    impl TunnelDataPlane for OnlinePlane {
        fn status(&mut self) -> Result<PlaneStatus> {
            Ok(PlaneStatus {
                connected: true,
                ..Default::default()
            })
        }
        fn controller(&self) -> Result<&Controller> {
            Ok(&self.0)
        }
        fn connect(&mut self, _: &[u8], _: &[Value]) -> Result<()> {
            unreachable!()
        }
        fn disconnect(&mut self) -> Result<()> {
            Ok(())
        }
        fn connections(&self) -> Result<Value> {
            Ok(json!({"connections":[]}))
        }
        fn probe_job(&self, _: Vec<Value>, _: String) -> Result<ProbeJob> {
            Ok(Box::new(|| Ok((5, 2))))
        }
    }

    #[test]
    fn connected_routes_report_chain_or_current_controller_selection() -> Result<()> {
        let server = Server::http("127.0.0.1:0").unwrap();
        let plane = Arc::new(Mutex::new(OnlinePlane(Controller {
            port: server.server_addr().to_ip().unwrap().port(),
            secret: String::new(),
        })));
        let mock = thread::spawn(move || {
            for _ in 0..2 {
                let req = server
                    .recv_timeout(Duration::from_secs(5))
                    .unwrap()
                    .unwrap();
                assert_eq!(req.url(), "/proxies/SmartVPN");
                req.respond(Response::from_string(r#"{"now":"Single-Old"}"#))
                    .unwrap();
            }
        });
        let dir = tempfile::tempdir()?;
        let store = Store::open(dir.path())?;
        for name in ["Hop-A", "Hop-B", "Single-Old"] {
            store.add_node(&json!({"name":name,"uri":"socks5://localhost:12345"}))?;
        }
        store.select("Single-Old")?;
        let runtime = Runtime::with_store(plane, store)?;
        let control = Store::background(dir.path())?;
        for (enabled, exit, expected) in [
            (false, "Single-Old", "Single-Old"),
            (true, "Hop-B", "SmartVPNChain"),
            (true, "Single-Old", "SmartVPNChain"),
        ] {
            control.set_chain(&json!({"enabled":enabled,"hops":["Hop-A",exit]}))?;
            for route in ["state", "status"] {
                let url = format!("http://{}/api/{route}", runtime.bootstrap.address);
                let doc: Value = http::agent()
                    .get(&url)
                    .set(
                        "Authorization",
                        &format!("Bearer {}", runtime.bootstrap.token),
                    )
                    .call()?
                    .into_json()?;
                assert_eq!(doc["activeNode"], expected);
                if route == "state" {
                    assert_eq!(doc["selectedNode"], exit);
                }
            }
        }
        mock.join().unwrap();
        runtime.stop()
    }

    #[test]
    fn concurrent_probe_writes_and_runtime_reads_preserve_wal() -> Result<()> {
        let dir = tempfile::tempdir()?;
        let store = Store::open(dir.path())?;
        for i in 0..4 {
            store.add_node(&json!({"name":format!("hop{i}"),"uri":"socks5://localhost:12345"}))?;
        }
        store.set_chain(&json!({"hops":["hop0","hop1"]}))?;
        drop(store);
        let plane = OnlinePlane(Controller {
            port: 0,
            secret: String::new(),
        });
        let runtime = Runtime::start(Arc::new(Mutex::new(plane)), dir.path())?;
        let base = format!("http://{}/api", runtime.bootstrap.address);
        let bearer = format!("Bearer {}", runtime.bootstrap.token);
        thread::scope(|scope| -> Result<()> {
            let mut workers = Vec::new();
            for i in 0..4 {
                let url = format!("{base}/nodes/chain/probe");
                let auth = &bearer;
                workers.push(scope.spawn(move || -> Result<()> {
                    for _ in 0..32 {
                        let doc: Value = http::agent()
                            .post(&url)
                            .set("Authorization", auth)
                            .send_json(
                                json!({"hops":[format!("hop{i}")],"url":"http://localhost/check"}),
                            )?
                            .into_json()?;
                        assert_eq!(doc["delay"], 5);
                    }
                    Ok(())
                }));
            }
            let background = Store::background(dir.path())?;
            for i in 0..32 {
                background.set_pref("mergeAt", &json!(i))?;
                let doc: Value = http::agent()
                    .get(&format!("{base}/state"))
                    .set("Authorization", &bearer)
                    .call()?
                    .into_json()?;
                assert_eq!(doc["activeNode"], "SmartVPNChain");
                assert!(android_game_bypass(dir.path())?.is_empty());
            }
            for worker in workers {
                worker.join().unwrap()?;
            }
            Ok(())
        })?;
        runtime.stop()?;
        let db = Store::background(dir.path())?.db;
        assert_eq!(
            db.query_row("SELECT count(*) FROM probe_results", [], |r| r
                .get::<_, i64>(0))?,
            4
        );
        assert_eq!(
            db.query_row("PRAGMA integrity_check", [], |r| r.get::<_, String>(0))?,
            "ok"
        );
        Ok(())
    }

    #[test]
    fn vpn_builder_reads_committed_game_bypass_during_wal_writes() -> Result<()> {
        let dir = tempfile::tempdir()?;
        let mut store = Store::open(dir.path())?;
        let tx = store.db.transaction()?;
        tx.execute(
            "UPDATE settings SET value=? WHERE key='gameBypass'",
            [r#"["example.game"]"#],
        )?;
        assert!(android_game_bypass(dir.path())?.is_empty());
        tx.commit()?;
        assert_eq!(android_game_bypass(dir.path())?, ["example.game"]);
        store
            .db
            .execute("DELETE FROM settings WHERE key='gameBypass'", [])?;
        assert!(android_game_bypass(dir.path()).is_err());
        Ok(())
    }

    #[test]
    fn connect_requests_only_the_selected_delay_in_large_catalog() {
        struct Connected(Controller);
        impl TunnelDataPlane for Connected {
            fn status(&mut self) -> Result<PlaneStatus> {
                Ok(PlaneStatus::default())
            }
            fn connect(&mut self, _: &[u8], nodes: &[Value]) -> Result<()> {
                ensure!(nodes.len() == 40);
                Ok(())
            }
            fn disconnect(&mut self) -> Result<()> {
                Ok(())
            }
            fn controller(&self) -> Result<&Controller> {
                Ok(&self.0)
            }
        }
        let server = Server::http("127.0.0.1:0").unwrap();
        let port = server.server_addr().to_ip().unwrap().port();
        let mock = thread::spawn(move || {
            for (path, reply) in [
                (
                    "/proxies/SmartVPN",
                    json!({"now":"selected","all":["selected"]}),
                ),
                ("/proxies/selected", json!({"type":"Socks5"})),
                ("/proxies/selected/delay", json!({"delay":15,"overall":48})),
            ] {
                let request = server
                    .recv_timeout(Duration::from_secs(3))
                    .unwrap()
                    .unwrap();
                assert_eq!(request.url().split('?').next().unwrap(), path);
                if path.ends_with("/delay") {
                    assert!(request.url().contains("timeout=5000"));
                }
                request
                    .respond(Response::from_string(reply.to_string()))
                    .unwrap();
            }
        });
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        for i in 0..39 {
            store
                .add_node(&json!({"name":format!("dead-{i}"),"uri":"socks5://localhost:9"}))
                .unwrap();
        }
        store
            .add_node(&json!({"name":"selected","uri":"socks5://localhost:12345"}))
            .unwrap();
        store.select("selected").unwrap();
        let plane = Arc::new(Mutex::new(Connected(Controller {
            port,
            secret: String::new(),
        })));
        let runtime = Runtime::with_store(plane, store).unwrap();
        let start = std::time::Instant::now();
        let response: Value = http::agent()
            .post(&format!("http://{}/api/connect", runtime.bootstrap.address))
            .set(
                "Authorization",
                &format!("Bearer {}", runtime.bootstrap.token),
            )
            .call()
            .unwrap()
            .into_json()
            .unwrap();
        assert_eq!(response, json!({"connected":true,"autoSwitched":false}));
        assert!(start.elapsed() < Duration::from_secs(2));
        mock.join().unwrap();
        runtime.stop().unwrap();
    }

    #[test]
    fn connect_probes_only_active_or_one_bounded_batch() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let names: Vec<_> = (0..40).map(|n| format!("node-{n}")).collect();
        // Chain probes store a JSON-encoded BLOB key, bulk delays a TEXT name.
        for hops in [vec!["node-39"], vec!["node-38", "node-37"]] {
            store
                .db
                .execute(
                    "INSERT INTO probe_results(name,latency_ms,timestamp,report) VALUES(?,2,1,?)",
                    rusqlite::params![
                        serde_json::to_vec(&hops).unwrap(),
                        json!({"hops":hops,"delay":2}).to_string()
                    ],
                )
                .unwrap();
        }
        assert_eq!(
            metrics::connect_choice(&names[0], &names, &store, |n, _| Ok(
                (n == "node-39").then_some(2)
            ))
            .unwrap(),
            Some("node-39")
        );
        let count = std::sync::atomic::AtomicUsize::new(0);
        let order = std::sync::atomic::Ordering::SeqCst;
        for reachable in [true, false] {
            count.store(0, order);
            let best = metrics::connect_choice(&names[0], &names, &store, |name, _| {
                count.fetch_add(1, order);
                Ok(if name == names[0] && reachable {
                    Some(10)
                } else if name == names[7] {
                    Some(5)
                } else {
                    None
                })
            })
            .unwrap();
            assert_eq!(count.load(order), if reachable { 1 } else { 9 });
            assert_eq!(best, if reachable { None } else { Some("node-7") });
        }
        assert_eq!(
            metrics::connect_choice(&names[0], &names, &store, |_, _| Ok(None)).unwrap(),
            None
        );
    }
    #[test]
    fn four_probes_leave_status_responsive() {
        struct Delayed(std::sync::mpsc::Sender<std::sync::mpsc::Sender<()>>);
        impl TunnelDataPlane for Delayed {
            fn probe_job(&self, _: Vec<Value>, _: String) -> Result<ProbeJob> {
                let started = self.0.clone();
                Ok(Box::new(move || {
                    let (release, rx) = std::sync::mpsc::channel();
                    started.send(release)?;
                    rx.recv_timeout(Duration::from_secs(4))?;
                    Ok((31, 19))
                }))
            }
            fn connect(&mut self, _: &[u8], _: &[Value]) -> Result<()> {
                unreachable!()
            }
            fn disconnect(&mut self) -> Result<()> {
                Ok(())
            }
            fn status(&mut self) -> Result<PlaneStatus> {
                Ok(PlaneStatus::default())
            }
            fn controller(&self) -> Result<&Controller> {
                unreachable!()
            }
        }
        let dir = tempfile::tempdir().unwrap();
        let s = Store::open(dir.path()).unwrap();
        s.db.execute("INSERT INTO active_catalog VALUES(1,?)",[json!({"proxies":[{"name":"n","server":"localhost","port":1080,"type":"socks5"}],"rules":[]}).to_string()]).unwrap();
        drop(s);
        let (started, rx) = std::sync::mpsc::channel();
        let r = Runtime::start(Arc::new(Mutex::new(Delayed(started))), dir.path()).unwrap();
        let base = format!("http://{}/api", r.bootstrap.address);
        let bearer = format!("Bearer {}", r.bootstrap.token);
        thread::scope(|scope| {
            let mut requests = Vec::new();
            for _ in 0..4 {
                requests.push(scope.spawn(|| {
                    http::agent()
                        .post(&format!("{base}/nodes/chain/probe"))
                        .set("Authorization", &bearer)
                        .send_json(json!({"hops":["n"]}))
                        .unwrap()
                        .into_json::<Value>()
                        .unwrap()
                }));
            }
            let releases = (0..4)
                .map(|_| rx.recv_timeout(Duration::from_secs(3)).unwrap())
                .collect::<Vec<_>>();
            assert_eq!(
                http::agent()
                    .get(&format!("{base}/status"))
                    .set("Authorization", &bearer)
                    .call()
                    .unwrap()
                    .status(),
                200
            );
            for release in releases {
                release.send(()).unwrap();
            }
            for task in requests {
                let r = task.join().unwrap();
                assert_eq!(
                    (r["delay"].as_u64(), r["warmDelay"].as_u64()),
                    (Some(31), Some(19))
                );
            }
        });
        r.stop().unwrap();
    }
    #[test]
    fn bearer_is_per_run_and_required_for_every_private_route() {
        let first = Runtime::with_store(
            Arc::new(Mutex::new(TestPlane)),
            Store::init(
                rusqlite::Connection::open_in_memory().unwrap(),
                Path::new(":memory:"),
            )
            .unwrap(),
        )
        .unwrap();
        let second = Runtime::with_store(
            Arc::new(Mutex::new(TestPlane)),
            Store::init(
                rusqlite::Connection::open_in_memory().unwrap(),
                Path::new(":memory:"),
            )
            .unwrap(),
        )
        .unwrap();
        assert_ne!(first.bootstrap.token, second.bootstrap.token);
        for (method, path) in [
            ("GET", "state"),
            ("GET", "settings"),
            ("POST", "direct/refresh"),
            ("GET", "latency"),
            ("POST", "diagnose"),
            ("POST", "nodes/chain/probe"),
            ("GET", "events"),
            ("GET", "path-checks"),
            ("PUT", "health/params"),
            ("POST", "subscriptions/update"),
            ("POST", "settings"),
            ("PUT", "settings"),
            ("GET", "nodes"),
            ("POST", "nodes/select"),
            ("PUT", "nodes/select"),
            ("POST", "nodes/manual"),
            ("DELETE", "nodes/manual"),
            ("GET", "nodes/latency"),
            ("GET", "subscriptions"),
            ("POST", "subscriptions/add"),
            ("DELETE", "subscriptions"),
            ("GET", "traffic/history"),
            ("POST", "subscriptions/activate"),
            ("POST", "subscriptions/refresh"),
            ("POST", "subscriptions/fetch"),
            ("POST", "nodes/fetch"),
            ("POST", "subscriptions/merge"),
            ("POST", "subscriptions/quota"),
            ("GET", "nodes/chain"),
            ("POST", "nodes/chain"),
            ("GET", "traffic"),
            ("POST", "shutdown"),
            ("GET", "status"),
            ("POST", "connect"),
            ("POST", "disconnect"),
            ("GET", "proxies"),
            ("PUT", "proxies/SmartVPN"),
            ("GET", "connections"),
        ] {
            let url = format!("http://{}/api/{path}", second.bootstrap.address);
            let req = http::agent().request(method, &url);
            assert!(matches!(
                req.clone().call(),
                Err(ureq::Error::Status(401, _))
            ));
            assert!(matches!(
                req.set(
                    "Authorization",
                    &format!("Bearer {}", first.bootstrap.token)
                )
                .call(),
                Err(ureq::Error::Status(401, _))
            ));
        }
        second.stop().unwrap();
        first.stop().unwrap();
    }
    #[test]
    fn loopback_contract() {
        let service = Runtime::with_store(
            Arc::new(Mutex::new(TestPlane)),
            Store::init(
                rusqlite::Connection::open_in_memory().unwrap(),
                Path::new(":memory:"),
            )
            .unwrap(),
        )
        .unwrap();
        let url = format!("http://{}", service.bootstrap.address);
        let id: Value = http::agent()
            .get(&format!("{url}/api/identify"))
            .call()
            .unwrap()
            .into_json()
            .unwrap();
        assert_eq!(
            id,
            json!({"app":"SmartVPN","edition":"rust-pure","version":VERSION,"kernel":"clash-rs","running":false})
        );
        let req = http::agent().get(&format!("{url}/api/status"));
        assert!(matches!(
            req.clone().call(),
            Err(ureq::Error::Status(401, _))
        ));
        let state: Value = req
            .set(
                "Authorization",
                &format!("Bearer {}", service.bootstrap.token),
            )
            .call()
            .unwrap()
            .into_json()
            .unwrap();
        let report: Value = http::agent()
            .get(&format!("http://{}/api/regions", service.bootstrap.address))
            .set(
                "Authorization",
                &format!("Bearer {}", service.bootstrap.token),
            )
            .call()
            .unwrap()
            .into_json()
            .unwrap();
        assert_eq!(report["job"]["total"], 0);
        assert_eq!(report["pool"], json!([]));
        assert_eq!(state["kernel"], "clash-rs");
        assert_eq!(state["connected"], false);
        assert_eq!(state["mixedPort"], 0);
        service.stop().unwrap();
    }
    #[test]
    fn slow_scheduled_download_leaves_api_responsive() {
        let dir = tempfile::tempdir().unwrap();
        let fixture = Server::http("127.0.0.1:0").unwrap();
        let store = Store::open(dir.path()).unwrap();
        store.add_sub(&json!({"label":"background","url":format!("http://{}/list",fixture.server_addr())})).unwrap();
        store.set_pref("mergeHours", &json!(1)).unwrap();
        let service = Runtime::with_store(Arc::new(Mutex::new(TestPlane)), store).unwrap();
        let download = fixture
            .recv_timeout(Duration::from_secs(5))
            .unwrap()
            .unwrap();
        let api = |route: &str| -> Value {
            http::agent()
                .get(&format!("http://{}/api/{route}", service.bootstrap.address))
                .set(
                    "Authorization",
                    &format!("Bearer {}", service.bootstrap.token),
                )
                .timeout(Duration::from_secs(1))
                .call()
                .unwrap()
                .into_json()
                .unwrap()
        };
        assert_eq!(api("status")["connected"], false);
        download
            .respond(
                Response::from_string("socks5://127.0.0.1:10001#background")
                    .with_header(Header::from_bytes("sUbScRiPtIoN-uSeRiNfO", "total=900").unwrap()),
            )
            .unwrap();
        let deadline = std::time::Instant::now() + Duration::from_secs(3);
        while api("subscriptions")["subscriptions"][0]["userInfo"]["total"] != 900 {
            assert!(std::time::Instant::now() < deadline);
            thread::sleep(Duration::from_millis(20));
        }
        assert_eq!(api("settings")["subscriptionUrl"], "__merged__");
        assert_eq!(api("nodes")["nodes"].as_array().unwrap().len(), 1);
        service.stop().unwrap();
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn connect_uses_pinned_exit_and_every_predecessor() {
        #[derive(Default)]
        struct Capture {
            profile: Value,
            nodes: Vec<Value>,
        }
        impl TunnelDataPlane for Capture {
            fn connect(&mut self, body: &[u8], nodes: &[Value]) -> Result<()> {
                self.profile = serde_json::from_slice(body)?;
                self.nodes = nodes.to_vec();
                Ok(())
            }
            fn status(&mut self) -> Result<PlaneStatus> {
                Ok(PlaneStatus::default())
            }
            fn disconnect(&mut self) -> Result<()> {
                Ok(())
            }
            fn controller(&self) -> Result<&Controller> {
                anyhow::bail!("capture has no controller")
            }
        }

        let temp = tempfile::tempdir().unwrap();
        let store = Store::open(temp.path()).unwrap();
        let hops = ["入口 [A]", "中继 B", "出口 [C]"];
        for name in hops.iter().rev() {
            store
                .add_node(&json!({"name":name,"uri":"socks5://127.0.0.1:1080"}))
                .unwrap();
        }
        store
            .set_chain(&json!({"enabled":true,"hops":hops}))
            .unwrap();
        let plane = Arc::new(Mutex::new(Capture::default()));
        let runtime = Runtime::with_store(plane.clone(), store).unwrap();
        let reply: Value = http::agent()
            .post(&format!("http://{}/api/connect", runtime.bootstrap.address))
            .set(
                "Authorization",
                &format!("Bearer {}", runtime.bootstrap.token),
            )
            .call()
            .unwrap()
            .into_json()
            .unwrap();
        assert_eq!(reply["connected"], true);
        {
            let captured = plane.lock().unwrap();
            assert_eq!(captured.profile["config"]["choices"], json!([hops[2]]));
            assert_eq!(captured.nodes.len(), 3);
            for (i, node) in captured.nodes.iter().enumerate() {
                assert_eq!(node["name"], hops[i]);
                assert_eq!(
                    node.get("dialer-proxy").and_then(Value::as_str),
                    i.checked_sub(1).map(|n| hops[n])
                );
            }
        }
        runtime.stop().unwrap();
    }

    #[test]
    fn connections_normalized() {
        for net in ["Tcp", "Udp"] {
            let mut doc = json!({"connections":[{"metadata":{"sourcePort":123,"destinationPort":"443","network":net}}]});
            normalize_connections(&mut doc).unwrap();
            assert_eq!(
                doc["connections"][0]["metadata"],
                json!({"sourcePort":"123","destinationPort":"443","network":net.to_lowercase()})
            );
        }
        assert!(normalize_connections(
            &mut json!({"connections":[{"metadata":{"network":"Tcp","sourcePort":65536}}]})
        )
        .is_err());
    }
}
