use rusqlite::Connection;
use smartvpn_store::*;
use std::{
    path::PathBuf,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    },
    time::{SystemTime, UNIX_EPOCH},
};
static NEXT: AtomicU64 = AtomicU64::new(0);
struct Temp {
    dir: PathBuf,
}
impl Temp {
    fn new() -> Self {
        let dir = std::env::temp_dir().join(format!(
            "smartvpn-store-test-{}-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir(&dir).unwrap();
        Self { dir }
    }
    fn db(&self) -> PathBuf {
        self.dir.join("synthetic.db")
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}
fn probe() -> ProbeResult {
    ProbeResult {
        hops: Some(vec!["入口<&>".into(), "出口\u{2028}".into()]),
        target: PROBE_TARGET.into(),
        delay: 80,
        warm_delay: 20,
        measured: 1700000000123,
        diagnostics_version: 1,
        ..Default::default()
    }
}
#[test]
fn schema_wal_migration_and_reopen_preserve_existing_rows() {
    let temp = Temp::new();
    {
        let c = Connection::open(temp.db()).unwrap();
        c.execute_batch(STORE_SCHEMA).unwrap();
        c.execute("INSERT INTO probe_results(path,fingerprint,delay,error,measured) VALUES('[]','legacy',12,'',10)",[]).unwrap();
        c.execute(
            "INSERT INTO settings_kv(key,value) VALUES('sentinel','keep')",
            [],
        )
        .unwrap();
    }
    for _ in 0..2 {
        let s = Store::open(temp.db()).unwrap();
        let rows = s.load_probe_rows().unwrap();
        assert_eq!(rows[0].details, "");
        assert_eq!(rows[0].fingerprint, "legacy");
        assert_eq!(s.get_value("sentinel").unwrap(), Some("keep".into()));
        s.close().unwrap();
        s.close().unwrap();
        assert!(matches!(s.get_value("sentinel"), Err(Error::Closed)));
    }
    let c = Connection::open(temp.db()).unwrap();
    let mode: String = c
        .query_row("PRAGMA journal_mode", [], |r| r.get(0))
        .unwrap();
    assert_eq!(mode, "wal");
    let count:i64=c.query_row("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('nodes','health','region_verify','switch_events','settings_kv','probe_results','traffic_hours','traffic_checkpoints')",[],|r|r.get(0)).unwrap();
    assert_eq!(count, 8);
}
#[test]
fn probes_upsert_details_and_sort_are_persistent() {
    let temp = Temp::new();
    {
        let s = Store::open(temp.db()).unwrap();
        s.save_probe("old", &probe()).unwrap();
        let mut p = probe();
        p.delay = 123;
        p.warm_error = "失败".into();
        s.save_probe("new", &p).unwrap();
        p.hops = Some(vec![]);
        p.measured += 1;
        s.save_probe("empty", &p).unwrap();
        p.hops = None;
        p.measured += 1;
        s.save_probe("nil", &p).unwrap();
    }
    let s = Store::open(temp.db()).unwrap();
    let rows = s.load_probe_rows().unwrap();
    assert_eq!(rows.len(), 3);
    assert_eq!(rows[0].path, "null");
    assert_eq!(rows[1].path, "[]");
    assert_eq!(rows[2].path, r#"["入口\u003c\u0026\u003e","出口\u2028"]"#);
    assert_eq!(rows[2].fingerprint, "new");
    let details: ProbeResult = serde_json::from_str(&rows[2].details).unwrap();
    assert_eq!(details.delay, 123);
    assert_eq!(details.warm_error, "失败");
}
#[test]
fn kv_upsert_nodes_and_atomic_rollback() {
    let temp = Temp::new();
    let s = Store::open(temp.db()).unwrap();
    assert_eq!(s.get_value("none").unwrap(), None);
    s.set_value("key", "first").unwrap();
    s.set_value("key", "第二次").unwrap();
    assert_eq!(s.get_value("key").unwrap(), Some("第二次".into()));
    let mut n = Node {
        name: "n".into(),
        protocol: "ss".into(),
        network: "tcp".into(),
        tls: false,
    };
    s.upsert_nodes(&[n.clone()], 10).unwrap();
    n.network = "ws".into();
    n.tls = true;
    s.upsert_nodes(&[n.clone()], 20).unwrap();
    assert_eq!(s.load_nodes().unwrap(), vec![n]);
    {
        let c = Connection::open(temp.db()).unwrap();
        c.execute_batch("CREATE TRIGGER reject_bad BEFORE INSERT ON nodes WHEN NEW.name='bad' BEGIN SELECT RAISE(ABORT,'synthetic rejection'); END;").unwrap();
    }
    let good = Node {
        name: "good".into(),
        protocol: "ss".into(),
        network: "".into(),
        tls: false,
    };
    let bad = Node {
        name: "bad".into(),
        ..good.clone()
    };
    assert!(s.upsert_nodes(&[good, bad], 30).is_err());
    assert_eq!(s.load_nodes().unwrap().len(), 1);
}
#[test]
fn region_health_roundtrip_clears_and_unset_times() {
    let temp = Temp::new();
    let s = Store::open(temp.db()).unwrap();
    let mut r = Region {
        name: "日本".into(),
        exit_ip: "203.0.113.1".into(),
        country: "JP".into(),
        status: "verified".into(),
        sources: vec!["one".into(), "two".into()],
        detail: "合成测试".into(),
        verified_at: 0,
    };
    s.save_region(&r).unwrap();
    assert_eq!(s.load_regions().unwrap()["日本"], r);
    r.sources.clear();
    r.country = "US".into();
    s.save_region(&r).unwrap();
    assert_eq!(s.load_regions().unwrap()["日本"], r);
    let h = Health {
        name: "n".into(),
        state: "HEALTHY".into(),
        consecutive_failures: 0,
        consecutive_successes: 1,
        latency_ms: 10,
        last_success_at: 0,
        cooldown_until: 0,
        updated_at: 100,
    };
    s.save_health(&h).unwrap();
    assert_eq!(s.load_health().unwrap()["n"], h);
    s.clear_health().unwrap();
    s.clear_regions().unwrap();
    assert!(s.load_health().unwrap().is_empty());
    assert!(s.load_regions().unwrap().is_empty());
}
#[test]
fn switch_events_order_same_time_and_default_limit() {
    let temp = Temp::new();
    let s = Store::open(temp.db()).unwrap();
    for i in 0..55 {
        let e = SwitchEvent {
            at: 10,
            group: "g".into(),
            from_node: "a".into(),
            to_node: "b".into(),
            trigger: i.to_string(),
            evidence: "".into(),
        };
        s.append_switch_event(&e).unwrap();
    }
    let all = s.recent_switch_events(0).unwrap();
    assert_eq!(all.len(), 50);
    assert_eq!(all[0].trigger, "54");
    assert_eq!(s.recent_switch_events(2).unwrap()[1].trigger, "53");
    assert_eq!(s.recent_switch_events(-1).unwrap().len(), 50);
}
#[test]
fn health_params_defaults_partial_null_clamps_and_recovery() {
    let temp = Temp::new();
    let s = Store::open(temp.db()).unwrap();
    assert_eq!(s.load_health_params(false).0, HealthParams::defaults(false));
    assert_eq!(s.load_health_params(true).0.connect_timeout_ms, 6000);
    s.set_value("health_params",r#"{"patrolIntervalSec":1,"failureThreshold":500,"CONNECTTIMEOUTMS":null,"cooldownſec":20}"#).unwrap();
    let (p, e) = s.load_health_params(false);
    assert!(e.is_none());
    assert_eq!(p.patrol_interval_sec, 10);
    assert_eq!(p.failure_threshold, 10);
    assert_eq!(p.connect_timeout_ms, 3000);
    assert_eq!(p.cooldown_sec, 20);
    s.save_health_params(&HealthParams::defaults(true)).unwrap();
    assert_eq!(s.load_health_params(false).0, HealthParams::defaults(true));
    for bad in [
        "broken",
        "[]",
        r#"{"failureThreshold":1.0}"#,
        r#"{"failureThreshold":"bad","failureThreshold":3}"#,
    ] {
        s.set_value("health_params", bad).unwrap();
        let (p, e) = s.load_health_params(false);
        assert!(e.is_some(), "{bad}");
        assert_eq!(p, HealthParams::defaults(false));
    }
    s.set_value("health_params", "null").unwrap();
    assert!(s.load_health_params(false).1.is_none());
}
#[test]
fn serialized_connection_supports_concurrent_writers() {
    let temp = Temp::new();
    let s = Arc::new(Store::open(temp.db()).unwrap());
    let tasks: Vec<_> = (0..8)
        .map(|i| {
            let s = s.clone();
            std::thread::spawn(move || {
                for j in 0..20 {
                    s.set_value(&format!("{i}-{j}"), "synthetic").unwrap();
                }
            })
        })
        .collect();
    for t in tasks {
        t.join().unwrap();
    }
    for i in 0..8 {
        assert_eq!(
            s.get_value(&format!("{i}-19")).unwrap(),
            Some("synthetic".into())
        );
    }
}
#[test]
fn malformed_database_and_missing_parent_fail_without_replacement() {
    let temp = Temp::new();
    std::fs::write(temp.db(), b"not sqlite").unwrap();
    assert!(Store::open(temp.db()).is_err());
    assert_eq!(std::fs::read(temp.db()).unwrap(), b"not sqlite");
    assert!(Store::open(temp.dir.join("missing").join("db")).is_err());
}

#[test]
fn legacy_invalid_utf8_text_is_reported_and_preserved() {
    let temp = Temp::new();
    Store::open(temp.db()).unwrap().close().unwrap();
    {
        let c = Connection::open(temp.db()).unwrap();
        c.execute_batch("INSERT INTO settings_kv(key,value) VALUES('bad',CAST(x'80ff' AS TEXT)); INSERT INTO probe_results(path,fingerprint,delay,error,measured,details) VALUES('[]','synthetic',1,'',1,CAST(x'80ff' AS TEXT));").unwrap();
    }
    let store = Store::open(temp.db()).unwrap();
    assert!(store.get_value("bad").is_err());
    assert!(store.load_probe_rows().is_err());
    store.close().unwrap();
    let c = Connection::open(temp.db()).unwrap();
    let kv: Vec<u8> = c
        .query_row(
            "SELECT CAST(value AS BLOB) FROM settings_kv WHERE key='bad'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    let details: Vec<u8> = c
        .query_row("SELECT CAST(details AS BLOB) FROM probe_results", [], |r| {
            r.get(0)
        })
        .unwrap();
    assert_eq!(kv, vec![0x80, 0xff]);
    assert_eq!(details, kv);
}

#[test]
fn health_params_unpaired_surrogate_key_is_rejected_without_rewriting() {
    let temp = Temp::new();
    let store = Store::open(temp.db()).unwrap();
    // Known compatibility boundary: Go replaces unpaired UTF-16 surrogates
    // and ignores the resulting unknown key. serde_json rejects that key.
    for raw in [
        r#"{"failureThreshold":7,"\ud800":1}"#,
        r#"{"\udfff":1,"failureThreshold":7}"#,
    ] {
        store.set_value("health_params", raw).unwrap();
        let (params, error) = store.load_health_params(false);
        assert!(error.is_some(), "unpaired surrogate key must be reported");
        assert_eq!(params, HealthParams::defaults(false));
        assert_eq!(
            store.get_value("health_params").unwrap().as_deref(),
            Some(raw)
        );
    }
}
