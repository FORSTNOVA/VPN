//! Runs only against a caller-supplied synthetic fixture database.
use smartvpn_store::*;
fn main() -> Result<()> {
    let args: Vec<_> = std::env::args().collect();
    assert_eq!(args.len(), 3, "mode and synthetic DB path required");
    let path = std::path::Path::new(&args[2]);
    if args[1] == "create" {
        assert!(!path.exists(), "refusing to replace an existing DB");
    } else {
        assert_eq!(args[1], "roundtrip");
        assert!(path.exists());
    }
    let store = Store::open(path)?;
    if args[1] == "roundtrip" {
        assert_eq!(store.get_value("go-key")?, Some("合成<&>".into()));
        let rows = store.load_probe_rows()?;
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].fingerprint, "go-fingerprint");
        let probe: ProbeResult = serde_json::from_str(&rows[0].details)?;
        assert_eq!(
            probe.hops,
            Some(vec!["入口<&>".into(), "出口\u{2028}".into()])
        );
        assert_eq!(probe.warm_delay, 42);
        assert!(probe.entry_attempt);
        assert_eq!(probe.entry_ip, "2001:db8::1");
        assert_eq!(go_json(&probe)?, rows[0].details);
        assert_eq!(store.load_nodes()?[0].protocol, "ss");
        assert_eq!(store.load_regions()?["synthetic"].country, "JP");
        assert_eq!(store.load_health()?["synthetic"].last_success_at, 0);
        assert_eq!(store.recent_switch_events(0)?[0].trigger, "go-seed");
    }
    let result = ProbeResult {
        hops: Some(vec!["入口<&>".into(), "出口\u{2028}".into()]),
        target: PROBE_TARGET.into(),
        delay: 222,
        warm_delay: 33,
        measured: 1700000000999,
        diagnostics_version: 1,
        error: "故障<&>".into(),
        ..Default::default()
    };
    store.save_probe("rust-fingerprint", &result)?;
    store.set_value("rust-key", "written<&>")?;
    store.upsert_nodes(
        &[Node {
            name: "synthetic".into(),
            protocol: "trojan".into(),
            network: "grpc".into(),
            tls: true,
        }],
        333,
    )?;
    store.save_region(&Region {
        name: "synthetic".into(),
        exit_ip: "198.51.100.2".into(),
        country: "US".into(),
        status: "verified".into(),
        sources: vec!["x".into(), "y".into()],
        detail: "Rust fixture".into(),
        verified_at: 333,
    })?;
    store.save_health(&Health {
        name: "synthetic".into(),
        state: "RECOVERY".into(),
        consecutive_failures: 2,
        consecutive_successes: 3,
        latency_ms: 444,
        last_success_at: 0,
        cooldown_until: 0,
        updated_at: 333,
    })?;
    for trigger in ["rust-first", "rust-second"] {
        store.append_switch_event(&SwitchEvent {
            at: 300,
            group: "SmartVPN".into(),
            from_node: "a".into(),
            to_node: "b".into(),
            trigger: trigger.into(),
            evidence: "synthetic".into(),
        })?;
    }
    store.save_health_params(&HealthParams::defaults(true))?;
    store.close()?;
    println!("Rust fixture read/write complete");
    Ok(())
}
