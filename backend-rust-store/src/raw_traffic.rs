//! Transactional traffic persistence. Callers supply Go-compatible SHA256 keys,
//! normalized metadata and calendar bucket/timezone strings; no clock or kernel access.
use crate::raw::RawText;
use crate::{Error, Result, Store};
use rusqlite::{params, OptionalExtension};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RawTrafficSample {
    /// False skips connections with an empty original ID, matching Go.
    pub has_id: bool,
    pub connection_key: RawText,
    pub app_key: RawText,
    pub app: RawText,
    pub target: RawText,
    pub network: RawText,
    pub chain: RawText,
    pub upload: i64,
    pub download: i64,
}
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RawTrafficHistoryRow {
    pub id: RawText,
    pub app: RawText,
    pub app_key: RawText,
    pub target: RawText,
    pub network: RawText,
    pub chain: RawText,
    pub upload: i64,
    pub download: i64,
    pub connections: i64,
    pub first_seen: i64,
    pub last_seen: i64,
}
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RawTrafficHistoryReport {
    pub period: RawText,
    pub date: RawText,
    pub group: RawText,
    pub timezone: RawText,
    pub upload_total: i64,
    pub download_total: i64,
    pub connection_count: i64,
    pub row_count: i64,
    pub offset: i64,
    pub limit: i64,
    pub has_more: bool,
    pub rows: Vec<RawTrafficHistoryRow>,
    pub sampling_error: RawText,
}
/// Mirrors signed Go int64 arithmetic, including wrap for externally malformed baselines.
pub fn counter_delta_raw(current: i64, previous: i64) -> i64 {
    if current < 0 {
        0
    } else if current < previous {
        current
    } else {
        current.wrapping_sub(previous)
    }
}
/// Preserves Go's partially populated report even when a transaction/query fails.
#[derive(Debug)]
pub struct RawTrafficHistoryOutcome {
    pub report: RawTrafficHistoryReport,
    pub error: Option<Error>,
}
impl Store {
    /// Atomically writes increments and cumulative checkpoints. Same snapshot retries
    /// contribute zero bytes. An empty snapshot still clears other sessions' baselines.
    pub fn record_traffic_raw(
        &self,
        session: &RawText,
        samples: &[RawTrafficSample],
        bucket: &RawText,
        now: i64,
    ) -> Result<()> {
        self.with(|c| {
            let tx = c.transaction()?;
            tx.execute("DELETE FROM traffic_checkpoints WHERE session <> ?", [session])?;
            for sample in samples.iter().filter(|s| s.has_id) {
                let (previous_up, previous_down) = tx.query_row(
                    "SELECT upload,download FROM traffic_checkpoints WHERE connection_key=?",
                    [&sample.connection_key], |r| Ok((r.get::<_,i64>(0)?,r.get::<_,i64>(1)?))
                ).optional()?.unwrap_or((0,0));
                let up=counter_delta_raw(sample.upload,previous_up);
                let down=counter_delta_raw(sample.download,previous_down);
                tx.execute("INSERT INTO traffic_hours VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(bucket,connection_key) DO UPDATE SET upload=traffic_hours.upload+excluded.upload,download=traffic_hours.download+excluded.download,last_seen=MAX(traffic_hours.last_seen,excluded.last_seen),app_key=excluded.app_key,app=excluded.app,target=excluded.target,chain=excluded.chain",
                    params![bucket,sample.connection_key,sample.app_key,sample.app,sample.target,sample.network,sample.chain,up,down,now,now])?;
                tx.execute("INSERT INTO traffic_checkpoints VALUES(?,?,?,?) ON CONFLICT(connection_key) DO UPDATE SET upload=excluded.upload,download=excluded.download",params![sample.connection_key,session,sample.upload.max(0),sample.download.max(0)])?;
            }
            tx.commit()?; Ok(())
        })
    }
    /// Storage-level semantics match Go, including SQLite LIKE and LIMIT/OFFSET.
    /// HTTP validation (date format, group, offset >=0, limit 1..200) remains upstream.
    pub fn read_traffic_history_partial_raw(
        &self,
        mut report: RawTrafficHistoryReport,
        app_key: &RawText,
    ) -> RawTrafficHistoryOutcome {
        report.rows.clear();
        let result = self.with(|c| {
            let tx=c.transaction()?;
            let group=if report.group.0==b"connection" {"connection_key"} else {"app_key"};
            let condition=" WHERE bucket LIKE ?1 AND (?2 = '' OR app_key = ?2)";
            let mut date=report.date.clone();date.0.push(b'%');
            tx.query_row(
                &format!("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0),COUNT(DISTINCT connection_key),COUNT(DISTINCT {group}) FROM traffic_hours{condition}"),
                params![date,app_key], |r| {report.upload_total=r.get(0)?;report.download_total=r.get(1)?;report.connection_count=r.get(2)?;report.row_count=r.get(3)?;Ok(())})?;
            {
                let mut stmt=tx.prepare(&format!("SELECT {group},MAX(app),MAX(app_key),MAX(target),MAX(network),MAX(chain),SUM(upload),SUM(download),COUNT(DISTINCT connection_key),MIN(first_seen),MAX(last_seen) FROM traffic_hours{condition} GROUP BY {group} ORDER BY SUM(upload)+SUM(download) DESC,{group} LIMIT ?3 OFFSET ?4"))?;
                let rows=stmt.query_map(params![date,app_key,report.limit,report.offset], |r| Ok(RawTrafficHistoryRow {
                    id:r.get(0)?,app:r.get(1)?,app_key:r.get(2)?,target:r.get(3)?,network:r.get(4)?,chain:r.get(5)?,upload:r.get(6)?,download:r.get(7)?,connections:r.get(8)?,first_seen:r.get(9)?,last_seen:r.get(10)?,
                }))?;
                for row in rows {
                    let mut row=row?;
                    if report.group.0==b"app" {row.target.clear();row.network.clear();row.chain.clear();}
                    report.rows.push(row);
                }
            }
            report.has_more=report.offset.wrapping_add(report.rows.len() as i64)<report.row_count;
            tx.commit()?; Ok(())
        });
        RawTrafficHistoryOutcome {
            report,
            error: result.err(),
        }
    }
    /// Compatibility convenience wrapper; use partial API at the Go/FFI boundary.
    pub fn read_traffic_history_raw(
        &self,
        report: RawTrafficHistoryReport,
        app_key: &RawText,
    ) -> Result<RawTrafficHistoryReport> {
        let outcome = self.read_traffic_history_partial_raw(report, app_key);
        match outcome.error {
            Some(e) => Err(e),
            None => Ok(outcome.report),
        }
    }
}
