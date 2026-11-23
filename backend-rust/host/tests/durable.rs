use smartvpn_core::service_state::{Command, Snapshot, State};
use smartvpn_host::{durable::*, wire, Request, Response};
use std::{
    fs,
    path::PathBuf,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc, Mutex,
    },
};
static NEXT: AtomicU64 = AtomicU64::new(0);
struct Temp(PathBuf);
impl Temp {
    fn new() -> Self {
        let p = std::env::temp_dir().join(format!(
            "smartvpn-offline-43-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::SeqCst)
        ));
        fs::create_dir(&p).unwrap();
        Self(p)
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
fn q(rev: u64) -> Request {
    Request::SetSamplingError {
        expected_revision: rev,
        value: vec![0xff, 0, b'x'],
    }
}
fn host(t: &Temp) -> DurableHost<DiskJournal> {
    DurableHost::open(DiskJournal::open(&t.0).unwrap(), 1, vec![], true).unwrap()
}

#[test]
fn legacy_http_receipt_replays_then_new_id_uses_idle_handler() {
    let dir = Temp::new();
    let req = Request::Http {
        method: b"GET".to_vec(),
        path: b"/api/latency".to_vec(),
        authorization: b"Bearer ".to_vec(),
    };
    let old = Response {
        status: 501,
        revision: 0,
        body: b"unsupported host business adapter: route 16\n".to_vec(),
    };
    let mut e = initial();
    e.highwater = 1;
    e.receipt = Some(DurableReceipt {
        legacy_http: true,
        id: 1,
        request: req.clone(),
        response: old.clone(),
    });
    let encoded = encode(&e).unwrap();
    assert_eq!(&encoded[..8], b"SVHOST01");
    fs::write(dir.0.join("00000000000000000001.checkpoint"), encoded).unwrap();
    let current = {
        let mut h = host(&dir);
        assert_eq!(h.request(1, req.clone()).unwrap(), old);
        let response = h.request(2, req.clone()).unwrap();
        assert_eq!(response.status, 409);
        response
    };
    let mut h = host(&dir);
    assert_eq!(h.request(2, req).unwrap(), current);
    drop(h);
    let path = dir.0.join("00000000000000000003.checkpoint");
    let bytes = fs::read(&path).unwrap();
    assert_eq!(&bytes[..8], b"SVHOST02");
    let mut invalid = decode(&bytes).unwrap();
    invalid.receipt.as_mut().unwrap().response = old;
    fs::write(path, encode(&invalid).unwrap()).unwrap();
    assert!(matches!(
        DurableHost::open(DiskJournal::open(&dir.0).unwrap(), 1, vec![], true),
        Err(Error::Invalid)
    ));
}
#[test]
fn disk_restart_receipt_and_next_mutation() {
    let t = Temp::new();
    let response = {
        let mut h = host(&t);
        h.request(1, q(0)).unwrap()
    };
    let mut h = host(&t);
    assert_eq!(h.request(1, q(0)).unwrap(), response);
    assert!(matches!(h.request(1, q(1)), Err(Error::Conflict)));
    assert_eq!(h.request(2, q(1)).unwrap().revision, 2);
    assert_eq!(
        h.checkpoint().unwrap().snapshot.sampling_error,
        vec![255, 0, b'x']
    );
}
#[test]
fn owner_lock_exclusive_and_stale_not_stolen() {
    let t = Temp::new();
    let a = DiskJournal::open(&t.0).unwrap();
    assert!(DiskJournal::open(&t.0).is_err());
    drop(a);
    fs::write(t.0.join("owner.lock"), b"synthetic dead owner").unwrap();
    assert!(DiskJournal::open(&t.0).is_err());
    assert_eq!(
        fs::read(t.0.join("owner.lock")).unwrap(),
        b"synthetic dead owner"
    );
}
#[test]
fn highest_corrupt_rejects_without_fallback() {
    let t = Temp::new();
    {
        let mut h = host(&t);
        h.request(1, q(0)).unwrap();
    }
    fs::write(t.0.join("00000000000000000004.checkpoint"), b"corrupt").unwrap();
    let mut j = DiskJournal::open(&t.0).unwrap();
    assert!(j.load().is_err());
}
#[test]
fn temp_file_not_treated_as_commit_but_not_overwritten() {
    let t = Temp::new();
    {
        let _ = host(&t);
    }
    fs::write(t.0.join("00000000000000000002.tmp"), b"partial").unwrap();
    let mut h = host(&t);
    assert!(matches!(h.request(1, q(0)), Err(Error::Uncertain)));
    assert_eq!(
        fs::read(t.0.join("00000000000000000002.tmp")).unwrap(),
        b"partial"
    );
    assert!(matches!(h.request(2, q(0)), Err(Error::Uncertain)));
}
#[derive(Clone)]
struct FaultJournal {
    disk: Arc<Mutex<Option<DurableEnvelope>>>,
    calls: Arc<Mutex<u64>>,
    fail: u64,
    after: bool,
    panic: bool,
}
impl Journal for FaultJournal {
    fn load(&mut self) -> Result<Option<DurableEnvelope>, Error> {
        Ok(self.disk.lock().unwrap().clone())
    }
    fn publish(&mut self, e: &DurableEnvelope) -> Result<(), Error> {
        *self.calls.lock().unwrap() += 1;
        let fail = *self.calls.lock().unwrap() == self.fail;
        if !fail || self.after {
            *self.disk.lock().unwrap() = Some(e.clone());
        }
        if fail {
            if self.panic {
                panic!("after synthetic publication")
            };
            return Err(Error::Invalid);
        }
        Ok(())
    }
}
#[test]
fn every_transaction_publication_fault_never_replays() {
    for phase in 2..=4 {
        for after in [false, true] {
            for panic in [false, true] {
                let store = Arc::new(Mutex::new(None));
                let calls = Arc::new(Mutex::new(0));
                let j = FaultJournal {
                    disk: store.clone(),
                    calls: calls.clone(),
                    fail: phase,
                    after,
                    panic,
                };
                let mut h = DurableHost::open(j.clone(), 1, vec![], true).unwrap();
                assert!(matches!(h.request(1, q(0)), Err(Error::Uncertain)));
                let before = *calls.lock().unwrap();
                assert!(matches!(h.request(1, q(0)), Err(Error::Uncertain)));
                assert_eq!(*calls.lock().unwrap(), before);
                drop(h);
                let mut good = j;
                good.fail = 0;
                let reopened = DurableHost::open(good, 1, vec![], true);
                if phase == 2 && !after {
                    assert!(reopened.is_ok());
                } else if phase == 4 && after {
                    assert_eq!(reopened.unwrap().request(1, q(0)).unwrap().revision, 1);
                } else {
                    assert!(matches!(reopened, Err(Error::RecoveryRequired)));
                }
            }
        }
    }
}
fn initial() -> DurableEnvelope {
    let s = State::new(Snapshot {
        epoch: 1,
        ..Snapshot::default()
    })
    .unwrap();
    DurableEnvelope {
        sequence: 1,
        checkpoint: s.export().unwrap(),
        highwater: 0,
        intent: None,
        receipt: None,
    }
}
#[test]
fn bounded_codec_roundtrip_truncations_and_checksum() {
    let e = initial();
    let b = encode(&e).unwrap();
    assert_eq!(decode(&b).unwrap(), e);
    for n in 0..b.len() {
        assert!(decode(&b[..n]).is_err());
    }
    for index in [0, 8, 16, b.len() - 1] {
        let mut corrupt = b.clone();
        corrupt[index] ^= 1;
        assert!(decode(&corrupt).is_err());
    }
    let mut trailing = b;
    trailing.push(0);
    assert!(decode(&trailing).is_err());
    let mut bad = e;
    bad.highwater = 3;
    assert!(encode(&bad).is_err());
}
#[test]
fn core_restore_preserves_pending_ack_and_rejects_bad_cp() {
    let mut s = State::new(Snapshot {
        epoch: 1,
        ..Snapshot::default()
    })
    .unwrap();
    let c = Command {
        epoch: 1,
        id: 9,
        kind: 2,
        sampling_error: b"x".to_vec(),
        ..Command::default()
    };
    let a = s.apply(c.clone()).unwrap();
    let cp = s.export().unwrap();
    let mut restored = State::from_checkpoint(cp.clone()).unwrap();
    assert_eq!(restored.export().unwrap(), cp);
    assert!(restored.status(&c).unwrap().pending);
    assert_eq!(restored.apply(c.clone()).unwrap(), a);
    assert!(restored
        .apply(Command {
            id: 10,
            expected_revision: 1,
            ..c
        })
        .is_err());
    restored.acknowledge(a).unwrap();
    assert!(!restored.export().unwrap().pending);
    let mut bad = cp;
    bad.last_ack.id = 7;
    assert!(State::from_checkpoint(bad).is_err());
}
#[test]
fn unresolved_clock_identity_is_rejected() {
    let mut s = State::new(Snapshot {
        epoch: 1,
        ..Snapshot::default()
    })
    .unwrap();
    let a = s
        .apply(Command {
            epoch: 1,
            id: 1,
            kind: 1,
            has_baseline: true,
            time_token: 99,
            ..Command::default()
        })
        .unwrap();
    s.acknowledge(a).unwrap();
    let e = DurableEnvelope {
        sequence: 1,
        checkpoint: s.export().unwrap(),
        highwater: 2,
        intent: None,
        receipt: Some(DurableReceipt {
            legacy_http: false,
            id: 2,
            request: Request::Snapshot,
            response: Response {
                status: 200,
                revision: 1,
                body: b"epoch=1 revision=1\n".to_vec(),
            },
        }),
    };
    let j = FaultJournal {
        disk: Arc::new(Mutex::new(Some(e))),
        calls: Arc::new(Mutex::new(0)),
        fail: 0,
        after: false,
        panic: false,
    };
    assert!(matches!(
        DurableHost::open(j, 1, vec![], true),
        Err(Error::ResourceIdentity)
    ));
}
#[test]
fn executable_process_restart_replays_without_new_publication() {
    use std::io::Write;
    use std::process::{Command, Stdio};
    let t = Temp::new();
    let run = |input: &[u8]| {
        let mut c = Command::new(env!("CARGO_BIN_EXE_smartvpn-host"))
            .arg("--offline-disk")
            .arg(&t.0)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        c.stdin.take().unwrap().write_all(input).unwrap();
        let o = c.wait_with_output().unwrap();
        assert!(o.status.success());
        o.stdout
    };
    let input = wire::encode_request(1, Some(&q(0)));
    let first = run(&input);
    let count = fs::read_dir(&t.0).unwrap().count();
    assert_eq!(first, run(&input));
    assert_eq!(count, fs::read_dir(&t.0).unwrap().count());
}
struct DiskFault {
    inner: DiskJournal,
    fail: u64,
}
impl Journal for DiskFault {
    fn load(&mut self) -> Result<Option<DurableEnvelope>, Error> {
        self.inner.load()
    }
    fn publish(&mut self, e: &DurableEnvelope) -> Result<(), Error> {
        self.inner.publish(e)?;
        if e.sequence == self.fail {
            Err(Error::Invalid)
        } else {
            Ok(())
        }
    }
    fn close(&mut self) -> Result<(), Error> {
        self.inner.close()
    }
}
#[test]
fn real_disk_pending_and_intent_restart_failclosed() {
    for fail in [2, 3] {
        let t = Temp::new();
        {
            let j = DiskFault {
                inner: DiskJournal::open(&t.0).unwrap(),
                fail,
            };
            let mut h = DurableHost::open(j, 1, vec![], true).unwrap();
            assert!(matches!(h.request(1, q(0)), Err(Error::Uncertain)));
        }
        let j = DiskJournal::open(&t.0).unwrap();
        assert!(matches!(
            DurableHost::open(j, 1, vec![], true),
            Err(Error::RecoveryRequired)
        ));
    }
}
#[test]
fn explicit_close_releases_only_owned_lock() {
    let t = Temp::new();
    let mut h = host(&t);
    h.close().unwrap();
    h.close().unwrap();
    assert!(DiskJournal::open(&t.0).is_ok());
    assert!(matches!(h.request(1, q(0)), Err(Error::Closed)));
}
#[test]
fn initial_publish_panic_is_explicit_uncertain() {
    let j = FaultJournal {
        disk: Arc::new(Mutex::new(None)),
        calls: Arc::new(Mutex::new(0)),
        fail: 1,
        after: true,
        panic: true,
    };
    assert!(matches!(
        DurableHost::open(j, 1, vec![], true),
        Err(Error::Uncertain)
    ));
}
#[test]
fn final_receipt_tampering_is_rejected() {
    let t = Temp::new();
    {
        let mut h = host(&t);
        h.request(1, q(0)).unwrap();
    }
    let mut j = DiskJournal::open(&t.0).unwrap();
    let mut e = j.load().unwrap().unwrap();
    e.receipt.as_mut().unwrap().request = q(1);
    assert!(encode(&e).is_err());
}
