//! SQLite persistence extracted from SmartVPN's Go store. No network, process,
//! global configuration, or runtime service integration. Only explicitly opened
//! database paths are touched. Callers still own probe fingerprint validation.
use rusqlite::{params, Connection, OptionalExtension};
use serde::{Deserialize, Serialize};
use std::{collections::BTreeMap, fmt, path::Path, sync::Mutex, time::Duration};

pub const STORE_SCHEMA: &str = include_str!("schema.sql");
pub const TRAFFIC_SCHEMA: &str = include_str!("traffic_schema.sql");
pub const PROBE_TARGET: &str = "https://www.gstatic.com/generate_204";
#[derive(Debug)]
pub enum Error {
    Sql(rusqlite::Error),
    Json(serde_json::Error),
    Closed,
    Poisoned,
    InvalidDetails,
}
impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Sql(e) => write!(f, "{e}"),
            Self::Json(e) => write!(f, "{e}"),
            Self::Closed => f.write_str("store is closed"),
            Self::Poisoned => f.write_str("store lock poisoned"),
            Self::InvalidDetails => f.write_str("invalid probe details object"),
        }
    }
}
impl std::error::Error for Error {}
impl From<rusqlite::Error> for Error {
    fn from(e: rusqlite::Error) -> Self {
        Self::Sql(e)
    }
}
impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Self::Json(e)
    }
}
pub type Result<T> = std::result::Result<T, Error>;

fn migrate(tx: &rusqlite::Transaction<'_>) -> Result<()> {
    tx.execute_batch(STORE_SCHEMA)?;
    tx.execute_batch(TRAFFIC_SCHEMA)?;
    let has: i64 = tx.query_row(
        "SELECT count(*) FROM pragma_table_info('probe_results') WHERE name='details'",
        [],
        |r| r.get(0),
    )?;
    if has == 0 {
        tx.execute_batch("ALTER TABLE probe_results ADD COLUMN details TEXT NOT NULL DEFAULT ''")?;
    }
    Ok(())
}

/// One mutex-protected connection mirrors Go's MaxOpenConns(1).
pub struct Store {
    connection: Mutex<Option<Connection>>,
}
impl Store {
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let mut conn = Connection::open(path)?;
        conn.busy_timeout(Duration::from_millis(5000))?;
        conn.pragma_update(None, "journal_mode", "WAL")?;
        // Atomic, idempotent schema/details migration; existing rows stay intact.
        let tx = conn.transaction()?;
        migrate(&tx)?;
        tx.commit()?;
        Ok(Self {
            connection: Mutex::new(Some(conn)),
        })
    }
    fn with<T>(&self, f: impl FnOnce(&mut Connection) -> Result<T>) -> Result<T> {
        let mut guard = self.connection.lock().map_err(|_| Error::Poisoned)?;
        f(guard.as_mut().ok_or(Error::Closed)?)
    }
    /// Repeated close is harmless. A failed close retains the connection.
    pub fn close(&self) -> Result<()> {
        let mut guard = self.connection.lock().map_err(|_| Error::Poisoned)?;
        if let Some(conn) = guard.take() {
            if let Err((conn, error)) = conn.close() {
                *guard = Some(conn);
                return Err(error.into());
            }
        }
        Ok(())
    }
    pub fn set_value(&self, key: &str, value: &str) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO settings_kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",params![key,value])?;Ok(())})
    }
    pub fn get_value(&self, key: &str) -> Result<Option<String>> {
        self.with(|c| {
            Ok(
                c.query_row("SELECT value FROM settings_kv WHERE key=?", [key], |r| {
                    r.get(0)
                })
                .optional()?,
            )
        })
    }
    /// Timestamp supplied explicitly, in Unix seconds, for deterministic updates.
    pub fn upsert_nodes(&self, nodes: &[Node], updated_at: i64) -> Result<()> {
        self.with(|c|{
        let tx=c.transaction()?;
        {let mut s=tx.prepare("INSERT INTO nodes(name,protocol,network,tls,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET protocol=excluded.protocol,network=excluded.network,tls=excluded.tls,updated_at=excluded.updated_at")?;
        for n in nodes{s.execute(params![n.name,n.protocol,n.network,n.tls,updated_at])?;}}
        tx.commit()?;Ok(())
    })
    }
    pub fn load_nodes(&self) -> Result<Vec<Node>> {
        self.with(|c| {
            let mut s = c.prepare("SELECT name,protocol,network,tls FROM nodes ORDER BY name")?;
            let rows = s.query_map([], |r| {
                Ok(Node {
                    name: r.get(0)?,
                    protocol: r.get(1)?,
                    network: r.get(2)?,
                    tls: r.get(3)?,
                })
            })?;
            Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
        })
    }
    /// Raw API preserves Go's opaque path/fingerprint/details storage contract.
    pub fn save_probe_row(&self, p: &ProbeRow) -> Result<()> {
        self.with(|c|{
        c.execute("INSERT INTO probe_results(path,fingerprint,delay,error,measured,details) VALUES(?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint,delay=excluded.delay,error=excluded.error,measured=excluded.measured,details=excluded.details",params![p.path,p.fingerprint,p.delay,p.error,p.measured,p.details])?;Ok(())
    })
    }
    pub fn save_probe(&self, fingerprint: &str, result: &ProbeResult) -> Result<()> {
        self.save_probe_row(&ProbeRow {
            path: go_json(&result.hops)?,
            fingerprint: fingerprint.into(),
            delay: result.delay,
            error: result.error.clone(),
            measured: result.measured,
            details: go_json(result)?,
        })
    }
    pub fn load_probe_rows(&self) -> Result<Vec<ProbeRow>> {
        self.with(|c|{
        let mut s=c.prepare("SELECT path,fingerprint,delay,error,measured,details FROM probe_results ORDER BY measured DESC")?;
        let rows=s.query_map([],|r|Ok(ProbeRow{path:r.get(0)?,fingerprint:r.get(1)?,delay:r.get(2)?,error:r.get(3)?,measured:r.get(4)?,details:r.get(5)?}))?;
        Ok(rows.collect::<std::result::Result<Vec<_>,_>>()?)
    })
    }
    pub fn save_region(&self, r: &Region) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO region_verify(name,exit_ip,country,status,sources,detail,verified_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET exit_ip=excluded.exit_ip,country=excluded.country,status=excluded.status,sources=excluded.sources,detail=excluded.detail,verified_at=excluded.verified_at",params![r.name,r.exit_ip,r.country,r.status,r.sources.join(","),r.detail,r.verified_at])?;Ok(())})
    }
    pub fn load_regions(&self) -> Result<BTreeMap<String, Region>> {
        self.with(|c| {
            let mut s = c.prepare(
                "SELECT name,exit_ip,country,status,sources,detail,verified_at FROM region_verify",
            )?;
            let rows = s.query_map([], |row| {
                let sources: String = row.get(4)?;
                Ok(Region {
                    name: row.get(0)?,
                    exit_ip: row.get(1)?,
                    country: row.get(2)?,
                    status: row.get(3)?,
                    sources: if sources.is_empty() {
                        vec![]
                    } else {
                        sources.split(',').map(str::to_owned).collect()
                    },
                    detail: row.get(5)?,
                    verified_at: row.get(6)?,
                })
            })?;
            let rows = rows.collect::<std::result::Result<Vec<_>, _>>()?;
            Ok(rows.into_iter().map(|r| (r.name.clone(), r)).collect())
        })
    }
    pub fn clear_regions(&self) -> Result<()> {
        self.with(|c| {
            c.execute("DELETE FROM region_verify", [])?;
            Ok(())
        })
    }
    pub fn save_health(&self, h: &Health) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO health(name,state,consecutive_failures,consecutive_successes,latency_ms,last_success_at,cooldown_until,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET state=excluded.state,consecutive_failures=excluded.consecutive_failures,consecutive_successes=excluded.consecutive_successes,latency_ms=excluded.latency_ms,last_success_at=excluded.last_success_at,cooldown_until=excluded.cooldown_until,updated_at=excluded.updated_at",params![h.name,h.state,h.consecutive_failures,h.consecutive_successes,h.latency_ms,h.last_success_at,h.cooldown_until,h.updated_at])?;Ok(())})
    }
    pub fn load_health(&self) -> Result<BTreeMap<String, Health>> {
        self.with(|c|{
        let mut s=c.prepare("SELECT name,state,consecutive_failures,consecutive_successes,latency_ms,last_success_at,cooldown_until,updated_at FROM health")?;
        let rows=s.query_map([],|r|Ok(Health{name:r.get(0)?,state:r.get(1)?,consecutive_failures:r.get(2)?,consecutive_successes:r.get(3)?,latency_ms:r.get(4)?,last_success_at:r.get(5)?,cooldown_until:r.get(6)?,updated_at:r.get(7)?}))?;
        let rows=rows.collect::<std::result::Result<Vec<_>,_>>()?;Ok(rows.into_iter().map(|r|(r.name.clone(),r)).collect())
    })
    }
    pub fn clear_health(&self) -> Result<()> {
        self.with(|c| {
            c.execute("DELETE FROM health", [])?;
            Ok(())
        })
    }
    pub fn append_switch_event(&self, e: &SwitchEvent) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO switch_events(at,group_name,from_node,to_node,trigger,evidence) VALUES(?,?,?,?,?,?)",params![e.at,e.group,e.from_node,e.to_node,e.trigger,e.evidence])?;Ok(())})
    }
    pub fn recent_switch_events(&self, limit: i64) -> Result<Vec<SwitchEvent>> {
        self.with(|c|{
        let mut s=c.prepare("SELECT at,group_name,from_node,to_node,trigger,evidence FROM switch_events ORDER BY at DESC,id DESC LIMIT ?")?;
        let rows=s.query_map([if limit<=0{50}else{limit}],|r|Ok(SwitchEvent{at:r.get(0)?,group:r.get(1)?,from_node:r.get(2)?,to_node:r.get(3)?,trigger:r.get(4)?,evidence:r.get(5)?}))?;
        Ok(rows.collect::<std::result::Result<Vec<_>,_>>()?)
    })
    }
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Node {
    pub name: String,
    pub protocol: String,
    pub network: String,
    pub tls: bool,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProbeRow {
    pub path: String,
    pub fingerprint: String,
    pub delay: i64,
    pub error: String,
    pub measured: i64,
    pub details: String,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Region {
    pub name: String,
    pub exit_ip: String,
    pub country: String,
    pub status: String,
    pub sources: Vec<String>,
    pub detail: String,
    pub verified_at: i64,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Health {
    pub name: String,
    pub state: String,
    pub consecutive_failures: i64,
    pub consecutive_successes: i64,
    pub latency_ms: i64,
    pub last_success_at: i64,
    pub cooldown_until: i64,
    pub updated_at: i64,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SwitchEvent {
    pub at: i64,
    pub group: String,
    pub from_node: String,
    pub to_node: String,
    pub trigger: String,
    pub evidence: String,
}
fn zero(v: &i64) -> bool {
    *v == 0
}
fn no(v: &bool) -> bool {
    !*v
}
#[derive(Debug, Default, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ProbeResult {
    #[serde(skip_serializing_if = "no")]
    pub entry_attempt: bool,
    #[serde(skip_serializing_if = "zero")]
    pub diagnostics_version: i64,
    #[serde(skip_serializing_if = "String::is_empty", rename = "entryIP")]
    pub entry_ip: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub entry_family: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub failure_stage: String,
    pub target: String,
    #[serde(skip_serializing_if = "zero")]
    pub warm_delay: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub warm_error: String,
    pub hops: Option<Vec<String>>,
    pub delay: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    pub measured: i64,
}
/// Matches encoding/json's default HTML and U+2028/U+2029 escaping. Important:
/// path is the primary key, so a different spelling would create duplicate rows.
pub fn go_json<T: Serialize>(value: &T) -> Result<String> {
    Ok(serde_json::to_string(value)?
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('&', "\\u0026")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029"))
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HealthParams {
    pub connect_timeout_ms: i64,
    pub patrol_interval_sec: i64,
    pub failure_threshold: i64,
    pub cooldown_sec: i64,
    pub min_dwell_sec: i64,
    pub recovery_successes: i64,
}
impl HealthParams {
    pub fn defaults(android: bool) -> Self {
        Self {
            connect_timeout_ms: if android { 6000 } else { 3000 },
            patrol_interval_sec: 60,
            failure_threshold: if android { 3 } else { 2 },
            cooldown_sec: 60,
            min_dwell_sec: 30,
            recovery_successes: 3,
        }
    }
    pub fn normalized(self) -> Self {
        Self {
            connect_timeout_ms: self.connect_timeout_ms.clamp(1000, 15000),
            patrol_interval_sec: self.patrol_interval_sec.clamp(10, 600),
            failure_threshold: self.failure_threshold.clamp(1, 10),
            cooldown_sec: self.cooldown_sec.clamp(10, 3600),
            min_dwell_sec: self.min_dwell_sec.clamp(0, 600),
            recovery_successes: self.recovery_successes.clamp(1, 10),
        }
    }
    /// Go unmarshal overlays defaults, accepts null and case-insensitive keys,
    /// ignores unknown keys, and rejects invalid typed fields even in duplicates.
    pub fn decode(body: &str, android: bool) -> Result<Self> {
        use serde::de::{DeserializeSeed, MapAccess, Visitor};
        struct Seed(HealthParams);
        impl<'de> DeserializeSeed<'de> for Seed {
            type Value = HealthParams;
            fn deserialize<D: serde::Deserializer<'de>>(
                self,
                d: D,
            ) -> std::result::Result<Self::Value, D::Error> {
                d.deserialize_any(self)
            }
        }
        impl<'de> Visitor<'de> for Seed {
            type Value = HealthParams;
            fn expecting(&self, f: &mut fmt::Formatter) -> fmt::Result {
                f.write_str("health parameter object or null")
            }
            fn visit_unit<E: serde::de::Error>(self) -> std::result::Result<Self::Value, E> {
                Ok(self.0)
            }
            fn visit_map<M: MapAccess<'de>>(
                mut self,
                mut map: M,
            ) -> std::result::Result<Self::Value, M::Error> {
                while let Some(key) = map.next_key::<String>()? {
                    let key: String = key
                        .chars()
                        .map(|c| match c {
                            '\u{212a}' => 'k',
                            '\u{017f}' => 's',
                            _ => c.to_ascii_lowercase(),
                        })
                        .collect();
                    let field = match key.as_str() {
                        "connecttimeoutms" => Some(&mut self.0.connect_timeout_ms),
                        "patrolintervalsec" => Some(&mut self.0.patrol_interval_sec),
                        "failurethreshold" => Some(&mut self.0.failure_threshold),
                        "cooldownsec" => Some(&mut self.0.cooldown_sec),
                        "mindwellsec" => Some(&mut self.0.min_dwell_sec),
                        "recoverysuccesses" => Some(&mut self.0.recovery_successes),
                        _ => None,
                    };
                    if let Some(field) = field {
                        if let Some(value) = map.next_value::<Option<i64>>()? {
                            *field = value;
                        }
                    } else {
                        map.next_value::<serde::de::IgnoredAny>()?;
                    }
                }
                Ok(self.0)
            }
        }
        let mut decoder = serde_json::Deserializer::from_str(body);
        let value = Seed(Self::defaults(android)).deserialize(&mut decoder)?;
        decoder.end()?;
        Ok(value)
    }
}
impl Store {
    pub fn save_health_params(&self, params: &HealthParams) -> Result<()> {
        self.set_value("health_params", &go_json(params)?)
    }
    pub fn load_health_params(&self, android: bool) -> (HealthParams, Option<Error>) {
        let defaults = HealthParams::defaults(android);
        match self.get_value("health_params") {
            Ok(None) => (defaults, None),
            Err(e) => (defaults, Some(e)),
            Ok(Some(body)) => match HealthParams::decode(&body, android) {
                Ok(p) => (p.normalized(), None),
                Err(e) => (defaults, Some(e)),
            },
        }
    }
}

pub mod traffic;

pub mod raw;
pub mod raw_traffic;
pub mod wire;

pub mod ffi;

#[cfg(test)]
mod migration_tests {
    use super::*;
    use std::{fs, process, time::SystemTime};

    #[test]
    fn interrupted_upgrade_of_legacy_copy() {
        const CHILD: &str = "SMARTVPN_TEST_INTERRUPT_STORE_UPGRADE";
        if std::env::var_os(CHILD).is_some() {
            let mut conn = Connection::open("copy.db").unwrap();
            conn.pragma_update(None, "journal_mode", "WAL").unwrap();
            let tx = conn.transaction().unwrap();
            migrate(&tx).unwrap();
            tx.cache_flush().unwrap();
            process::exit(47); // Bypass transaction destructors, before COMMIT.
        }
        let nonce = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let dir = std::env::temp_dir().join(format!("smartvpn-upgrade-{}-{nonce}", process::id()));
        fs::create_dir(&dir).unwrap();
        let old = dir.join("legacy.db");
        let copy = dir.join("copy.db");
        let conn = Connection::open(&old).unwrap();
        conn.execute_batch(STORE_SCHEMA).unwrap();
        conn.execute_batch("INSERT INTO probe_results VALUES('legacy','marker',7,'',11)")
            .unwrap();
        drop(conn);
        fs::copy(&old, &copy).unwrap();
        let result = process::Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "migration_tests::interrupted_upgrade_of_legacy_copy",
            ])
            .current_dir(&dir)
            .env(CHILD, "1")
            .status()
            .unwrap();
        assert_eq!(result.code(), Some(47));
        let conn = Connection::open(&copy).unwrap();
        let partial: i64 = conn.query_row("SELECT (SELECT count(*) FROM pragma_table_info('probe_results') WHERE name='details') + (SELECT count(*) FROM sqlite_master WHERE name='traffic_hours')", [], |r| r.get(0)).unwrap();
        assert_eq!(partial, 0);
        drop(conn);
        let db = Store::open(&copy).unwrap();
        assert_eq!(db.load_probe_rows().unwrap()[0].fingerprint, "marker");
        db.close().unwrap();
        fs::remove_dir_all(dir).unwrap();
    }
}
