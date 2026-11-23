use smartvpn_host::*;
fn host() -> Host<MemoryResources> {
    Host::new(7, b"test".to_vec(), true, MemoryResources::default()).unwrap()
}
fn execute<R: Resources>(h: &mut Host<R>, id: u64, q: Request) -> Result<Response, HostError> {
    match h.admit(id, q)? {
        Admission::Execute(t) => h.execute(t),
        Admission::Replay(r) => Ok(r),
    }
}
fn mutation(rev: u64) -> Request {
    Request::SetSamplingError {
        expected_revision: rev,
        value: b"synthetic failure".to_vec(),
    }
}
#[test]
fn successful_mutation_duplicate_exactly_once() {
    let mut h = host();
    let r = execute(&mut h, 1, mutation(0)).unwrap();
    assert_eq!(r.revision, 1);
    assert_eq!(execute(&mut h, 1, mutation(0)), Ok(r));
    assert_eq!(h.resources().persists, 1);
    assert!(matches!(h.admit(1, mutation(1)), Err(HostError::Conflict)));
    assert!(!h.checkpoint().unwrap().pending);
}
#[test]
fn old_replay_is_rejected_not_reexecuted() {
    let mut h = host();
    execute(&mut h, 1, mutation(0)).unwrap();
    execute(&mut h, 2, Request::Snapshot).unwrap();
    assert_eq!(execute(&mut h, 1, mutation(0)), Err(HostError::Stale));
    assert_eq!(h.resources().persists, 1);
}
#[test]
fn admission_close_drains_existing_only() {
    let mut h = host();
    let t = match h.admit(1, mutation(0)).unwrap() {
        Admission::Execute(t) => t,
        _ => panic!(),
    };
    h.begin_shutdown();
    assert_eq!(h.phase(), Phase::Closing);
    assert!(matches!(
        h.admit(2, Request::Snapshot),
        Err(HostError::Closing)
    ));
    assert_eq!(h.resources().listener_closes, 1);
    assert_eq!(h.resources().store_closes, 0);
    assert_eq!(h.execute(t).unwrap().revision, 1);
    assert_eq!(h.phase(), Phase::Closed);
    h.begin_shutdown();
    assert_eq!(h.resources().store_closes, 1);
    assert_eq!(h.execute(t), Err(HostError::UnknownTicket));
}
#[test]
fn all_known_routes_report_their_offline_status() {
    let mut h = host();
    for (i, (method, path)) in smartvpn_core::http_core::ROUTES.iter().enumerate() {
        let r = execute(
            &mut h,
            (i + 1) as u64,
            Request::Http {
                method: method.as_bytes().to_vec(),
                path: path.as_bytes().to_vec(),
                authorization: b"Bearer test".to_vec(),
            },
        )
        .unwrap();
        let expected = match *path {
            "/api/identify" | "/api/traffic" => 200,
            "/api/nodes/latency" | "/api/latency" => 409,
            _ => 501,
        };
        assert_eq!(r.status, expected, "{path}");
    }
    assert_eq!(h.resources().persists, 0);
}
#[test]
fn auth_and_route_precedence() {
    let mut h = host();
    for (id, method, path, status) in [
        (1, "GET", "/api/state", 401),
        (2, "NO", "/api/state", 405),
        (3, "GET", "/missing", 404),
        (4, "HEAD", "/api/identify", 200),
    ] {
        let r = execute(
            &mut h,
            id,
            Request::Http {
                method: method.as_bytes().to_vec(),
                path: path.as_bytes().to_vec(),
                authorization: vec![],
            },
        )
        .unwrap();
        assert_eq!(r.status, status);
        if method == "HEAD" {
            assert!(r.body.is_empty());
        }
    }
}
#[derive(Default)]
struct Fault {
    writes: usize,
    listener: usize,
    store: usize,
    panic: bool,
}
impl Resources for Fault {
    fn persist(&mut self, _: &smartvpn_core::service_state::Checkpoint) -> Result<(), String> {
        self.writes += 1;
        if self.panic {
            panic!("synthetic after effect")
        }
        Err("uncertain".into())
    }
    fn close_listener(&mut self) -> Result<(), String> {
        self.listener += 1;
        Err("listener".into())
    }
    fn close_store(&mut self) -> Result<(), String> {
        self.store += 1;
        Err("store".into())
    }
}
#[test]
fn after_effect_error_no_replay_or_stale_takeover() {
    let mut h = Host::new(1, vec![], true, Fault::default()).unwrap();
    assert_eq!(
        execute(&mut h, 1, mutation(0)),
        Err(HostError::StoreUncertain)
    );
    assert_eq!(
        execute(&mut h, 1, mutation(0)),
        Err(HostError::StoreUncertain)
    );
    let cp = h.checkpoint().unwrap();
    assert_eq!(cp.snapshot.revision, 1);
    assert!(cp.pending);
    assert_eq!(
        execute(&mut h, 2, mutation(1)),
        Err(HostError::StoreUncertain)
    );
    assert_eq!(h.resources().writes, 1);
    h.begin_shutdown();
    h.begin_shutdown();
    assert_eq!(h.resources().listener, 1);
    assert_eq!(h.resources().store, 1);
    assert_eq!(h.shutdown_error(), Some("listener"));
}
#[test]
fn panic_after_persist_still_failclosed() {
    let mut h = Host::new(
        1,
        vec![],
        true,
        Fault {
            panic: true,
            ..Fault::default()
        },
    )
    .unwrap();
    let caught = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        execute(&mut h, 1, mutation(0))
    }));
    assert_eq!(caught.unwrap(), Err(HostError::StoreUncertain));
    assert_eq!(
        execute(&mut h, 1, mutation(0)),
        Err(HostError::StoreUncertain)
    );
    assert_eq!(
        execute(&mut h, 2, mutation(1)),
        Err(HostError::StoreUncertain)
    );
    assert_eq!(h.resources().writes, 1);
}
#[test]
fn bounded_admission_and_instance_isolation() {
    let mut a = host();
    let mut b = host();
    for id in 1..=64 {
        assert!(a.admit(id, Request::Snapshot).is_ok());
    }
    assert!(matches!(
        a.admit(65, Request::Snapshot),
        Err(HostError::Capacity)
    ));
    execute(&mut b, 1, mutation(0)).unwrap();
    assert_eq!(a.checkpoint().unwrap().snapshot.revision, 0);
    assert_eq!(b.checkpoint().unwrap().snapshot.revision, 1);
}
#[test]
fn framing_truncation_and_limit_before_effect() {
    for frame in [vec![1], vec![1, 0, 0, 0], u32::MAX.to_le_bytes().to_vec()] {
        let mut out = vec![];
        assert!(wire::run(&mut frame.as_slice(), &mut out).is_err());
        assert!(out.is_empty());
    }
}
#[test]
fn executable_synthetic_stdin_replay_and_shutdown() {
    use std::io::Write;
    use std::process::{Command, Stdio};
    let mut input = wire::encode_request(1, Some(&mutation(0)));
    input.extend(wire::encode_request(1, Some(&mutation(0))));
    input.extend(wire::encode_request(2, None));
    let mut child = Command::new(env!("CARGO_BIN_EXE_smartvpn-host"))
        .arg("--offline-stdio")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    child.stdin.take().unwrap().write_all(&input).unwrap();
    let result = child.wait_with_output().unwrap();
    assert!(result.status.success());
    let mut bytes = result.stdout.as_slice();
    let mut frames = vec![];
    while !bytes.is_empty() {
        let n = u32::from_le_bytes(bytes[..4].try_into().unwrap()) as usize;
        frames.push(bytes[4..4 + n].to_vec());
        bytes = &bytes[4 + n..];
    }
    assert_eq!(frames.len(), 3);
    assert_eq!(frames[0], frames[1]);
    assert_eq!(u64::from_le_bytes(frames[0][2..10].try_into().unwrap()), 1);
    assert_eq!(&frames[2][14..], b"closed\n");
}

#[derive(Default)]
struct ClosePanic {
    listener: usize,
    store: usize,
    persist: usize,
    listener_panic: bool,
    store_panic: bool,
    persist_panic: bool,
}
impl Resources for ClosePanic {
    fn persist(&mut self, _: &smartvpn_core::service_state::Checkpoint) -> Result<(), String> {
        self.persist += 1;
        if self.persist_panic {
            panic!("persist after effect")
        }
        Ok(())
    }
    fn close_listener(&mut self) -> Result<(), String> {
        self.listener += 1;
        if self.listener_panic {
            panic!("listener after effect")
        }
        Ok(())
    }
    fn close_store(&mut self) -> Result<(), String> {
        self.store += 1;
        if self.store_panic {
            panic!("store after effect")
        }
        Ok(())
    }
}
#[test]
fn listener_panic_empty_queue_still_closes_authority_once() {
    let mut h = Host::new(
        1,
        vec![],
        true,
        ClosePanic {
            listener_panic: true,
            ..ClosePanic::default()
        },
    )
    .unwrap();
    h.begin_shutdown();
    assert_eq!(h.phase(), Phase::Closed);
    assert!(h
        .shutdown_error()
        .unwrap()
        .contains("listener close panicked"));
    assert!(h.checkpoint().is_err());
    h.begin_shutdown();
    assert_eq!(h.resources().listener, 1);
    assert_eq!(h.resources().store, 1);
}
#[test]
fn store_panic_still_closes_authority_once() {
    let mut h = Host::new(
        1,
        vec![],
        true,
        ClosePanic {
            store_panic: true,
            ..ClosePanic::default()
        },
    )
    .unwrap();
    h.begin_shutdown();
    assert_eq!(h.phase(), Phase::Closed);
    assert!(h.shutdown_error().unwrap().contains("store close panicked"));
    assert!(h.checkpoint().is_err());
    h.begin_shutdown();
    assert_eq!(h.resources().listener, 1);
    assert_eq!(h.resources().store, 1);
}
#[test]
fn last_draining_ticket_persist_panic_retains_error_receipt_and_closes() {
    let mut h = Host::new(
        1,
        vec![],
        true,
        ClosePanic {
            persist_panic: true,
            listener_panic: true,
            store_panic: true,
            ..ClosePanic::default()
        },
    )
    .unwrap();
    let t = match h.admit(1, mutation(0)).unwrap() {
        Admission::Execute(t) => t,
        _ => panic!(),
    };
    h.begin_shutdown();
    assert_eq!(h.phase(), Phase::Closing);
    assert_eq!(h.execute(t), Err(HostError::StoreUncertain));
    assert_eq!(h.phase(), Phase::Closed);
    assert_eq!(
        execute(&mut h, 1, mutation(0)),
        Err(HostError::StoreUncertain)
    );
    assert_eq!(h.execute(t), Err(HostError::UnknownTicket));
    h.begin_shutdown();
    assert!(h.checkpoint().is_err());
    assert_eq!(h.resources().persist, 1);
    assert_eq!(h.resources().listener, 1);
    assert_eq!(h.resources().store, 1);
    assert!(h
        .shutdown_error()
        .unwrap()
        .contains("listener close panicked"));
}
