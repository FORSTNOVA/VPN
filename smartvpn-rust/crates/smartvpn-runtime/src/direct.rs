use anyhow::{ensure, Context, Result};
use chrono::{DateTime, Utc};
use serde_json::{json, Value};
use std::{
    fs,
    io::{ErrorKind, Write},
    path::{Path, PathBuf},
    thread::{self, JoinHandle},
    time::{Duration, SystemTime},
};

const MIRRORS:&str="https://cdn.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/\nhttps://testingcf.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/";
const TTL: Duration = Duration::from_secs(7 * 86400);

fn download(dir: &Path, mirrors: &str) -> Result<()> {
    let client = crate::http::download_agent();
    let mut errors = Vec::new();
    for mirror in mirrors.lines() {
        let attempt = (|| -> Result<()> {
            let base = crate::store::web_url(mirror)?;
            let mut tmp = tempfile::NamedTempFile::new_in(dir)?;
            for (file, v4) in [("china.txt", true), ("china6.txt", false)] {
                let url = base.join(file)?;
                let response = client
                    .get(url.as_str())
                    .call()
                    .map_err(crate::http::error)?;
                let bytes = crate::http::body(response)?;
                let text = std::str::from_utf8(&bytes)?;
                ensure!(!text.is_empty(), "empty CIDR list");
                for line in text.lines() {
                    let cidr: ipnet::IpNet = line.trim().parse().context("invalid CIDR")?;
                    ensure!(cidr.addr().is_ipv4() == v4, "wrong CIDR family");
                    writeln!(tmp, "{cidr}")?;
                }
            }
            tmp.as_file().sync_all()?;
            tmp.persist(dir.join("china-ip.txt"))?;
            Ok(())
        })();
        match attempt {
            Ok(()) => return Ok(()),
            Err(e) => errors.push(format!("{e:#}")),
        }
    }
    anyhow::bail!("CIDR mirrors failed: {}", errors.join("; "))
}

#[derive(Default)]
pub struct Direct {
    worker: Option<JoinHandle<Result<()>>>,
    error: String,
    pub mounted: Option<SystemTime>,
}
impl Direct {
    fn settle(&mut self) -> Result<()> {
        if self.worker.as_ref().is_some_and(|w| w.is_finished()) {
            let result = self
                .worker
                .take()
                .unwrap()
                .join()
                .map_err(|_| anyhow::anyhow!("CIDR refresh thread panicked"))?;
            self.error = match result {
                Ok(()) => String::new(),
                Err(e) => format!("{e:#}"),
            };
        }
        Ok(())
    }
    pub fn refresh(&mut self, dir: &Path, force: bool) -> Result<()> {
        self.settle()?;
        let providers = dir.join("providers");
        fs::create_dir_all(&providers)?;
        if self.worker.is_some() {
            return Ok(());
        }
        if !force {
            match fs::metadata(providers.join("china-ip.txt")) {
                Ok(m) if SystemTime::now().duration_since(m.modified()?)? < TTL => return Ok(()),
                Ok(_) => (),
                Err(e) if e.kind() == ErrorKind::NotFound => (),
                Err(e) => return Err(e.into()),
            }
        }
        self.error.clear();
        let config = dir.join("china-mirrors.txt");
        self.worker = Some(thread::spawn(move || {
            download(&providers, &mirrors(config)?)
        }));
        Ok(())
    }
    pub fn state(&mut self, dir: &Path, connected: bool) -> Result<Value> {
        self.settle()?;
        let path = dir.join("providers/china-ip.txt");
        let mut state = json!({"available":false,"entries":0,"updatedAt":"","source":"","active":false,
            "error":self.error,"refreshing":self.worker.is_some(),"fetching":self.worker.is_some()});
        match fs::File::open(path) {
            Ok(file) => {
                use std::io::{BufRead, BufReader};
                let timestamp = file.metadata()?.modified()?;
                state["active"] = json!(connected && self.mounted == Some(timestamp));
                state["updatedAt"] = json!(DateTime::<Utc>::from(timestamp).to_rfc3339());
                let mut count = 0;
                for line in BufReader::new(file).lines() {
                    line?
                        .parse::<ipnet::IpNet>()
                        .context("invalid cached CIDR")?;
                    count += 1;
                }
                state["available"] = json!(count > 0);
                state["entries"] = json!(count);
                state["source"] = json!("china-ip.txt");
            }
            Err(e) if e.kind() == ErrorKind::NotFound => (),
            Err(e) => return Err(e.into()),
        }
        Ok(state)
    }
}
fn mirrors(config: PathBuf) -> Result<String> {
    match std::env::var("SMARTVPN_CHINA_MIRRORS") {
        Ok(s) => Ok(s),
        Err(std::env::VarError::NotPresent) => match fs::read_to_string(config) {
            Ok(s) => Ok(s),
            Err(e) if e.kind() == ErrorKind::NotFound => Ok(MIRRORS.into()),
            Err(e) => Err(e.into()),
        },
        Err(e) => Err(e.into()),
    }
}
impl Drop for Direct {
    fn drop(&mut self) {
        if let Some(worker) = self.worker.take() {
            match worker.join() {
                Ok(Ok(())) => (),
                Ok(Err(e)) => eprintln!("CIDR refresh failed: {e:#}"),
                Err(_) => eprintln!("CIDR refresh thread panicked"),
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn publishes_both_families_and_keeps_previous_file_on_invalid_update() {
        let dir = tempfile::tempdir().unwrap();
        let http = tiny_http::Server::http("127.0.0.1:0").unwrap();
        let mirror = format!("http://{}/", http.server_addr());
        let server = thread::spawn(move || {
            for (path, text) in [
                ("/china.txt", "192.0.2.0/24"),
                ("/china6.txt", "2001:db8::/32"),
                ("/china.txt", "192.0.2.0/24"),
                ("/china6.txt", "invalid"),
            ] {
                let req = http.recv().unwrap();
                assert_eq!(req.url(), path);
                req.respond(tiny_http::Response::from_string(text)).unwrap();
            }
        });
        download(dir.path(), &mirror).unwrap();
        let original = fs::read(dir.path().join("china-ip.txt")).unwrap();
        assert!(download(dir.path(), &mirror).is_err());
        assert_eq!(original, fs::read(dir.path().join("china-ip.txt")).unwrap());
        server.join().unwrap();
    }
}
