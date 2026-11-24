use crate::{checks, store::Store, TunnelDataPlane};
use anyhow::{ensure, Context, Result};
use serde_json::{json, Value};
use std::{
    hash::{DefaultHasher, Hash, Hasher},
    path::Path,
};

pub fn region(port: u16, dir: &Path) -> Result<Value> {
    let agent = checks::through_mixed(port)?;
    let fetch = |file, default| -> Result<String> {
        let response = agent
            .get(&checks::endpoint(dir, file, default)?)
            .call()
            .map_err(crate::http::error)?;
        Ok(String::from_utf8(crate::http::body(response)?)?)
    };
    let trace = fetch(
        "region-trace.txt",
        "https://www.cloudflare.com/cdn-cgi/trace",
    )?;
    let first = trace
        .lines()
        .find_map(|l| l.strip_prefix("loc="))
        .context("trace has no country")?;
    let second = fetch("region-country.txt", "https://ipinfo.io/country")?;
    ensure!(
        first.len() == 2 && first.bytes().all(|b| b.is_ascii_uppercase()),
        "invalid country code"
    );
    ensure!(first == second.trim(), "region sources disagree");
    Ok(json!({"country":first,"status":"verified","sources":["Cloudflare","IPinfo"]}))
}

fn fingerprint(node: &Value) -> String {
    let mut hash = DefaultHasher::new();
    node.hash(&mut hash);
    format!("{:x}", hash.finish())
}
fn fresh(row: &Value, node: &Value) -> bool {
    row["status"] == "verified"
        && row["fingerprint"] == fingerprint(node)
        && row["expires"]
            .as_i64()
            .is_some_and(|t| t > chrono::Utc::now().timestamp())
}
impl Store {
    pub fn verify_regions<P: TunnelDataPlane>(&self, plane: &P, refresh: bool) -> Result<()> {
        let previous = self.pref("regions")?;
        let mut rows = json!({});
        for mut node in self.configs()? {
            let name = node["name"].as_str().unwrap().to_owned();
            let key = fingerprint(&node);
            if !refresh && fresh(&previous[&name], &node) {
                rows[&name] = previous[&name].clone();
                continue;
            }
            node.as_object_mut().unwrap().remove("smartvpn-source");
            let mut row = match plane.region(vec![node]) {
                Ok(v) => v,
                Err(e) => {
                    json!({"status":"unreachable","detail":crate::http::redact(&e.to_string())})
                }
            };
            row["verifiedAt"] = json!(chrono::Utc::now().to_rfc3339());
            row["expires"] = json!(chrono::Utc::now().timestamp() + 86400);
            row["fingerprint"] = json!(key);
            rows[&name] = row;
        }
        self.set_pref("regions", &rows)
    }
    pub fn region_pool(&self, country: &str) -> Result<Vec<String>> {
        let rows = self.pref("regions")?;
        Ok(self
            .configs()?
            .iter()
            .filter_map(|n| {
                let name = n["name"].as_str().unwrap();
                let row = &rows[name];
                (fresh(row, n) && row["country"] == country).then(|| name.to_owned())
            })
            .collect())
    }
    pub fn region_report(&self) -> Result<Value> {
        let rows = self.pref("regions")?;
        let locked = self.pref("lockedRegion")?;
        let mut nodes = Vec::new();
        for node in self.configs()? {
            let name = node["name"].as_str().unwrap();
            let mut row = rows
                .get(name)
                .cloned()
                .unwrap_or_else(|| json!({"status":"unverified"}));
            row["name"] = json!(name);
            row["stale"] = json!(!fresh(&row, &node));
            row.as_object_mut().unwrap().remove("fingerprint");
            nodes.push(row);
        }
        let done = nodes.iter().filter(|n| n["status"] != "unverified").count();
        let verified = nodes
            .iter()
            .filter(|n| n["status"] == "verified" && n["stale"] == false)
            .count();
        let mut health = json!({});
        for probe in self.probes()? {
            if let Some(name) = probe["name"].as_str() {
                health[name] = json!({"state":if probe["error"].is_null(){"HEALTHY"}else{"UNHEALTHY"},"latencyMs":probe["latencyMs"]});
            }
        }
        Ok(
            json!({"nodes":nodes,"lockedRegion":locked,"health":health,"pool":self.region_pool(locked.as_str().unwrap())?,
            "job":{"running":false,"total":nodes.len(),"done":done,"verified":verified,"failed":done-verified}}),
        )
    }
}
