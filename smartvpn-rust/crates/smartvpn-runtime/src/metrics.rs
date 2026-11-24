use super::{
    store::{web_url, Store},
    Controller,
};
use anyhow::{anyhow, ensure, Context, Result};
use percent_encoding::{utf8_percent_encode, NON_ALPHANUMERIC};
use serde_json::{json, Value};
use std::{thread, time::Instant};
fn controller_error(code: u16, body: &[u8]) -> String {
    let raw = String::from_utf8_lossy(body);
    let reason = raw
        .split_once(" failed with error: ")
        .map_or(raw.as_ref(), |(_, s)| s);
    format!(
        "controller HTTP {code}: {}",
        crate::http::redact(reason.trim())
    )
}
impl Controller {
    pub fn json(&self, path: &str) -> Result<Value> {
        let (code, data) = self.request("GET", path, &[])?;
        ensure!(code == 200, "{}", controller_error(code, &data));
        Ok(serde_json::from_slice(&data)?)
    }
}
pub(crate) fn probe_delay(
    c: &Controller,
    name: &str,
    url: &str,
    timeout: u32,
) -> Result<(Option<i64>, Option<String>)> {
    let query = url::form_urlencoded::Serializer::new(String::new())
        .append_pair("url", url)
        .append_pair("timeout", &timeout.to_string())
        .finish();
    let path = format!(
        "/proxies/{}/delay?{query}",
        utf8_percent_encode(name, NON_ALPHANUMERIC)
    );
    let (code, data) = c.request("GET", &path, &[])?;
    let (delay, error) = match code {
        200 => (
            Some(
                serde_json::from_slice::<Value>(&data)?["delay"]
                    .as_i64()
                    .context("invalid member delay")?,
            ),
            None,
        ),
        400 => (None, Some(controller_error(code, &data))),
        _ => return Err(anyhow!("controller delay HTTP {code}")),
    };
    Ok((delay, error))
}

pub(crate) fn connect_choice<'a>(
    from: &str,
    choices: &'a [String],
    store: &Store,
    measure: impl Fn(&str, u32) -> Result<Option<i64>> + Sync,
) -> Result<Option<&'a str>> {
    let probe = |name: &str| measure(name, crate::checks::DEFAULT_PROBE_TIMEOUT_MS);
    if probe(from)?.is_some() {
        return Ok(None);
    }
    let mut history = store
        .db
        .prepare("SELECT CASE WHEN report IS NULL THEN name ELSE json_extract(report,'$.hops[0]') END,
                         latency_ms FROM probe_results
                  WHERE latency_ms IS NOT NULL AND (report IS NULL OR json_array_length(report,'$.hops')=1)
                  ORDER BY timestamp, report IS NOT NULL")?;
    let known = history
        .query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?)))?
        .collect::<rusqlite::Result<std::collections::HashMap<_, _>>>()?;
    let mut candidates: Vec<_> = choices
        .iter()
        .map(String::as_str)
        .filter(|&n| n != from)
        .collect();
    candidates.sort_by_key(|n| (known.get(*n).is_none(), known.get(*n)));
    thread::scope(|scope| {
        let probe = &probe;
        let workers: Vec<_> = candidates
            .into_iter()
            .take(8)
            .map(|name| (name, scope.spawn(move || probe(name))))
            .collect();
        let mut best = None;
        for (name, worker) in workers {
            if let Some(ms) = worker
                .join()
                .map_err(|_| anyhow!("connect probe panicked"))??
            {
                if best.is_none_or(|(time, _)| ms < time) {
                    best = Some((ms, name));
                }
            }
        }
        Ok(best.map(|(_, name)| name))
    })
}

pub fn latency(c: &Controller, s: &mut Store, url: &str, timeout: u32) -> Result<Value> {
    web_url(url)?;
    ensure!(
        (1..=10000).contains(&timeout),
        "timeout must be 1..10000 ms"
    );
    let members = c.json("/proxies/SmartVPN")?;
    let names = members["all"].as_array().context("missing group members")?;
    let mut results = Vec::new();
    for chunk in names.chunks(4) {
        thread::scope(|scope| -> Result<()> {
            let mut tasks = Vec::new();
            for n in chunk {
                let name = n.as_str().context("invalid group member")?.to_owned();
                tasks.push(scope.spawn(move || -> Result<_> {
                    let (delay, error) = probe_delay(c, &name, url, timeout)?;
                    Ok((name, delay, error))
                }));
            }
            for task in tasks {
                results.push(
                    task.join()
                        .map_err(|_| anyhow!("probe worker panicked"))??,
                );
            }
            Ok(())
        })?;
    }
    let tx =
        s.db.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
    let mut response = Vec::new();
    for (name, delay, error) in results {
        let ts: i64 = tx.query_row(
            "INSERT OR REPLACE INTO probe_results(name,latency_ms,error,timestamp) VALUES(?,?,?,unixepoch()) RETURNING timestamp",
            rusqlite::params![name, delay, error],
            |r| r.get(0),
        )?;
        response.push(json!({"name":name,"latencyMs":delay,"error":error,"timestamp":ts}));
    }
    tx.commit()?;
    Ok(json!({"results":response}))
}
pub fn traffic(mut doc: Value, s: &mut Store) -> Result<Value> {
    let now = Instant::now();
    let up = doc["uploadTotal"].as_u64().context("invalid uploadTotal")?;
    let down = doc["downloadTotal"]
        .as_u64()
        .context("invalid downloadTotal")?;
    let (ur, dr) = match s.sample.replace((now, up, down)) {
        Some((at, u, d)) => {
            let elapsed = (now - at).as_secs_f64();
            (
                (up.checked_sub(u).context("upload counter reset")? as f64 / elapsed) as u64,
                (down.checked_sub(d).context("download counter reset")? as f64 / elapsed) as u64,
            )
        }
        None => (0, 0),
    };
    let rows = doc["connections"].as_array_mut().unwrap();
    for row in rows.iter_mut() {
        let m = row["metadata"].clone();
        for field in ["network", "type", "sourcePort", "destinationPort"] {
            row[field] = m[field].clone();
        }
        let host = m["host"]
            .as_str()
            .filter(|s| !s.is_empty())
            .or(m["destinationIP"].as_str())
            .context("missing destination")?;
        row["target"] = json!(format!(
            "{}:{}",
            host,
            m["destinationPort"].as_str().unwrap()
        ));
        row["process"] = m["process"].clone();
        row["chain"] = json!(row["chains"]
            .as_array()
            .context("missing chains")?
            .iter()
            .map(|v| v.as_str().context("invalid chain member"))
            .collect::<Result<Vec<_>>>()?
            .join(" → "));
    }
    let count = rows.len();
    doc["available"] = json!(true);
    doc["uploadRate"] = json!(ur);
    doc["downloadRate"] = json!(dr);
    doc["connectionCount"] = json!(count);
    doc["shownCount"] = json!(count);
    Ok(doc)
}

#[cfg(test)]
mod connect_tests {
    use super::*;
    use tiny_http::{Response, Server};

    #[test]
    fn healthy_cold_connection_keeps_selected_node() -> Result<()> {
        let mock = Server::http("127.0.0.1:0").unwrap();
        let controller = Controller {
            port: mock.server_addr().to_ip().unwrap().port(),
            secret: "fixture".into(),
        };
        let service = thread::spawn(move || {
            let request = mock.recv().unwrap();
            let url = url::Url::parse(&format!("http://localhost{}", request.url())).unwrap();
            assert_eq!(url.path(), "/proxies/selected/delay");
            let (_, budget) = url.query_pairs().find(|(k, _)| k == "timeout").unwrap();
            // The fixture needs 2.9 s for the handshake and HTTP exchange.
            assert!(budget.parse::<u32>().unwrap() >= 2900);
            request
                .respond(Response::from_string(r#"{"delay":470,"overall":2900}"#))
                .unwrap();
        });
        let dir = tempfile::tempdir()?;
        let store = Store::open(dir.path())?;
        assert_eq!(
            connect_choice(
                "selected",
                &["selected".into(), "previous".into()],
                &store,
                |name, timeout| Ok(probe_delay(
                    &controller,
                    name,
                    "https://fixture.invalid/probe",
                    timeout
                )?
                .0)
            )?,
            None
        );
        service.join().unwrap();
        Ok(())
    }
}
