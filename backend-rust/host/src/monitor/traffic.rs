use super::{json_response, Controller};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Deserializer};
use serde_json::{json, Value};
use smartvpn_core::traffic_projection::{connection_rows, Facts};
use std::time::{Duration, Instant, SystemTime};

// Go's encoding/json leaves primitive fields at their zero value for null.
fn null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Metadata {
    #[serde(deserialize_with = "null_default")]
    process: String,
    #[serde(deserialize_with = "null_default")]
    process_path: String,
    #[serde(deserialize_with = "null_default")]
    host: String,
    #[serde(rename = "destinationIP", deserialize_with = "null_default")]
    destination_ip: String,
    destination_port: Value,
    #[serde(deserialize_with = "null_default")]
    network: String,
    #[serde(rename = "type", deserialize_with = "null_default")]
    kind: String,
}
#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Connection {
    #[serde(deserialize_with = "null_default")]
    upload: i64,
    #[serde(deserialize_with = "null_default")]
    download: i64,
    #[serde(deserialize_with = "null_default")]
    rule: String,
    #[serde(deserialize_with = "null_default")]
    rule_payload: String,
    chains: Option<Vec<Option<String>>>,
    #[serde(deserialize_with = "null_default")]
    start: String,
    metadata: Option<Metadata>,
}
#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Snapshot {
    #[serde(deserialize_with = "null_default")]
    upload_total: i64,
    #[serde(deserialize_with = "null_default")]
    download_total: i64,
    connections: Option<Vec<Option<Connection>>>,
}
fn host_port(host: &str, port: &str) -> String {
    if host.contains(':') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    }
}
impl Connection {
    fn facts(self, now: DateTime<Utc>) -> Facts {
        let m = self.metadata.unwrap_or_default();
        let path = m.process_path.trim().replace('\\', "/");
        let base = path
            .trim_end_matches('/')
            .rsplit('/')
            .next()
            .unwrap_or_default();
        let host = m.host.trim();
        let ip = m.destination_ip.trim();
        let port = match m.destination_port {
            Value::Null => String::new(),
            Value::String(s) if s == "null" => String::new(),
            Value::String(s) => s,
            other => other.to_string(),
        };
        let port = port.trim();
        let hp = host_port(host, port);
        let ipp = host_port(ip, port);
        let elapsed = DateTime::parse_from_rfc3339(&self.start).ok().map(|s| {
            now.signed_duration_since(s)
                .num_nanoseconds()
                .unwrap_or_else(|| if now >= s { i64::MAX } else { i64::MIN })
        });
        Facts {
            s: [
                m.process.trim(),
                &path,
                base,
                host,
                ip,
                port,
                &hp,
                &ipp,
                "",
                &m.network.to_ascii_lowercase(),
                &m.kind,
                self.rule.trim(),
                self.rule_payload.trim(),
            ]
            .map(|s| s.as_bytes().to_vec()),
            up: self.upload,
            down: self.download,
            elapsed: elapsed.unwrap_or(0),
            parsed: elapsed.is_some(),
            chains: self
                .chains
                .unwrap_or_default()
                .iter()
                .flatten()
                .map(|s| s.trim().as_bytes().to_vec())
                .collect(),
        }
    }
}
pub(super) type Baseline = Option<(Instant, i64, i64)>;
impl Controller {
    pub(super) async fn current_traffic(&self) -> (u16, Vec<u8>) {
        // Serialize these samples so a slower earlier read cannot overwrite a
        // newer baseline. Other routes do not hold this lock.
        let mut baseline = self.traffic.lock().await;
        let Ok(s) = self
            .get::<Option<Snapshot>>("/connections", 4 << 20, Duration::from_secs(5))
            .await
        else {
            *baseline = None;
            return json_response(json!({"available":false,"reason":"无法读取内核连接表"}));
        };
        let s = s.unwrap_or_default();
        let now = Instant::now();
        let rates = baseline
            .replace((now, s.upload_total, s.download_total))
            .and_then(|(prev, up, down)| {
                smartvpn_core::traffic_logic::rates(
                    up,
                    down,
                    s.upload_total,
                    s.download_total,
                    now.duration_since(prev).as_nanos().min(i64::MAX as u128) as i64,
                    false,
                )
            })
            .unwrap_or((0, 0));
        let wall = DateTime::<Utc>::from(SystemTime::now());
        let connections = s.connections.unwrap_or_default();
        let count = connections.len();
        let facts = connections
            .into_iter()
            .map(|c| c.unwrap_or_default().facts(wall))
            .collect();
        let rows: Vec<_> = connection_rows(facts)
            .into_iter()
            .map(|row| {
                let [process, target, network, kind, rule, chain] = row
                    .s
                    .map(|s| String::from_utf8(s).expect("projection of UTF-8 metadata"));
                json!({"process":process, "target":target, "network":network, "type":kind,
                    "rule":rule, "chain":chain, "upload":row.up, "download":row.down,
                    "durationMs":row.duration})
            })
            .collect();
        json_response(
            json!({"available":true,"uploadTotal":s.upload_total,"downloadTotal":s.download_total,
            "uploadRate":rates.0,"downloadRate":rates.1,"connectionCount":count,"shownCount":rows.len(),"connections":rows}),
        )
    }
}
