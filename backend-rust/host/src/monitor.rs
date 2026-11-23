//! Read-only HTTP transport. Both sockets are pinned to IPv4 loopback;
//! the caller owns the controller process and supplies its credentials.
use bytes::Bytes;
use http_body_util::{BodyExt, Empty, Full, Limited};
use hyper::{
    body::Incoming,
    header::{HeaderValue, AUTHORIZATION, CONTENT_LENGTH, CONTENT_TYPE},
    Request, Response,
};
use hyper_util::rt::TokioIo;
use serde::de::DeserializeOwned;
use serde_json::{json, Value};
use smartvpn_core::{http_core, http_dispatch_flow};
use std::{collections::BTreeMap, convert::Infallible, env, io, sync::Arc, time::Duration};
use tokio::{
    net::{TcpListener, TcpStream},
    sync::{Mutex, Semaphore},
    time::timeout,
};
mod traffic;

struct Controller {
    port: u16,
    auth: HeaderValue,
    node_timeout: u64,
    traffic: Mutex<traffic::Baseline>,
}
impl Controller {
    async fn get<T: DeserializeOwned>(
        &self,
        path: &str,
        limit: usize,
        budget: Duration,
    ) -> Result<T, ()> {
        timeout(budget, async {
            let stream = TcpStream::connect((std::net::Ipv4Addr::LOCALHOST, self.port)).await;
            let stream = stream.map_err(|_| ())?;
            let (mut tx, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream))
                .await
                .map_err(|_| ())?;
            tokio::spawn(async move {
                let _ = conn.await;
            });
            let req = Request::get(path)
                .header("Host", format!("127.0.0.1:{}", self.port))
                .header(AUTHORIZATION, self.auth.clone())
                .header("Connection", "close")
                .body(Empty::<Bytes>::new())
                .map_err(|_| ())?;
            let res = tx.send_request(req).await.map_err(|_| ())?;
            if res.status() != 200 {
                return Err(());
            }
            let body = Limited::new(res.into_body(), limit)
                .collect()
                .await
                .map_err(|_| ())?
                .to_bytes();
            serde_json::from_slice(&body).map_err(|_| ())
        })
        .await
        .map_err(|_| ())?
    }
    async fn group(&self, name: &str) -> Result<String, ()> {
        let value: Value = self
            .get(&format!("/proxies/{name}"), 1 << 20, Duration::from_secs(5))
            .await?;
        Ok(value["now"].as_str().unwrap_or_default().to_owned())
    }
    async fn delays(self: &Arc<Self>, ms: u64) -> Result<Option<BTreeMap<String, i64>>, ()> {
        let query = format!("timeout={ms}&url=https%3A%2F%2Fwww.youtube.com%2Fgenerate_204");
        let budget = Duration::from_millis(ms + 12000);
        let group: Option<BTreeMap<String, i64>> = self
            .get(&format!("/group/SmartVPN/delay?{query}"), 256 << 10, budget)
            .await?;
        if !group
            .as_ref()
            .is_some_and(|m| m.len() == 1 && m.contains_key("SmartVPN"))
        {
            return Ok(group);
        }
        #[derive(serde::Deserialize)]
        struct Members {
            all: Vec<String>,
        }
        #[derive(serde::Deserialize)]
        struct Delay {
            delay: i64,
        }
        let members: Members = self.get("/proxies/SmartVPN", 1 << 20, budget).await?;
        let mut tasks = tokio::task::JoinSet::new();
        let mut results = BTreeMap::new();
        for name in members.all {
            if matches!(
                name.as_str(),
                "DIRECT" | "REJECT" | "GLOBAL" | "SmartVPN" | "SmartVPNAuto" | "SmartVPNFallback"
            ) {
                continue;
            }
            if name.is_empty() {
                return Err(());
            }
            let mut escaped = String::new();
            for b in name.bytes() {
                if b.is_ascii_alphanumeric() || b"-_.~".contains(&b) {
                    escaped.push(b as char)
                } else {
                    use std::fmt::Write;
                    write!(escaped, "%{b:02X}").unwrap();
                }
            }
            let path = format!("/proxies/{escaped}/delay?{query}");
            let controller = self.clone();
            tasks.spawn(async move {
                controller
                    .get::<Delay>(&path, 16 << 10, budget)
                    .await
                    .map(|v| (name, v.delay))
            });
            if tasks.len() == 8 {
                let (name, value) = tasks.join_next().await.ok_or(())?.map_err(|_| ())??;
                results.insert(name, value);
            }
        }
        while let Some(result) = tasks.join_next().await {
            let (name, value) = result.map_err(|_| ())??;
            results.insert(name, value);
        }
        Ok(Some(results))
    }
    async fn latency(self: &Arc<Self>, all: bool, ms: u64) -> (u16, Vec<u8>) {
        let Ok(delays) = self.delays(ms).await else {
            return (
                502,
                if all {
                    b"could not measure the nodes\n".to_vec()
                } else {
                    b"could not measure the current node\n".to_vec()
                },
            );
        };
        if all {
            return json_response(json!({"latency":delays}));
        }
        let mut name = self.group("SmartVPN").await.unwrap_or_default();
        if matches!(name.as_str(), "SmartVPNAuto" | "SmartVPNFallback") {
            if let Ok(selected) = self.group(&name).await {
                name = selected;
            }
        }
        if name.is_empty() {
            return (502, b"could not read the current node\n".to_vec());
        }
        let delay = delays
            .as_ref()
            .and_then(|v| v.get(&name))
            .copied()
            .unwrap_or(0);
        if delay <= 0 {
            return (
                502,
                b"current node could not reach YouTube; select another node\n".to_vec(),
            );
        }
        json_response(json!({"latencyMs":delay,"selectedNode":name}))
    }
}
fn json_response(v: Value) -> (u16, Vec<u8>) {
    let mut b = v.to_string().into_bytes();
    b.push(b'\n');
    (200, b)
}
struct Monitor {
    reload_key: Option<HeaderValue>,
    writes: bool,
    resources: Option<(u16, HeaderValue)>,
    token: Vec<u8>,
    controller: Option<Arc<Controller>>,
}
impl Monitor {
    async fn handle(&self, req: Request<Incoming>) -> Result<Response<Full<Bytes>>, Infallible> {
        let method = req.method().as_str().as_bytes();
        let mut auth = req.headers().get_all(AUTHORIZATION).iter();
        let header = auth.next().map(HeaderValue::as_bytes).unwrap_or_default();
        let header = if auth.next().is_some() {
            &[][..]
        } else {
            header
        };
        let plan = http_dispatch_flow::plan(
            method,
            req.uri().path().as_bytes(),
            header,
            &self.token,
            cfg!(windows),
        );
        let (status, body) = match (plan.status, plan.route, self.controller.as_ref()) {
            (401, _, _) => (401, b"unauthorized\n".to_vec()),
            (404, _, _) => (404, b"404 page not found\n".to_vec()),
            (405, _, _) => (405, b"Method Not Allowed\n".to_vec()),
            (_, 1, c) => {
                let connected = match c {
                    Some(c) => c
                        .get::<Value>("/version", 16 << 10, Duration::from_secs(2))
                        .await
                        .is_ok_and(|v| v["version"].as_str().is_some_and(|s| !s.is_empty())),
                    None => false,
                };
                (200, http_core::identity_json(connected).to_vec())
            }
            (_, 9, Some(c)) => {
                let ms = req.headers().get("x-smartvpn-node-timeout-ms").map(|v| {
                    v.to_str()
                        .ok()?
                        .parse::<u64>()
                        .ok()
                        .filter(|n| (1000..=15000).contains(n))
                });
                match ms {
                    Some(None) => (400, b"invalid node timeout\n".to_vec()),
                    _ => {
                        c.latency(true, ms.flatten().unwrap_or(c.node_timeout))
                            .await
                    }
                }
            }
            (_, 16, Some(c)) => c.latency(false, 8000).await,
            (_, 23, Some(c)) => c.current_traffic().await,
            (_, 9 | 16 | 23, None) => super::idle_monitor_response(plan.route).unwrap(),
            _ => (501, b"unsupported monitor route\n".to_vec()),
        };
        let n = body.len();
        let data = if method == b"HEAD" {
            Bytes::new()
        } else {
            Bytes::from(body)
        };
        let mut response = Response::new(Full::new(data));
        *response.status_mut() = status.try_into().unwrap();
        let headers = response.headers_mut();
        headers.insert(
            CONTENT_TYPE,
            HeaderValue::from_static(if status == 200 {
                "application/json"
            } else {
                "text/plain; charset=utf-8"
            }),
        );
        headers.insert(CONTENT_LENGTH, n.into());
        if status == 405 {
            let mut allowed = std::collections::BTreeSet::new();
            for &(verb, path) in http_core::ROUTES {
                if path == req.uri().path() {
                    allowed.insert(verb);
                    if verb == "GET" {
                        allowed.insert("HEAD");
                    }
                }
            }
            let methods = allowed.into_iter().collect::<Vec<_>>().join(", ");
            headers.insert(
                hyper::header::ALLOW,
                HeaderValue::from_str(&methods).expect("static method names"),
            );
        }
        Ok(response)
    }
}
fn secret(key: &str) -> io::Result<String> {
    env::var(key)
        .ok()
        .filter(|s| !s.trim().is_empty() && s.trim() == s && HeaderValue::from_str(s).is_ok())
        .ok_or_else(|| io::Error::other(format!("missing or invalid {key}")))
}
pub fn serve(port: Option<u16>) -> io::Result<()> {
    serve_listener(port, None)
}
pub fn serve_managed(port: u16, key: Option<String>) -> io::Result<()> {
    let auth = key
        .map(|v| HeaderValue::from_str(&format!("Bearer {v}")))
        .transpose()
        .map_err(io::Error::other)?;
    serve_listener(Some(port), auth)
}
fn serve_listener(port: Option<u16>, reload_key: Option<HeaderValue>) -> io::Result<()> {
    let managed_v2 = reload_key.is_some();
    let token = secret("SMARTVPN_MONITOR_TOKEN")?.into_bytes();
    let controller = port
        .map(|port| -> io::Result<_> {
            let key = secret("SMARTVPN_MONITOR_CONTROLLER_SECRET")?;
            let default = if cfg!(target_os = "android") {
                6000
            } else {
                3000
            };
            let node_timeout = match env::var("SMARTVPN_MONITOR_NODE_TIMEOUT_MS") {
                Ok(s) => s
                    .parse::<u64>()
                    .ok()
                    .filter(|v| (1000..=15000).contains(v))
                    .ok_or_else(|| io::Error::other("invalid node timeout"))?,
                Err(_) => default,
            };
            Ok(Controller {
                port,
                auth: HeaderValue::from_str(&format!("Bearer {key}")).map_err(io::Error::other)?,
                node_timeout,
                traffic: Mutex::default(),
            })
        })
        .transpose()?;
    let monitor = Monitor {
        reload_key,
        token,
        controller: controller.map(Arc::new),
        writes: false,
        resources: None,
    };
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()?
        .block_on(async {
            let listener = TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0)).await?;
            println!(
                "{}{}",
                if managed_v2 { "v2 " } else { "" },
                listener.local_addr()?
            );
            accept(listener, monitor).await
        })
}

async fn accept(listener: TcpListener, monitor: Monitor) -> io::Result<()> {
    let monitor = Arc::new(monitor);
    let slots = Arc::new(Semaphore::new(64));
    loop {
        let permit = slots
            .clone()
            .acquire_owned()
            .await
            .map_err(io::Error::other)?;
        let (stream, _) = listener.accept().await?;
        let monitor = monitor.clone();
        tokio::spawn(async move {
            let _permit = permit;
            let integrated = monitor.resources.is_some();
            let _ = stream.set_nodelay(true);
            let adapter = Arc::new(Mutex::new(None));
            let service = hyper::service::service_fn(move |req| {
                let monitor = monitor.clone();
                let adapter = adapter.clone();
                async move { monitor.respond(req, &adapter).await }
            });
            let mut http = hyper::server::conn::http1::Builder::new();
            http.keep_alive(integrated).max_buf_size(32 << 10);
            if integrated {
                http.timer(hyper_util::rt::TokioTimer::new())
                    .header_read_timeout(Duration::from_secs(3));
                let _ = http.serve_connection(TokioIo::new(stream), service).await;
            } else {
                let _ = timeout(
                    Duration::from_secs(45),
                    http.serve_connection(TokioIo::new(stream), service),
                )
                .await;
            }
        });
    }
}

pub mod embedded;
mod reads;
