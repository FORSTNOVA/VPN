use crate::store::Store;
use anyhow::{ensure, Context, Result};
use chrono::{Local, NaiveDateTime};
use rusqlite::{params, Connection, OptionalExtension};
use serde_json::{json, Value};

pub const SCHEMA:&str="CREATE TABLE IF NOT EXISTS traffic_checkpoints(id TEXT PRIMARY KEY,upload INTEGER NOT NULL,download INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS traffic_history(hour TEXT NOT NULL,id TEXT NOT NULL,app TEXT NOT NULL,target TEXT NOT NULL,network TEXT NOT NULL,rule TEXT NOT NULL,chain TEXT NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,first_seen INTEGER NOT NULL,last_seen INTEGER NOT NULL,PRIMARY KEY(hour,id));";

pub fn record(db: &mut Connection, doc: &Value) -> Result<()> {
    let now = Local::now();
    let hour = now.format("%Y-%m-%dT%H").to_string();
    let tx = db.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
    let mut live = Vec::new();
    for row in doc["connections"]
        .as_array()
        .context("missing connections")?
    {
        let id = row["id"].as_str().context("missing connection id")?;
        live.push(id);
        let up = row["upload"].as_i64().context("invalid upload")?;
        let down = row["download"].as_i64().context("invalid download")?;
        ensure!(up >= 0 && down >= 0, "negative traffic counters");
        let prev: Option<(i64, i64)> = tx
            .query_row(
                "SELECT upload,download FROM traffic_checkpoints WHERE id=?",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .optional()?;
        let (u, d) = prev.unwrap_or((0, 0));
        let delta = |n, p| if n >= p { n - p } else { n };
        let m = &row["metadata"];
        let host = m["host"]
            .as_str()
            .filter(|s| !s.is_empty())
            .or(m["destinationIP"].as_str())
            .context("missing traffic destination")?;
        let target = format!(
            "{}:{}",
            host,
            m["destinationPort"].as_str().context("invalid port")?
        );
        let chain = row["chains"]
            .as_array()
            .context("missing chains")?
            .iter()
            .map(|v| v.as_str().context("invalid chain member"))
            .collect::<Result<Vec<_>>>()?
            .join(" → ");
        tx.execute("INSERT INTO traffic_history VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(hour,id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download,last_seen=excluded.last_seen",
        params![hour,id,m["process"].as_str().unwrap_or(""),target,m["network"].as_str().context("missing network")?,row["rule"].as_str().unwrap_or(""),chain,delta(up,u),delta(down,d),now.timestamp(),now.timestamp()])?;
        tx.execute(
            "INSERT OR REPLACE INTO traffic_checkpoints VALUES(?,?,?)",
            params![id, up, down],
        )?;
    }
    tx.execute(
        "DELETE FROM traffic_checkpoints WHERE id NOT IN(SELECT value FROM json_each(?))",
        [serde_json::to_string(&live)?],
    )?;
    tx.commit()?;
    Ok(())
}

pub fn query(
    s: &Store,
    period: &str,
    date: Option<&str>,
    group: &str,
    app: Option<&str>,
    offset: usize,
    limit: usize,
) -> Result<Value> {
    let (fmt, suffix) = match period {
        "hour" => ("%Y-%m-%dT%H", ":00"),
        "day" => ("%Y-%m-%d", "T00:00"),
        "month" => ("%Y-%m", "-01T00:00"),
        "year" => ("%Y", "-01-01T00:00"),
        _ => anyhow::bail!("invalid granularity"),
    };
    let date = date
        .map(str::to_owned)
        .unwrap_or_else(|| Local::now().format(fmt).to_string());
    let dt = NaiveDateTime::parse_from_str(&format!("{date}{suffix}"), "%Y-%m-%dT%H:%M")?;
    ensure!(dt.format(fmt).to_string() == date, "invalid date");
    let key = match group {
        "app" => "app",
        "connection" => "id",
        _ => anyhow::bail!("invalid group"),
    };
    ensure!((1..=1000).contains(&limit), "invalid limit");
    let filter = "WHERE substr(hour,1,length(?1))=?1 AND (?2 IS NULL OR app=?2)";
    let (up,down,count,rows):(i64,i64,i64,i64)=s.db.query_row(&format!("SELECT coalesce(sum(upload),0),coalesce(sum(download),0),count(distinct id),count(distinct {key}) FROM traffic_history {filter}"),params![date,app],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?)))?;
    let details = if group == "app" {
        "'','','',''"
    } else {
        "target,network,rule,chain"
    };
    let mut stmt=s.db.prepare(&format!("SELECT {key},app,{details},sum(upload),sum(download),count(distinct id),min(first_seen),max(last_seen) FROM traffic_history {filter} GROUP BY {key} ORDER BY sum(upload)+sum(download) DESC,{key} LIMIT ?3 OFFSET ?4"))?;
    let result=stmt.query_map(params![date,app,limit,offset],|r|Ok(json!({"id":r.get::<_,String>(0)?,"app":r.get::<_,String>(1)?,"appKey":r.get::<_,String>(1)?,"target":r.get::<_,String>(2)?,"network":r.get::<_,String>(3)?,"rule":r.get::<_,String>(4)?,"chain":r.get::<_,String>(5)?,"upload":r.get::<_,i64>(6)?,"download":r.get::<_,i64>(7)?,"connections":r.get::<_,i64>(8)?,"firstSeen":r.get::<_,i64>(9)?,"lastSeen":r.get::<_,i64>(10)?})))?.collect::<rusqlite::Result<Vec<_>>>()?;
    Ok(
        json!({"period":period,"granularity":period,"date":date,"timezone":"local","group":group,"uploadTotal":up,"downloadTotal":down,"connectionCount":count,"rowCount":rows,"offset":offset,"limit":limit,"hasMore":offset.saturating_add(result.len())<(rows as usize),"rows":result}),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn replay_reset_and_rollback() {
        let mut db = Connection::open_in_memory().unwrap();
        db.execute_batch(SCHEMA).unwrap();
        let mut doc = json!({"connections":[{"id":"a","upload":9,"download":11,"chains":["exit"],"metadata":{"destinationIP":"::1","destinationPort":"80","network":"tcp"}}]});
        record(&mut db, &doc).unwrap();
        record(&mut db, &doc).unwrap();
        doc["connections"][0]["upload"] = json!(3);
        doc["connections"][0]["download"] = json!(15);
        doc["connections"].as_array_mut().unwrap().push(json!({}));
        assert!(record(&mut db, &doc).is_err());
        doc["connections"].as_array_mut().unwrap().pop();
        record(&mut db, &doc).unwrap();
        assert_eq!(
            db.query_row(
                "SELECT sum(upload),sum(download) FROM traffic_history",
                [],
                |r| Ok((r.get::<_, i64>(0)?, r.get::<_, i64>(1)?))
            )
            .unwrap(),
            (12, 15)
        );
    }
}
