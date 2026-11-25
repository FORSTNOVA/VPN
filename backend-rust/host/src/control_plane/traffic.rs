use super::{query_param, state::RuntimeState};
use serde_json::{json, Value};
use smartvpn_engine::traffic::Traffic;
use smartvpn_store::{
    traffic::{TrafficHistoryReport, TrafficSample},
    Store,
};
use std::{
    sync::{atomic::Ordering, Arc},
    time::Instant,
};

pub(super) struct Monitor {
    db: Store,
    session: Option<Session>,
}
struct Session {
    key: String,
    source: Arc<Traffic>,
    completed_up: u64,
    completed_down: u64,
    previous: (Instant, u64, u64),
}
impl Monitor {
    pub(super) fn open(home: &std::path::Path) -> Result<Self, String> {
        Ok(Self {
            db: Store::open(home.join("rust_traffic.db")).map_err(|e| e.to_string())?,
            session: None,
        })
    }
    pub(super) fn attach(&mut self, source: Arc<Traffic>) {
        source.enable();
        self.session = Some(Session {
            key: format!("{:032x}", rand::random::<u128>()),
            source,
            completed_up: 0,
            completed_down: 0,
            previous: (Instant::now(), 0, 0),
        });
    }
    pub(super) fn sample(&mut self) -> Result<Value, String> {
        let Some(session) = self.session.as_mut() else {
            return Ok(json!({"available":false,"reason":"未连接"}));
        };
        let mut rows = Vec::new();
        let mut samples = Vec::new();
        let mut finished = Vec::new();
        let (mut up, mut down) = (session.completed_up, session.completed_down);
        let (mut closed_up, mut closed_down) = (0, 0);
        for row in session.source.snapshot() {
            let closed = row.closed.load(Ordering::Acquire);
            let u = row.upload.load(Ordering::Relaxed);
            let d = row.download.load(Ordering::Relaxed);
            let app = row.app.as_deref().unwrap_or("未知应用");
            up += u;
            down += d;
            samples.push(TrafficSample {
                has_id: true,
                connection_key: format!("{}:{}", session.key, row.id),
                app_key: format!("app:{app}"),
                app: app.into(),
                target: row.target.clone(),
                network: row.network.into(),
                chain: row.route.clone(),
                upload: u as i64,
                download: d as i64,
            });
            if closed {
                finished.push(row.id);
                closed_up += u;
                closed_down += d;
            } else {
                rows.push(json!({"process":app,"target":row.target,"network":row.network,"type":"Tun","chain":row.route,"upload":u,"download":d,"durationMs":row.started.elapsed().as_millis()}));
            }
        }
        let now = chrono::Local::now();
        self.db
            .record_traffic(
                &session.key,
                &samples,
                &now.format("%Y-%m-%dT%H").to_string(),
                now.timestamp(),
            )
            .map_err(|e| e.to_string())?;
        session.source.acknowledge(&finished);
        session.completed_up += closed_up;
        session.completed_down += closed_down;
        let (last, last_up, last_down) = session.previous;
        let seconds = last.elapsed().as_secs_f64();
        session.previous = (Instant::now(), up, down);
        Ok(
            json!({"available":true,"uploadTotal":up,"downloadTotal":down,"uploadRate":((up-last_up) as f64/seconds) as u64,"downloadRate":((down-last_down) as f64/seconds) as u64,"connectionCount":rows.len(),"shownCount":rows.len(),"connections":rows}),
        )
    }
    pub(super) fn finish(&mut self) -> Result<(), String> {
        self.sample()?;
        self.session = None;
        Ok(())
    }
    pub(super) fn history(&self, query: &str) -> Result<Value, String> {
        let period = query_param(query, "period").unwrap_or_else(|| "day".into());
        let (fmt, suffix) = match period.as_str() {
            "hour" => ("%Y-%m-%dT%H", ":00"),
            "day" => ("%Y-%m-%d", "T00:00"),
            "month" => ("%Y-%m", "-01T00:00"),
            "year" => ("%Y", "-01-01T00:00"),
            _ => return Err("invalid period".into()),
        };
        let date = query_param(query, "date")
            .unwrap_or_else(|| chrono::Local::now().format(fmt).to_string());
        chrono::NaiveDateTime::parse_from_str(&format!("{date}{suffix}"), "%Y-%m-%dT%H:%M")
            .map_err(|e| e.to_string())?;
        let group = query_param(query, "group").unwrap_or_else(|| "app".into());
        let number = |key, default| {
            query_param(query, key)
                .map(|s| s.parse::<i64>())
                .transpose()
                .map(|n| n.unwrap_or(default))
                .map_err(|e| e.to_string())
        };
        let offset = number("offset", 0)?;
        let limit = number("limit", 100)?;
        if !matches!(group.as_str(), "app" | "connection")
            || offset < 0
            || !(1..=200).contains(&limit)
        {
            return Err("invalid history group or page".into());
        }
        let report = TrafficHistoryReport {
            period,
            date,
            group,
            timezone: "local".into(),
            offset,
            limit,
            ..Default::default()
        };
        let result = self
            .db
            .read_traffic_history(report, &query_param(query, "app").unwrap_or_default())
            .map_err(|e| e.to_string())?;
        Ok(json!(result))
    }
}

pub(super) fn refresh(st: &mut RuntimeState) -> Result<(), String> {
    st.traffic_snapshot = st.traffic.sample()?;
    Ok(())
}
