//! Lossless SQLite TEXT API. Values remain TEXT, never BLOB or lossy UTF-8.
use crate::{Result, Store};
use rusqlite::types::{FromSql, FromSqlError, FromSqlResult, ToSql, ToSqlOutput, ValueRef};
use rusqlite::{params, OptionalExtension};
use std::collections::BTreeMap;
/// Owned Go-string-compatible bytes. Invalid UTF-8 and embedded NUL are preserved.
#[derive(
    Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, serde::Serialize, serde::Deserialize,
)]
pub struct RawText(pub Vec<u8>);
impl RawText {
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
    pub fn clear(&mut self) {
        self.0.clear();
    }
}
impl From<&str> for RawText {
    fn from(s: &str) -> Self {
        Self(s.as_bytes().to_vec())
    }
}
impl ToSql for RawText {
    fn to_sql(&self) -> rusqlite::Result<ToSqlOutput<'_>> {
        Ok(ToSqlOutput::Borrowed(ValueRef::Text(&self.0)))
    }
}
impl FromSql for RawText {
    fn column_result(v: ValueRef<'_>) -> FromSqlResult<Self> {
        match v {
            ValueRef::Text(b) | ValueRef::Blob(b) => Ok(Self(b.to_vec())),
            _ => Err(FromSqlError::InvalidType),
        }
    }
}
impl Store {
    pub fn set_value_raw(&self, key: &RawText, value: &RawText) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO settings_kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",params![key,value])?;Ok(())})
    }
    pub fn get_value_raw(&self, key: &RawText) -> Result<Option<RawText>> {
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
    pub fn upsert_nodes_raw(&self, nodes: &[RawNode], updated_at: i64) -> Result<()> {
        self.with(|c|{
        let tx=c.transaction()?;
        {let mut s=tx.prepare("INSERT INTO nodes(name,protocol,network,tls,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET protocol=excluded.protocol,network=excluded.network,tls=excluded.tls,updated_at=excluded.updated_at")?;
        for n in nodes{s.execute(params![n.name,n.protocol,n.network,n.tls,updated_at])?;}}
        tx.commit()?;Ok(())
    })
    }
    pub fn load_nodes_raw(&self) -> Result<Vec<RawNode>> {
        self.with(|c| {
            let mut s = c.prepare("SELECT name,protocol,network,tls FROM nodes ORDER BY name")?;
            let rows = s.query_map([], |r| {
                Ok(RawNode {
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
    pub fn save_probe_row_raw(&self, p: &RawProbeRow) -> Result<()> {
        self.with(|c|{
        c.execute("INSERT INTO probe_results(path,fingerprint,delay,error,measured,details) VALUES(?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint,delay=excluded.delay,error=excluded.error,measured=excluded.measured,details=excluded.details",params![p.path,p.fingerprint,p.delay,p.error,p.measured,p.details])?;Ok(())
    })
    }
    pub fn load_probe_rows_raw(&self) -> Result<Vec<RawProbeRow>> {
        self.with(|c|{
        let mut s=c.prepare("SELECT path,fingerprint,delay,error,measured,details FROM probe_results ORDER BY measured DESC")?;
        let rows=s.query_map([],|r|Ok(RawProbeRow{path:r.get(0)?,fingerprint:r.get(1)?,delay:r.get(2)?,error:r.get(3)?,measured:r.get(4)?,details:r.get(5)?}))?;
        Ok(rows.collect::<std::result::Result<Vec<_>,_>>()?)
    })
    }
    pub fn save_region_raw(&self, r: &RawRegion) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO region_verify(name,exit_ip,country,status,sources,detail,verified_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET exit_ip=excluded.exit_ip,country=excluded.country,status=excluded.status,sources=excluded.sources,detail=excluded.detail,verified_at=excluded.verified_at",params![r.name,r.exit_ip,r.country,r.status,RawText(r.sources.iter().map(|s|s.0.as_slice()).collect::<Vec<_>>().join(&[b','][..])),r.detail,r.verified_at])?;Ok(())})
    }
    pub fn load_regions_raw(&self) -> Result<BTreeMap<RawText, RawRegion>> {
        self.with(|c| {
            let mut s = c.prepare(
                "SELECT name,exit_ip,country,status,sources,detail,verified_at FROM region_verify",
            )?;
            let rows = s.query_map([], |row| {
                let sources: RawText = row.get(4)?;
                Ok(RawRegion {
                    name: row.get(0)?,
                    exit_ip: row.get(1)?,
                    country: row.get(2)?,
                    status: row.get(3)?,
                    sources: if sources.is_empty() {
                        vec![]
                    } else {
                        sources
                            .0
                            .split(|b| *b == b',')
                            .map(|b| RawText(b.to_vec()))
                            .collect()
                    },
                    detail: row.get(5)?,
                    verified_at: row.get(6)?,
                })
            })?;
            let rows = rows.collect::<std::result::Result<Vec<_>, _>>()?;
            Ok(rows.into_iter().map(|r| (r.name.clone(), r)).collect())
        })
    }
    pub fn clear_regions_raw(&self) -> Result<()> {
        self.with(|c| {
            c.execute("DELETE FROM region_verify", [])?;
            Ok(())
        })
    }
    pub fn save_health_raw(&self, h: &RawHealth) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO health(name,state,consecutive_failures,consecutive_successes,latency_ms,last_success_at,cooldown_until,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET state=excluded.state,consecutive_failures=excluded.consecutive_failures,consecutive_successes=excluded.consecutive_successes,latency_ms=excluded.latency_ms,last_success_at=excluded.last_success_at,cooldown_until=excluded.cooldown_until,updated_at=excluded.updated_at",params![h.name,h.state,h.consecutive_failures,h.consecutive_successes,h.latency_ms,h.last_success_at,h.cooldown_until,h.updated_at])?;Ok(())})
    }
    pub fn load_health_raw(&self) -> Result<BTreeMap<RawText, RawHealth>> {
        self.with(|c|{
        let mut s=c.prepare("SELECT name,state,consecutive_failures,consecutive_successes,latency_ms,last_success_at,cooldown_until,updated_at FROM health")?;
        let rows=s.query_map([],|r|Ok(RawHealth{name:r.get(0)?,state:r.get(1)?,consecutive_failures:r.get(2)?,consecutive_successes:r.get(3)?,latency_ms:r.get(4)?,last_success_at:r.get(5)?,cooldown_until:r.get(6)?,updated_at:r.get(7)?}))?;
        let rows=rows.collect::<std::result::Result<Vec<_>,_>>()?;Ok(rows.into_iter().map(|r|(r.name.clone(),r)).collect())
    })
    }
    pub fn clear_health_raw(&self) -> Result<()> {
        self.with(|c| {
            c.execute("DELETE FROM health", [])?;
            Ok(())
        })
    }
    pub fn append_switch_event_raw(&self, e: &RawSwitchEvent) -> Result<()> {
        self.with(|c|{c.execute("INSERT INTO switch_events(at,group_name,from_node,to_node,trigger,evidence) VALUES(?,?,?,?,?,?)",params![e.at,e.group,e.from_node,e.to_node,e.trigger,e.evidence])?;Ok(())})
    }
    pub fn recent_switch_events_raw(&self, limit: i64) -> Result<Vec<RawSwitchEvent>> {
        self.with(|c|{
        let mut s=c.prepare("SELECT at,group_name,from_node,to_node,trigger,evidence FROM switch_events ORDER BY at DESC,id DESC LIMIT ?")?;
        let rows=s.query_map([if limit<=0{50}else{limit}],|r|Ok(RawSwitchEvent{at:r.get(0)?,group:r.get(1)?,from_node:r.get(2)?,to_node:r.get(3)?,trigger:r.get(4)?,evidence:r.get(5)?}))?;
        Ok(rows.collect::<std::result::Result<Vec<_>,_>>()?)
    })
    }
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawNode {
    pub name: RawText,
    pub protocol: RawText,
    pub network: RawText,
    pub tls: bool,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawProbeRow {
    pub path: RawText,
    pub fingerprint: RawText,
    pub delay: i64,
    pub error: RawText,
    pub measured: i64,
    pub details: RawText,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawRegion {
    pub name: RawText,
    pub exit_ip: RawText,
    pub country: RawText,
    pub status: RawText,
    pub sources: Vec<RawText>,
    pub detail: RawText,
    pub verified_at: i64,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawHealth {
    pub name: RawText,
    pub state: RawText,
    pub consecutive_failures: i64,
    pub consecutive_successes: i64,
    pub latency_ms: i64,
    pub last_success_at: i64,
    pub cooldown_until: i64,
    pub updated_at: i64,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawSwitchEvent {
    pub at: i64,
    pub group: RawText,
    pub from_node: RawText,
    pub to_node: RawText,
    pub trigger: RawText,
    pub evidence: RawText,
}
