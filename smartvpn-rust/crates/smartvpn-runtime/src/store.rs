use crate::{node, token};
use anyhow::{ensure, Context, Result};
use rusqlite::{params, Connection};
use serde_json::{json, Value};
use std::{
    path::{Path, PathBuf},
    time::{Duration, Instant},
};
use url::Url;

pub struct Store {
    pub db: Connection,
    pub dir: PathBuf,
    pub direct: crate::direct::Direct,
    pub sample: Option<(Instant, u64, u64)>,
}
fn defaults() -> Value {
    json!({"subscriptionUrl":"","connectionMode":if cfg!(target_os="android"){"vpn"}else{"system-proxy"},"domesticWeb":false,"webRTCProtection":false,"dnsProtection":false,"importPolicy":false,"gameBypass":[],"socksPort":0,"mihomoPath":"","wintunPath":""})
}
pub(crate) fn existing_connection(dir: &Path, flags: rusqlite::OpenFlags) -> Result<Connection> {
    let db = Connection::open_with_flags(dir.join("smartvpn.db"), flags)?;
    db.busy_timeout(Duration::from_secs(5))?;
    Ok(db)
}

impl Store {
    pub fn open(dir: &Path) -> Result<Self> {
        std::fs::create_dir_all(dir)?;
        let db = Connection::open(dir.join("smartvpn.db"))?;
        let store = Self::init(db, dir)?;
        // Reconcile the exported file if publication was interrupted before SQL commit.
        let provider = dir.join("providers/subscription.yaml");
        let active = store.active_catalog()?;
        if !active.proxies.is_empty() {
            std::fs::create_dir_all(provider.parent().unwrap())?;
            let tmp = provider.with_extension("tmp");
            std::fs::write(&tmp, serde_json::to_vec(&active)?)?;
            std::fs::rename(tmp, provider)?;
        }
        Ok(store)
    }
    pub fn init(mut db: Connection, dir: &Path) -> Result<Self> {
        db.busy_timeout(Duration::from_secs(5))?;
        db.pragma_update(None, "journal_mode", "WAL")?;
        let tx = db.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
        tx.execute_batch("CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS subscriptions(id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT UNIQUE NOT NULL,enabled INTEGER NOT NULL,prefix TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS manual_nodes(name TEXT PRIMARY KEY,uri TEXT NOT NULL,config TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS selection(id INTEGER PRIMARY KEY CHECK(id=1),name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS probe_results(name TEXT PRIMARY KEY,latency_ms INTEGER,error TEXT,timestamp INTEGER NOT NULL);")?;
        if !tx.query_row(
            "SELECT EXISTS(SELECT 1 FROM pragma_table_info('manual_nodes') WHERE name='added_at')",
            [],
            |row| row.get::<_, bool>(0),
        )? {
            tx.execute("ALTER TABLE manual_nodes ADD COLUMN added_at INTEGER", [])?;
        }
        if !tx.query_row(
            "SELECT EXISTS(SELECT 1 FROM pragma_table_info('probe_results') WHERE name='report')",
            [],
            |r| r.get::<_, bool>(0),
        )? {
            tx.execute("ALTER TABLE probe_results ADD COLUMN report TEXT", [])?;
        }
        tx.execute_batch(crate::history::SCHEMA)?;
        tx.execute_batch(crate::monitor::SCHEMA)?;
        tx.execute_batch("CREATE TABLE IF NOT EXISTS catalogs(id TEXT PRIMARY KEY,doc TEXT NOT NULL,updated INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS quotas(id TEXT PRIMARY KEY,doc TEXT NOT NULL,updated INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS active_catalog(singleton INTEGER PRIMARY KEY CHECK(singleton=1),doc TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS chain_profiles(name TEXT PRIMARY KEY,hops TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS chain(singleton INTEGER PRIMARY KEY CHECK(singleton=1),doc TEXT NOT NULL);")?;
        for (k, v) in defaults().as_object().unwrap() {
            tx.execute(
                "INSERT OR IGNORE INTO settings VALUES(?,?)",
                params![k, v.to_string()],
            )?;
        }
        #[cfg(target_os = "android")]
        tx.execute("UPDATE settings SET value=json_quote('vpn') WHERE key='connectionMode' AND value<>json_quote('vpn')", [])?;
        tx.commit()?;
        Ok(Self::from_connection(db, dir))
    }

    pub(crate) fn background(dir: &Path) -> Result<Self> {
        let db = existing_connection(dir, rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE)?;
        Ok(Self::from_connection(db, dir))
    }
    fn from_connection(db: Connection, dir: &Path) -> Self {
        Self {
            db,
            dir: dir.to_owned(),
            direct: Default::default(),
            sample: None,
        }
    }
    fn values(&self, sql: &str) -> Result<Vec<Value>> {
        let mut q = self.db.prepare(sql)?;
        let rows = q.query_map([], |r| r.get::<_, String>(0))?;
        rows.map(|r| Ok(serde_json::from_str(&r?)?)).collect()
    }
    pub fn settings(&self) -> Result<Value> {
        let s: String = self.db.query_row(
            "SELECT json_group_object(key,json(value)) FROM settings",
            [],
            |r| r.get(0),
        )?;
        Ok(serde_json::from_str(&s)?)
    }
    pub fn update(&mut self, data: &Value) -> Result<Value> {
        let schema = defaults();
        let tx = self
            .db
            .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
        for (k, v) in data.as_object().context("expected settings object")? {
            ensure!(
                schema.get(k).is_some()
                    && std::mem::discriminant(&schema[k]) == std::mem::discriminant(v),
                "invalid setting {k}"
            );
            match k.as_str() {
                "connectionMode" => ensure!(
                    if cfg!(target_os = "android") {
                        v == "vpn"
                    } else {
                        matches!(v.as_str(), Some("system-proxy" | "tun"))
                    },
                    "invalid connection mode"
                ),
                "socksPort" => ensure!(
                    v.as_u64()
                        .is_some_and(|p| p == 0 || (1024..=65535).contains(&p)),
                    "invalid port"
                ),
                "gameBypass" => ensure!(
                    v.as_array().unwrap().iter().all(Value::is_string),
                    "invalid game list"
                ),
                "subscriptionUrl" if v != "" && v != "__merged__" => {
                    let label = crate::subscriptions::source_label(v.as_str().unwrap())?;
                    tx.execute(
                        "INSERT INTO subscriptions VALUES(?,?,?,1,'') ON CONFLICT(url) DO NOTHING",
                        params![token()?, label, v.as_str()],
                    )?;
                }
                _ => (),
            }
            tx.execute(
                "UPDATE settings SET value=? WHERE key=?",
                params![v.to_string(), k],
            )?;
        }
        tx.commit()?;
        self.settings()
    }
    pub fn configs(&self) -> Result<Vec<Value>> {
        let mut nodes: Vec<_> = self
            .values("SELECT config FROM manual_nodes ORDER BY rowid")?
            .into_iter()
            .filter_map(|n| crate::subscriptions::validate_node(n).ok())
            .collect();
        nodes.extend(self.active_catalog()?.proxies);
        let mut names = std::collections::HashSet::new();
        ensure!(
            nodes
                .iter()
                .all(|n| names.insert(n["name"].as_str().unwrap())),
            "duplicate node name in active profile"
        );
        Ok(nodes)
    }
    pub fn chain(&self) -> Result<Value> {
        let raw: Option<String> = self
            .db
            .query_row("SELECT (SELECT doc FROM chain)", [], |r| r.get(0))?;
        match raw {
            Some(s) => Ok(serde_json::from_str(&s)?),
            None => Ok(json!({"enabled":false,"hops":[]})),
        }
    }
    fn validate_hops(&self, hops: &[String]) -> Result<()> {
        ensure!((2..=8).contains(&hops.len()), "expected 2 to 8 hops");
        let configs = self.configs()?;
        let mut seen = std::collections::HashSet::new();
        for name in hops {
            ensure!(
                seen.insert(name) && configs.iter().any(|n| n["name"] == *name),
                "unknown or repeated hop"
            )
        }
        Ok(())
    }
    pub fn save_profile(&mut self, doc: &Value) -> Result<Value> {
        #[derive(serde::Deserialize)]
        struct Profile {
            name: String,
            original: String,
            hops: Vec<String>,
        }
        let p: Profile = serde_json::from_value(doc.clone())?;
        ensure!(!p.name.trim().is_empty(), "empty chain name");
        self.validate_hops(&p.hops)?;
        let chain = self.chain()?;
        let tx = self
            .db
            .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
        if !p.original.is_empty() {
            ensure!(
                tx.execute("DELETE FROM chain_profiles WHERE name=?", [&p.original])? == 1,
                "unknown chain profile"
            );
            if chain["enabled"] == true && chain["name"] == p.original {
                tx.execute(
                    "UPDATE chain SET doc=?",
                    [json!({"name":p.name,"enabled":true,"hops":p.hops}).to_string()],
                )?;
            }
        }
        tx.execute(
            "INSERT INTO chain_profiles VALUES(?,?)",
            params![p.name, serde_json::to_string(&p.hops)?],
        )?;
        tx.commit()?;
        Ok(json!({"ok":true}))
    }
    pub fn delete_profile(&mut self, name: &str) -> Result<()> {
        let tx = self
            .db
            .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
        ensure!(
            tx.execute("DELETE FROM chain_profiles WHERE name=?", [name])? == 1,
            "unknown chain profile"
        );
        tx.execute(
            "DELETE FROM chain WHERE json_extract(doc,'$.name')=?",
            [name],
        )?;
        tx.commit()?;
        Ok(())
    }
    pub fn set_chain(&self, doc: &Value) -> Result<Value> {
        #[derive(serde::Deserialize)]
        struct Chain {
            hops: Vec<String>,
            enabled: Option<bool>,
            name: Option<String>,
        }
        let c: Chain = serde_json::from_value(doc.clone())?;
        let active = c.enabled.unwrap_or(true);
        if active || !c.hops.is_empty() {
            self.validate_hops(&c.hops)?;
        }
        let result = json!({"enabled":active,"hops":c.hops,"name":c.name});
        self.db.execute(
            "INSERT OR REPLACE INTO chain VALUES(1,?)",
            [result.to_string()],
        )?;
        Ok(result)
    }
    pub fn selected(&self) -> Result<Option<String>> {
        Ok(self
            .db
            .query_row("SELECT (SELECT name FROM selection)", [], |r| r.get(0))?)
    }
    pub fn select(&self, name: &str) -> Result<()> {
        self.db
            .execute("INSERT OR REPLACE INTO selection VALUES(1,?)", [name])?;
        Ok(())
    }
    pub fn nodes(&self) -> Result<Value> {
        let chain = self.chain()?;
        let selected = if chain["enabled"] == true {
            Some(
                chain["hops"]
                    .as_array()
                    .and_then(|hops| hops.last())
                    .and_then(Value::as_str)
                    .context("enabled chain has no exit")?
                    .to_owned(),
            )
        } else {
            self.selected()?
        };
        let manual = self.values("SELECT json_quote(name) FROM manual_nodes")?;
        let profiles: String = self.db.query_row(
            "SELECT json_group_object(name,json(hops)) FROM chain_profiles",
            [],
            |r| r.get(0),
        )?;
        let active_label:Option<String>=self.db.query_row("SELECT (SELECT name FROM subscriptions WHERE url=(SELECT json_extract(value,'$') FROM settings WHERE key='subscriptionUrl'))",[],|r|r.get(0))?;
        let nodes=self.configs()?.into_iter().map(|n|json!({"name":n["name"],"type":n["type"],"network":n["network"],"tls":n["tls"],"sourceLabel":if manual.contains(&n["name"]){Some("手动节点")}else{n["smartvpn-source"].as_str().or(active_label.as_deref())},"manual":manual.contains(&n["name"]),"selected":n["name"].as_str()==selected.as_deref()})).collect::<Vec<_>>();
        Ok(
            json!({"nodes":nodes,"selectedNode":selected,"selectionMode":"manual","chain":chain,"chains":serde_json::from_str::<Value>(&profiles)?}),
        )
    }
    pub fn manual_nodes(&self) -> Result<Vec<Value>> {
        self.values("SELECT json_object('name',name,'type',json_extract(config,'$.type'),'server',json_extract(config,'$.server'),'port',json_extract(config,'$.port'),'addedAt',added_at) FROM manual_nodes ORDER BY rowid")
    }
    pub fn add_node(&self, data: &Value) -> Result<Value> {
        let uri = data["uri"].as_str().context("missing uri")?;
        let n = node::parse(
            uri,
            data.get("name")
                .map(|v| v.as_str().context("invalid name"))
                .transpose()?,
        )?;
        let n = crate::subscriptions::validate_node(n)?;
        let preview = data
            .get("preview")
            .map(|v| v.as_bool().context("invalid preview"))
            .transpose()?
            .unwrap_or(false);
        if !preview {
            ensure!(
                !self
                    .active_catalog()?
                    .proxies
                    .iter()
                    .any(|node| node["name"] == n["name"]),
                "name already used by subscription"
            );
            self.db.execute(
                "INSERT INTO manual_nodes(name,uri,config,added_at) VALUES(?,?,?,unixepoch())",
                params![n["name"].as_str(), uri, n.to_string()],
            )?;
        }
        Ok(
            json!({"node":{"name":n["name"],"port":n["port"],"server":n["server"],"type":n["type"]}}),
        )
    }
    pub fn remove_node(&mut self, name: &str) -> Result<()> {
        let tx = self
            .db
            .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
        ensure!(
            tx.execute("DELETE FROM manual_nodes WHERE name=?", [name])? == 1,
            "unknown manual node"
        );
        tx.execute("DELETE FROM selection WHERE name=?", [name])?;
        tx.execute("DELETE FROM probe_results WHERE name=?1 OR EXISTS(SELECT 1 FROM json_each(report,'$.hops') WHERE value=?1)", [name])?;
        tx.commit()?;
        Ok(())
    }
    pub fn subscriptions(&self) -> Result<Vec<Value>> {
        self.values("SELECT json_object('id',id,'name',name,'label',name,'prefix',prefix,'url',url,'userInfo',json((SELECT doc FROM quotas WHERE quotas.id=subscriptions.id)),'quotaUpdatedAt',(SELECT updated FROM quotas WHERE quotas.id=subscriptions.id),'nodeCount',(SELECT json_array_length(doc,'$.proxies') FROM catalogs WHERE catalogs.id=subscriptions.id),'enabled',json(CASE enabled WHEN 1 THEN 'true' ELSE 'false' END)) FROM subscriptions ORDER BY rowid")
    }
    pub fn add_sub(&self, data: &Value) -> Result<Value> {
        #[derive(serde::Deserialize)]
        struct Sub {
            url: String,
            #[serde(alias = "label")]
            name: String,
            enabled: Option<bool>,
            #[serde(default)]
            prefix: String,
        }
        let sub: Sub = serde_json::from_value(data.clone())?;
        crate::subscriptions::source_label(&sub.url)?;
        let id = token()?;
        self.db.execute(
            "INSERT INTO subscriptions VALUES(?,?,?,?,?)",
            params![
                id,
                sub.name,
                sub.url,
                sub.enabled.unwrap_or(true),
                sub.prefix
            ],
        )?;
        Ok(json!({"id":id}))
    }
    pub fn probes(&self) -> Result<Vec<Value>> {
        self.values("WITH rows AS (
            SELECT CASE WHEN report IS NULL THEN json_object('name',name,'hops',json_array(name),'delay',latency_ms,'latencyMs',latency_ms,'error',error,'timestamp',timestamp,'measured',timestamp*1000) ELSE report END AS doc,
                   report IS NOT NULL AS detailed FROM probe_results
        ), ranked AS (
            SELECT doc,row_number() OVER(PARTITION BY json_extract(doc,'$.hops') ORDER BY detailed DESC,json_extract(doc,'$.measured') DESC) AS priority FROM rows
        ) SELECT doc FROM ranked WHERE priority=1 ORDER BY json_extract(doc,'$.hops')")
    }
}
pub(crate) fn install_country_db(dir: &Path) -> Result<()> {
    use std::io::{ErrorKind, Write};
    const COUNTRY: &[u8] = include_bytes!("../../../vendor/Country.mmdb");
    let path = dir.join("Country.mmdb");
    match std::fs::read(&path) {
        Ok(bytes) if bytes == COUNTRY => return Ok(()),
        Ok(_) => {}
        Err(e) if e.kind() == ErrorKind::NotFound => {}
        Err(e) => return Err(e).context("reading kernel country database"),
    }
    let mut tmp = tempfile::NamedTempFile::new_in(dir)?;
    tmp.write_all(COUNTRY)?;
    tmp.persist(path)
        .context("installing kernel country database")?;
    Ok(())
}

pub fn web_url(value: &str) -> Result<Url> {
    let u = Url::parse(value)?;
    ensure!(
        matches!(u.scheme(), "http" | "https") && u.has_host(),
        "expected HTTP(S) URL"
    );
    Ok(u)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn deleting_active_profile_releases_selection() {
        let mut s =
            Store::init(Connection::open_in_memory().unwrap(), Path::new(":memory:")).unwrap();
        for name in ["入口 [A]", "出口 [B]"] {
            s.add_node(&json!({"name":name,"uri":"socks5://localhost:12345"}))
                .unwrap();
        }
        let profile = json!({"original":"","name":"测试","hops":["入口 [A]","出口 [B]"]});
        s.save_profile(&profile).unwrap();
        s.select("入口 [A]").unwrap();
        s.set_chain(&profile).unwrap();
        let nodes = s.nodes().unwrap();
        assert_eq!(nodes["selectedNode"], "出口 [B]");
        assert!(nodes["nodes"]
            .as_array()
            .unwrap()
            .iter()
            .all(|n| n["selected"] == (n["name"] == "出口 [B]")));
        s.delete_profile("测试").unwrap();
        assert_eq!(s.chain().unwrap()["enabled"], false);
        assert_eq!(s.nodes().unwrap()["selectedNode"], "入口 [A]");
        assert_eq!(s.nodes().unwrap()["chains"], json!({}));
        assert!(s.delete_profile("测试").is_err());
    }
    #[test]
    fn invalid_settings_patch_is_atomic() {
        let mut store =
            Store::init(Connection::open_in_memory().unwrap(), Path::new(":memory:")).unwrap();
        let initial = store.settings().unwrap();
        assert!(store
            .update(&json!({"domesticWeb":true,"socksPort":70000}))
            .is_err());
        assert_eq!(initial, store.settings().unwrap());
        let timeout: u64 = store
            .db
            .query_row("PRAGMA busy_timeout", [], |r| r.get(0))
            .unwrap();
        assert_eq!(timeout, 5000);
    }
}
