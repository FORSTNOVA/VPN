use super::{
    checks,
    store::{web_url, Store},
    Controller,
};
use anyhow::{ensure, Context, Result};
use rusqlite::params;
use serde_json::{json, Value};

pub const SCHEMA:&str="CREATE TABLE IF NOT EXISTS preferences(key TEXT PRIMARY KEY,doc TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY,doc TEXT NOT NULL);
INSERT OR IGNORE INTO preferences VALUES('health','{\"connectTimeoutMs\":3000,\"patrolIntervalSec\":60,\"failureThreshold\":2,\"cooldownSec\":60,\"minDwellSec\":30,\"recoverySuccesses\":3}'),
('paths','{\"enabled\":false,\"intervalSec\":120,\"targets\":[{\"label\":\"YouTube\",\"url\":\"https://www.youtube.com/generate_204\",\"enabled\":true,\"builtin\":true}]}'),
('regions','{}'),('lockedRegion','\"\"'),('pathStatus','{}'),('mergeHours','0'),('mergeAt','0');";
impl Store {
    pub fn pref(&self, key: &str) -> Result<Value> {
        let s: String =
            self.db
                .query_row("SELECT doc FROM preferences WHERE key=?", [key], |r| {
                    r.get(0)
                })?;
        Ok(serde_json::from_str(&s)?)
    }
    pub fn set_pref(&self, key: &str, v: &Value) -> Result<()> {
        ensure!(
            self.db.execute(
                "UPDATE preferences SET doc=? WHERE key=?",
                params![v.to_string(), key]
            )? == 1,
            "unknown preference"
        );
        Ok(())
    }
    pub fn path_report(&self) -> Result<Value> {
        Ok(
            json!({"settings":self.pref("paths")?,"status":self.pref("pathStatus")?,"limits":{"minIntervalSec":30,"maxIntervalSec":3600,"maxTargets":20}}),
        )
    }
    pub fn save_paths(&self, mut v: Value) -> Result<()> {
        ensure!(
            v["enabled"].is_boolean()
                && v["intervalSec"]
                    .as_u64()
                    .is_some_and(|n| (30..=3600).contains(&n)),
            "invalid path monitor settings"
        );
        let targets = v["targets"]
            .as_array_mut()
            .context("expected path targets")?;
        ensure!(targets.len() <= 20, "too many path targets");
        for t in targets {
            let url = t["url"].as_str().context("missing target URL")?;
            web_url(url)?;
            ensure!(
                t["enabled"].is_boolean() && t["label"].is_string(),
                "invalid path target"
            );
            t["builtin"] = json!(url == checks::TARGET);
        }
        self.set_pref("paths", &v)
    }
    pub fn run_paths(&self, c: &Controller, port: u16) -> Result<Value> {
        let agent = checks::through_mixed(port)?;
        let mut results = Vec::new();
        let mut failures = Vec::new();
        for t in self.pref("paths")?["targets"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|t| t["enabled"] == true)
        {
            let site = checks::site(
                &agent,
                t["label"].as_str().unwrap(),
                t["url"].as_str().unwrap(),
            );
            if site["state"] != "ok" {
                failures.push(t["label"].clone());
            }
            results.push(site);
        }
        self.set_pref("pathStatus",&json!({"at":chrono::Utc::now().to_rfc3339(),"activeNode":c.active_leaf()?,"results":results,"failures":failures,"running":false}))?;
        self.path_report()
    }
    pub fn record_switch(&self, from: &str, to: &str, trigger: &str) -> Result<()> {
        self.db.execute("INSERT INTO events(doc) VALUES(?)",[json!({"at":chrono::Utc::now().to_rfc3339(),"fromNode":from,"toNode":to,"trigger":trigger}).to_string()])?;
        Ok(())
    }
    pub fn events(&self, limit: u32) -> Result<Vec<Value>> {
        let mut q = self
            .db
            .prepare("SELECT doc FROM events ORDER BY id DESC LIMIT ?")?;
        let rows = q.query_map([limit], |r| r.get::<_, String>(0))?;
        rows.map(|r| Ok(serde_json::from_str(&r?)?)).collect()
    }
}
