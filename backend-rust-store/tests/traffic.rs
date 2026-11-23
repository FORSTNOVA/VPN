use rusqlite::Connection;
use smartvpn_store::{traffic::*, Store};
static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
struct Temp(std::path::PathBuf);
impl Temp {
    fn new() -> Self {
        let p = std::env::temp_dir().join(format!(
            "smartvpn-traffic-{}-{}-{}",
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
        self.0.join("test.db")
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
fn sample(key: &str, up: i64, down: i64) -> TrafficSample {
    TrafficSample {
        has_id: true,
        connection_key: key.into(),
        app_key: "app:a".into(),
        app: "A".into(),
        network: "tcp".into(),
        upload: up,
        download: down,
        ..Default::default()
    }
}
fn report() -> TrafficHistoryReport {
    TrafficHistoryReport {
        date: "2026-11".into(),
        group: "connection".into(),
        limit: 100,
        ..Default::default()
    }
}
#[test]
fn retry_reset_negative_session_and_hour_boundaries() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    for _ in 0..2 {
        s.record_traffic("a", &[sample("a", 10, 20)], "2026-11-16T01", 1)
            .unwrap();
    }
    s.record_traffic("a", &[sample("a", 15, 22)], "2026-11-16T02", 2)
        .unwrap();
    s.record_traffic("a", &[sample("a", 3, -1)], "2026-11-16T02", 3)
        .unwrap();
    s.record_traffic("a", &[sample("a", 5, 4)], "2026-11-16T02", 4)
        .unwrap();
    let r = s.read_traffic_history(report(), "").unwrap();
    assert_eq!(
        (r.upload_total, r.download_total, r.connection_count),
        (20, 26, 1)
    );
    s.record_traffic("b", &[], "2026-11-16T02", 5).unwrap();
    let c = Connection::open(t.db()).unwrap();
    let n: i64 = c
        .query_row("SELECT count(*) FROM traffic_checkpoints", [], |r| r.get(0))
        .unwrap();
    assert_eq!(n, 0);
    s.record_traffic("b", &[sample("b", 2, 3)], "2026-11-16T02", 6)
        .unwrap();
    assert_eq!(
        s.read_traffic_history(report(), "").unwrap().upload_total,
        22
    );
}
#[test]
fn metadata_pagination_filter_and_empty_ids() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    let mut a = sample("a", 5, 5);
    let mut b = sample("b", 10, 0);
    b.app_key = "app:b".into();
    let mut skip = sample("skip", 999, 999);
    skip.has_id = false;
    s.record_traffic("s", &[a.clone(), b, skip], "2026-11-16T01", 10)
        .unwrap();
    a.network = "udp".into();
    a.target = "new".into();
    s.record_traffic("s", &[a], "2026-11-16T01", 5).unwrap();
    let mut q = report();
    q.limit = 1;
    let r = s.read_traffic_history(q.clone(), "").unwrap();
    assert!(r.has_more);
    assert_eq!(r.rows[0].id, "a");
    assert_eq!(r.rows[0].network, "tcp");
    assert_eq!(r.rows[0].target, "new");
    assert_eq!((r.rows[0].first_seen, r.rows[0].last_seen), (10, 10));
    q.offset = 1;
    assert!(!s.read_traffic_history(q, "").unwrap().has_more);
    let mut q = report();
    q.group = "app".into();
    let r = s.read_traffic_history(q, "app:a").unwrap();
    assert_eq!(r.row_count, 1);
    assert_eq!(r.rows[0].target, "");
    assert_eq!(r.rows[0].network, "");
    assert!(s
        .read_traffic_history(report(), "missing")
        .unwrap()
        .rows
        .is_empty());
}
#[test]
fn checkpoint_failure_rolls_back_history_and_session_cleanup() {
    let t = Temp::new();
    let s = Store::open(t.db()).unwrap();
    s.record_traffic("old", &[sample("old", 1, 2)], "2026-11-16T01", 1)
        .unwrap();
    let c = Connection::open(t.db()).unwrap();
    c.execute_batch("CREATE TRIGGER fail_checkpoint BEFORE INSERT ON traffic_checkpoints WHEN NEW.connection_key='bad' BEGIN SELECT RAISE(ABORT,'fixture'); END;").unwrap();
    assert!(s
        .record_traffic(
            "new",
            &[sample("good", 3, 4), sample("bad", 5, 6)],
            "2026-11-16T01",
            2
        )
        .is_err());
    assert_eq!(
        s.read_traffic_history(report(), "").unwrap().upload_total,
        1
    );
    let session: String = c
        .query_row("SELECT session FROM traffic_checkpoints", [], |r| r.get(0))
        .unwrap();
    assert_eq!(session, "old");
}
#[test]
fn counter_boundary_semantics() {
    assert_eq!(counter_delta(-1, 2), 0);
    assert_eq!(counter_delta(2, 3), 2);
    assert_eq!(counter_delta(i64::MAX, -1), i64::MIN);
}
