use rusqlite::Connection;
use smartvpn_store::{raw::*, raw_traffic::*, traffic::*, Store};
static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
struct Temp(std::path::PathBuf);
impl Temp {
    fn new() -> Self {
        let p = std::env::temp_dir().join(format!(
            "smartvpn-raw-{}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        std::fs::create_dir(&p).unwrap();
        Self(p)
    }
    fn db(&self) -> std::path::PathBuf {
        self.0.join("raw.db")
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
fn b() -> RawText {
    RawText(vec![b'a', 0, 0x80, 0xff])
}
#[test]
fn lossless_text_roundtrip_every_store_record() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let v = b();
    s.set_value_raw(&v, &v).unwrap();
    assert_eq!(s.get_value_raw(&v).unwrap(), Some(v.clone()));
    let n = RawNode {
        name: v.clone(),
        protocol: v.clone(),
        network: v.clone(),
        tls: true,
    };
    s.upsert_nodes_raw(std::slice::from_ref(&n), 7).unwrap();
    assert_eq!(s.load_nodes_raw().unwrap(), vec![n]);
    let p = RawProbeRow {
        path: v.clone(),
        fingerprint: v.clone(),
        delay: 1,
        error: v.clone(),
        measured: 2,
        details: v.clone(),
    };
    s.save_probe_row_raw(&p).unwrap();
    assert_eq!(s.load_probe_rows_raw().unwrap(), vec![p]);
    let r = RawRegion {
        name: v.clone(),
        exit_ip: v.clone(),
        country: v.clone(),
        status: v.clone(),
        sources: vec![v.clone(), RawText::from("")],
        detail: v.clone(),
        verified_at: 3,
    };
    s.save_region_raw(&r).unwrap();
    assert_eq!(s.load_regions_raw().unwrap().get(&v), Some(&r));
    let h = RawHealth {
        name: v.clone(),
        state: v.clone(),
        consecutive_failures: 1,
        consecutive_successes: 2,
        latency_ms: 3,
        last_success_at: 4,
        cooldown_until: 5,
        updated_at: 6,
    };
    s.save_health_raw(&h).unwrap();
    assert_eq!(s.load_health_raw().unwrap().get(&v), Some(&h));
    let e = RawSwitchEvent {
        at: 1,
        group: v.clone(),
        from_node: v.clone(),
        to_node: v.clone(),
        trigger: v.clone(),
        evidence: v.clone(),
    };
    s.append_switch_event_raw(&e).unwrap();
    assert_eq!(s.recent_switch_events_raw(0).unwrap(), vec![e]);
    s.close().unwrap();
    let s = Store::open(t.db()).unwrap();
    assert_eq!(s.get_value_raw(&v).unwrap(), Some(v.clone()));
    let c = Connection::open(t.db()).unwrap();
    let (kind, hex): (String, String) = c
        .query_row(
            "SELECT typeof(value),hex(value) FROM settings_kv WHERE key=CAST(x'610080ff' AS TEXT)",
            [],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .unwrap();
    assert_eq!(kind, "text");
    assert_eq!(hex, "610080FF");
    let n: i64 = c
        .query_row(
            "SELECT count(*) FROM settings_kv WHERE key=x'610080ff'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(n, 0, "TEXT must not become BLOB");
    s.clear_regions_raw().unwrap();
    s.clear_health_raw().unwrap();
    assert!(s.load_regions_raw().unwrap().is_empty());
    assert!(s.load_health_raw().unwrap().is_empty());
}
#[test]
fn raw_traffic_keeps_bytes_filters_and_checkpoint_identity() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let v = b();
    let sample = RawTrafficSample {
        has_id: true,
        connection_key: v.clone(),
        app_key: v.clone(),
        app: v.clone(),
        target: v.clone(),
        network: v.clone(),
        chain: v.clone(),
        upload: 5,
        download: 6,
    };
    for _ in 0..2 {
        s.record_traffic_raw(
            &v,
            std::slice::from_ref(&sample),
            &RawText::from("2026-11-16T01"),
            1,
        )
        .unwrap();
    }
    let q = RawTrafficHistoryReport {
        date: RawText::from("2026"),
        group: RawText::from("connection"),
        limit: 10,
        ..Default::default()
    };
    let outcome = s.read_traffic_history_partial_raw(q, &v);
    assert!(outcome.error.is_none());
    let r = outcome.report;
    assert_eq!((r.upload_total, r.download_total), (5, 6));
    assert_eq!(r.rows[0].id, v);
    assert_eq!(r.rows[0].app, sample.app);
    assert_eq!(r.rows[0].chain, sample.chain);
    let c = Connection::open(t.db()).unwrap();
    let kind: String = c
        .query_row("SELECT typeof(session) FROM traffic_checkpoints", [], |r| {
            r.get(0)
        })
        .unwrap();
    assert_eq!(kind, "text");
}
fn report() -> TrafficHistoryReport {
    TrafficHistoryReport {
        date: "2026".into(),
        group: "connection".into(),
        limit: 10,
        upload_total: 91,
        download_total: 92,
        connection_count: 93,
        row_count: 94,
        has_more: true,
        rows: vec![TrafficHistoryRow::default()],
        ..Default::default()
    }
}
#[test]
fn traffic_partial_preserves_totals_and_prior_rows_on_scan_error() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let c = Connection::open(t.db()).unwrap();
    c.execute_batch("INSERT INTO traffic_hours VALUES('2026','a','a','A','t','tcp','c',10,2,1,2);INSERT INTO traffic_hours VALUES('2026','b','b','B','t','tcp','c',1,1,x'01',2);").unwrap();
    let out = s.read_traffic_history_partial(report(), "");
    assert!(out.error.is_some());
    assert_eq!(
        (
            out.report.upload_total,
            out.report.download_total,
            out.report.row_count
        ),
        (11, 3, 2)
    );
    assert_eq!(out.report.rows.len(), 1);
    assert_eq!(out.report.rows[0].id, "a");
    assert!(out.report.has_more, "not recomputed after error");
    let out = s.read_traffic_history_partial_raw(
        RawTrafficHistoryReport {
            date: "2026".into(),
            group: "connection".into(),
            limit: 10,
            ..Default::default()
        },
        &"".into(),
    );
    assert!(out.error.is_some());
    assert_eq!(out.report.rows.len(), 1);
}
#[test]
fn traffic_partial_begin_and_totals_scan_failures() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let c = Connection::open(t.db()).unwrap();
    c.execute_batch(
        "INSERT INTO traffic_hours VALUES('2026','a','a','A','t','tcp','c',7,1.5,1,2);",
    )
    .unwrap();
    let out = s.read_traffic_history_partial(report(), "");
    assert!(out.error.is_some());
    assert_eq!(out.report.upload_total, 7);
    assert_eq!(out.report.download_total, 92);
    assert_eq!(out.report.connection_count, 93);
    assert!(out.report.rows.is_empty());
    s.close().unwrap();
    let out = s.read_traffic_history_partial(report(), "");
    assert!(out.error.is_some());
    assert!(out.report.rows.is_empty());
    assert_eq!(out.report.upload_total, 91);
}

#[test]
fn raw_reads_legacy_text_and_blob_without_rewriting() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let c = Connection::open(t.db()).unwrap();
    c.execute_batch("INSERT INTO settings_kv VALUES('legacy',CAST(x'80ff00' AS TEXT));INSERT INTO settings_kv VALUES('blob',x'80ff00');").unwrap();
    assert_eq!(
        s.get_value_raw(&"legacy".into()).unwrap(),
        Some(RawText(vec![0x80, 0xff, 0]))
    );
    assert_eq!(
        s.get_value_raw(&"blob".into()).unwrap(),
        Some(RawText(vec![0x80, 0xff, 0]))
    );
    let (kind, hex): (String, String) = c
        .query_row(
            "SELECT typeof(value),hex(value) FROM settings_kv WHERE key='blob'",
            [],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .unwrap();
    assert_eq!((kind.as_str(), hex.as_str()), ("blob", "80FF00"));
}

#[test]
fn legacy_probe_blob_columns_read_losslessly_without_reclassifying() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let c = Connection::open(t.db()).unwrap();
    c.execute_batch(
        "INSERT INTO probe_results VALUES(x'80ff00',x'80ff00',1,x'80ff00',2,x'80ff00');",
    )
    .unwrap();
    let rows = s.load_probe_rows_raw().unwrap();
    let r = &rows[0];
    let expected = RawText(vec![0x80, 0xff, 0]);
    assert_eq!(r.path, expected);
    assert_eq!(r.fingerprint, expected);
    assert_eq!(r.error, expected);
    assert_eq!(r.details, expected);
    let types:(String,String,String,String)=c.query_row("SELECT typeof(path),typeof(fingerprint),typeof(error),typeof(details) FROM probe_results",[],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?))).unwrap();
    assert_eq!(
        types,
        ("blob".into(), "blob".into(), "blob".into(), "blob".into())
    );
}
