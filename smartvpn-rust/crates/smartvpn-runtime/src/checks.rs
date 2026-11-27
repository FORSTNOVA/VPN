use crate::{store::web_url, Controller, PlaneStatus};
use anyhow::{ensure, Context, Result};
use percent_encoding::{utf8_percent_encode, NON_ALPHANUMERIC};
use serde_json::{json, Value};
use std::{
    collections::HashSet,
    time::{Duration, Instant},
};

pub fn target(dir: &std::path::Path) -> Result<String> {
    endpoint(dir, "latency-target.txt", TARGET)
}
pub(crate) fn endpoint(dir: &std::path::Path, file: &str, default: &str) -> Result<String> {
    match std::fs::read_to_string(dir.join(file)) {
        Ok(s) => Ok(web_url(s.trim())?.to_string()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(default.to_owned()),
        Err(e) => Err(e.into()),
    }
}
pub(crate) const DEFAULT_PROBE_TIMEOUT_MS: u32 = 5000;
pub const TARGET: &str = "https://www.youtube.com/generate_204";
impl Controller {
    pub fn active_leaf(&self) -> Result<String> {
        let mut name = String::from("SmartVPN");
        let mut visited = HashSet::new();
        loop {
            ensure!(visited.insert(name.clone()), "controller selection cycle");
            let doc = self.json(&format!(
                "/proxies/{}",
                utf8_percent_encode(&name, NON_ALPHANUMERIC)
            ))?;
            if doc.get("all").is_none() {
                ensure!(doc["type"].is_string(), "invalid controller proxy");
                return Ok(name);
            }
            name = doc["now"]
                .as_str()
                .context("group has no selected proxy")?
                .to_owned();
        }
    }
    pub fn delay(&self, name: &str, url: &str) -> Result<(u64, u64)> {
        web_url(url)?;
        let query = url::form_urlencoded::Serializer::new(String::new())
            .append_pair("url", url)
            .append_pair("timeout", &DEFAULT_PROBE_TIMEOUT_MS.to_string())
            .finish();
        let path = format!(
            "/proxies/{}/delay?{query}",
            utf8_percent_encode(name, NON_ALPHANUMERIC)
        );
        let sample = self.json(&path)?;
        Ok((
            sample["overall"]
                .as_u64()
                .context("controller overall missing")?,
            sample["delay"]
                .as_u64()
                .context("controller delay missing")?,
        ))
    }
}
pub fn through_mixed(port: u16) -> Result<ureq::Agent> {
    ensure!(port > 0, "VPN is disconnected");
    let proxy = ureq::Proxy::new(format!("http://127.0.0.1:{port}"))?;
    Ok(ureq::AgentBuilder::new()
        .try_proxy_from_env(false)
        .proxy(proxy)
        .timeout(Duration::from_secs(6))
        .build())
}
pub fn site(agent: &ureq::Agent, name: &str, url: &str) -> Value {
    let start = Instant::now();
    match agent.get(url).call() {
        Ok(r) => {
            json!({"name":name,"state":"ok","latencyMs":start.elapsed().as_millis(),"httpCode":r.status()})
        }
        Err(e) => {
            json!({"name":name,"state":"error","detail":crate::http::error(e).to_string(),"latencyMs":start.elapsed().as_millis()})
        }
    }
}
pub fn diagnose(c: &Controller, status: PlaneStatus, dir: &std::path::Path) -> Result<Value> {
    let agent = through_mixed(status.mixed_port)?;
    let egress = (|| -> Result<Value> {
        let r = agent
            .get(&endpoint(
                dir,
                "egress-target.txt",
                "https://api.ipify.org",
            )?)
            .call()
            .map_err(crate::http::error)?;
        let addr = std::str::from_utf8(&crate::http::body(r)?)?
            .trim()
            .parse::<std::net::IpAddr>()?;
        Ok(if addr.is_ipv4() {
            json!({"ipv4":addr})
        } else {
            json!({"ipv6":addr})
        })
    })();
    Ok(
        json!({"node":c.active_leaf()?,"temporary":false,"egress":match egress{Ok(v)=>v,Err(e)=>json!({"error":e.to_string()})},
        "sites":[site(&agent,"YouTube",&target(dir)?)]}),
    )
}

const SPEED_WINDOW: Duration = Duration::from_secs(10);

pub fn download_speed(dir: &std::path::Path, port: u16, mb: u64) -> Result<Value> {
    let agent = through_mixed(port)?;
    let origin = endpoint(dir, "speed-origin.txt", "https://speed.cloudflare.com")?;
    let origin = origin.trim_end_matches('/');
    download(&agent, origin, mb, SPEED_WINDOW)
}

fn download(agent: &ureq::Agent, origin: &str, mb: u64, window: Duration) -> Result<Value> {
    use std::io::Read;
    ensure!(matches!(mb, 10 | 30 | 100), "invalid speed test size");
    let quota = mb * 1_000_000 / 2;
    let url = format!("{origin}/__down?bytes={quota}");
    let start = Instant::now();
    let deadline = start + window;
    let stream = || {
        let mut received = 0;
        let result = (|| -> Result<()> {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .context("download deadline")?;
            let mut reader = agent
                .get(&url)
                .timeout(remaining)
                .call()
                .map_err(crate::http::error)?
                .into_reader()
                .take(quota);
            let mut buf = [0; 32768];
            while Instant::now() < deadline {
                let count = reader.read(&mut buf)?;
                if count == 0 {
                    break;
                }
                received += count as u64;
            }
            Ok(())
        })();
        (received, result)
    };
    let [(a, first), (b, second)] = std::thread::scope(|scope| {
        let other = scope.spawn(stream);
        [stream(), other.join().unwrap()]
    });
    let bytes = a + b;
    if bytes == 0 {
        first.and(second).context("no download data")?;
        anyhow::bail!("empty speed test download");
    }
    let elapsed = start.elapsed();
    Ok(
        json!({"target":"Cloudflare","megaBytes":mb,"downBytes":bytes,
        "downMbps":8.0 * bytes as f64 / 1e6 / elapsed.as_secs_f64(),"downMs":elapsed.as_millis()}),
    )
}

pub fn speed(dir: &std::path::Path, port: u16, mb: u64) -> Result<Value> {
    use std::io::{self, Read};
    let origin = endpoint(dir, "speed-origin.txt", "https://speed.cloudflare.com")?;
    let origin = origin.trim_end_matches('/');
    let agent = through_mixed(port)?;
    let mut sample = download(&agent, origin, mb, SPEED_WINDOW)?;
    let up = mb * 1_000_000 / 3;
    let start = Instant::now();
    let result = agent
        .post(&format!("{origin}/__up"))
        .timeout(SPEED_WINDOW)
        .set("Content-Length", &up.to_string())
        .send(io::repeat(0).take(up));
    let up_time = start.elapsed();
    let confirmed = match result {
        Ok(_) => up,
        Err(e) => {
            sample["error"] = json!(format!("上传测速失败: {}", crate::http::error(e)));
            0
        }
    };
    sample["upBytes"] = json!(confirmed);
    sample["upMs"] = json!(up_time.as_millis());
    sample["upMbps"] = json!(8.0 * confirmed as f64 / 1e6 / up_time.as_secs_f64());
    Ok(json!({"speed":sample}))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn partial_window_counts_both_streams() -> Result<()> {
        use std::io::{BufRead, BufReader, Write};
        let listener = std::net::TcpListener::bind(("127.0.0.1", 0))?;
        let url = format!("http://{}", listener.local_addr()?);
        let server = std::thread::spawn(move || -> Result<()> {
            let mut held = Vec::new();
            for _ in 0..2 {
                let (mut socket, _) = listener.accept()?;
                let mut lines = BufReader::new(&socket).lines();
                assert!(lines
                    .next()
                    .unwrap()?
                    .starts_with("GET /__down?bytes=5000000 "));
                for line in lines {
                    if line?.is_empty() {
                        break;
                    }
                }
                socket.write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 5000000\r\n\r\n12345")?;
                held.push(socket);
            }
            std::thread::sleep(Duration::from_secs(1));
            Ok(())
        });
        let sample = download(&crate::http::agent(), &url, 10, Duration::from_millis(400))?;
        assert_eq!(sample["downBytes"], 10);
        let ms = sample["downMs"].as_u64().unwrap();
        assert!((390..950).contains(&ms), "{ms} ms");
        let bits = sample["downMbps"].as_f64().unwrap() * 1000.0 * ms as f64;
        assert!((bits - 80.0).abs() < 1.0);
        server.join().unwrap()
    }

    #[test]
    fn controller_delay_preserves_reason() {
        let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
        let c = Controller {
            port: server.server_addr().to_ip().unwrap().port(),
            secret: "fixture".into(),
        };
        let t = std::thread::spawn(move || {
            server
                .recv()
                .unwrap()
                .respond(
                    tiny_http::Response::from_string(
                        "get delay for sample failed with error: TLS handshake timed out",
                    )
                    .with_status_code(400),
                )
                .unwrap()
        });
        assert_eq!(
            c.delay("sample", TARGET).unwrap_err().to_string(),
            "controller HTTP 400: TLS handshake timed out"
        );
        t.join().unwrap();
    }
    #[test]
    fn controller_resolves_nested_group_and_encodes_leaf() {
        let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
        let c = Controller {
            port: server.server_addr().to_ip().unwrap().port(),
            secret: "fixture".into(),
        };
        let mock = std::thread::spawn(move || {
            for (prefix, reply) in [
                ("/proxies/SmartVPN", json!({"all":["inner"],"now":"inner"})),
                ("/proxies/inner", json!({"all":["A/B"],"now":"A/B"})),
                ("/proxies/A%2FB", json!({"type":"Socks5"})),
                ("/proxies/A%2FB/delay?", json!({"delay":12,"overall":38})),
            ] {
                let req = server.recv().unwrap();
                assert!(req.url().starts_with(prefix));
                if prefix.ends_with('?') {
                    let url = url::Url::parse(&format!("http://localhost{}", req.url())).unwrap();
                    assert!(url.query_pairs().any(|(k, v)| k == "url" && v == TARGET));
                    assert!(url
                        .query_pairs()
                        .any(|(k, v)| k == "timeout" && v == "5000"));
                }
                req.respond(tiny_http::Response::from_string(reply.to_string()))
                    .unwrap();
            }
        });
        let name = c.active_leaf().unwrap();
        assert_eq!(name, "A/B");
        assert_eq!(c.delay(&name, TARGET).unwrap(), (38, 12));
        mock.join().unwrap();
    }
}
