use smartvpn_core::{
    service_state::{validate_checkpoint, Ack, Checkpoint, Command, Snapshot},
    service_state_v3_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
const CAP: usize = 4096;
fn n(b: &mut Vec<u8>, v: u64) {
    b.extend_from_slice(&v.to_le_bytes())
}
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    n(b, v.len() as u64);
    b.extend_from_slice(v)
}
fn command(c: &Command) -> Vec<u8> {
    let mut b = vec![];
    for v in [c.epoch, c.id, c.expected_revision] {
        n(&mut b, v)
    }
    b.push(c.kind);
    b.push(c.has_baseline as u8);
    for v in [c.time_token, c.up as u64, c.down as u64] {
        n(&mut b, v)
    }
    bytes(&mut b, &c.sampling_error);
    bytes(&mut b, &c.path_payload);
    bytes(&mut b, &c.health_payload);
    b
}
fn ack(a: Ack) -> Vec<u8> {
    let mut b = vec![];
    for v in [a.epoch, a.id, a.revision] {
        n(&mut b, v)
    }
    b
}
struct Reader<'a>(&'a [u8], usize);
impl Reader<'_> {
    fn u(&mut self) -> u64 {
        let p = self.1;
        self.1 += 8;
        u64::from_le_bytes(self.0[p..p + 8].try_into().unwrap())
    }
    fn flag(&mut self) -> bool {
        let p = self.1;
        self.1 += 1;
        self.0[p] != 0
    }
    fn bytes(&mut self) -> Vec<u8> {
        let n = self.u() as usize;
        let p = self.1;
        self.1 += n;
        self.0[p..p + n].to_vec()
    }
    fn snapshot(&mut self) -> Snapshot {
        Snapshot {
            epoch: self.u(),
            revision: self.u(),
            has_baseline: self.flag(),
            time_token: self.u(),
            up: self.u() as i64,
            down: self.u() as i64,
            sampling_error: self.bytes(),
            path_payload: self.bytes(),
            health_payload: self.bytes(),
            control_payload: vec![],
            catalog_payload: vec![],
            china_payload: vec![],
        }
    }
    fn cmd(&mut self) -> Command {
        let epoch = self.u();
        let id = self.u();
        let expected_revision = self.u();
        let p = self.1;
        self.1 += 1;
        Command {
            epoch,
            id,
            expected_revision,
            kind: self.0[p],
            has_baseline: self.flag(),
            time_token: self.u(),
            up: self.u() as i64,
            down: self.u() as i64,
            sampling_error: self.bytes(),
            path_payload: self.bytes(),
            health_payload: self.bytes(),
            control_payload: vec![],
            catalog_payload: vec![],
            china_payload: vec![],
        }
    }
    fn ack(&mut self) -> Ack {
        Ack {
            epoch: self.u(),
            id: self.u(),
            revision: self.u(),
        }
    }
}
struct Session {
    id: u64,
    out: Vec<u8>,
}
impl Session {
    fn new() -> Self {
        let mut id = 0;
        assert_eq!(unsafe { smartvpn_service_state_open_v3(&mut id) }, 0);
        Self { id, out: vec![] }
    }
    fn call(&mut self, op: u8, b: &[u8]) -> Result<Vec<u8>, i32> {
        self.out.resize(CAP.max(self.out.len()), 0);
        let mut n = 0;
        let code = unsafe {
            smartvpn_service_state_call_v3(
                self.id,
                op,
                b.as_ptr(),
                b.len(),
                self.out.as_mut_ptr(),
                self.out.len(),
                &mut n,
            )
        };
        if code == -6 {
            assert!(matches!(op, 1 | 2 | 4 | 5 | 7 | 8));
            assert!(n <= 64 << 20);
            self.out.resize(n, 0);
            let code = unsafe {
                smartvpn_service_state_call_v3(
                    self.id,
                    op,
                    b.as_ptr(),
                    b.len(),
                    self.out.as_mut_ptr(),
                    self.out.len(),
                    &mut n,
                )
            };
            assert_eq!(code, 0);
            return Ok(self.out[..n].to_vec());
        }
        if code == 0 {
            Ok(self.out[..n].to_vec())
        } else {
            assert_eq!(n, 0);
            Err(code)
        }
    }
    fn domain(&mut self, c: smartvpn_core::path_state::Command) -> Ack {
        let revision = self.read().revision;
        let c = Command {
            epoch: self.id,
            id: revision + 1,
            expected_revision: revision,
            kind: 3,
            path_payload: smartvpn_core::path_state::encode_command(&c),
            ..Command::default()
        };
        let a = self.apply(&c);
        self.call(6, &ack(a)).unwrap();
        a
    }
    fn path(&mut self) -> smartvpn_core::path_state::PathState {
        smartvpn_core::path_state::decode(&self.read().path_payload).unwrap()
    }
    fn read(&mut self) -> Snapshot {
        let b = self.call(1, &[]).unwrap();
        let mut r = Reader(&b, 0);
        let s = r.snapshot();
        assert_eq!(r.1, b.len());
        s
    }
    fn apply(&mut self, c: &Command) -> Ack {
        let b = self.call(3, &command(c)).unwrap();
        let mut r = Reader(&b, 0);
        let a = r.ack();
        assert_eq!(r.1, b.len());
        a
    }
    fn status(&mut self, c: &Command) -> (bool, bool) {
        let b = self.call(4, &command(c)).unwrap();
        let mut r = Reader(&b, 0);
        let found = r.flag();
        let pending = if found {
            assert_eq!(r.cmd(), *c);
            assert_eq!(
                r.ack(),
                Ack {
                    epoch: c.epoch,
                    id: c.id,
                    revision: c.expected_revision + 1
                }
            );
            r.flag()
        } else {
            false
        };
        assert_eq!(r.1, b.len());
        (found, pending)
    }
    fn export(&mut self) -> Checkpoint {
        let b = self.call(5, &[]).unwrap();
        let mut r = Reader(&b, 0);
        let snapshot = r.snapshot();
        let has_last = r.flag();
        let (last_command, last_ack) = if has_last {
            (r.cmd(), r.ack())
        } else {
            (Command::default(), Ack::default())
        };
        let pending = r.flag();
        assert_eq!(r.1, b.len());
        let c = Checkpoint {
            snapshot,
            has_last,
            last_command,
            last_ack,
            pending,
        };
        validate_checkpoint(&c).unwrap();
        c
    }
    fn baseline(&self, id: u64) -> Command {
        Command {
            epoch: self.id,
            id,
            expected_revision: id - 1,
            kind: 1,
            has_baseline: true,
            time_token: id,
            up: 100,
            down: 200,
            ..Command::default()
        }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.id != 0 {
            assert_eq!(smartvpn_service_state_close_v3(self.id), 0)
        }
    }
}

use smartvpn_core::path_state::{self, Command as PathCommand, Config, Sample, SiteResult, Target};
#[test]
fn dynamic_queries_and_preflight_mutation_capacity() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    assert_eq!(smartvpn_service_state_version_v3(), 3);
    assert_eq!(smartvpn_service_state_capabilities_v3(), 7);
    let before = s.export();
    for op in [1, 5] {
        for _ in 0..3 {
            let mut n = 0;
            assert_eq!(
                unsafe {
                    smartvpn_service_state_call_v3(
                        s.id,
                        op,
                        ptr::null(),
                        0,
                        ptr::null_mut(),
                        0,
                        &mut n,
                    )
                },
                -6
            );
            assert!(n > 0);
            assert_eq!(s.export(), before)
        }
    }
    let c = s.baseline(1);
    let b = command(&c);
    let mut small = [0u8; 23];
    let mut n = 99;
    assert_eq!(
        unsafe {
            smartvpn_service_state_call_v3(
                s.id,
                3,
                b.as_ptr(),
                b.len(),
                small.as_mut_ptr(),
                23,
                &mut n,
            )
        },
        -1
    );
    assert_eq!(n, 0);
    assert_eq!(s.export(), before);
    let mut enough = [0u8; 24];
    assert_eq!(
        unsafe {
            smartvpn_service_state_call_v3(
                s.id,
                3,
                b.as_ptr(),
                b.len(),
                enough.as_mut_ptr(),
                24,
                &mut n,
            )
        },
        0
    );
    assert_eq!(n, 24);
    let a = Reader(&enough, 0).ack();
    let a = ack(a);
    assert_eq!(
        unsafe {
            smartvpn_service_state_call_v3(s.id, 6, a.as_ptr(), a.len(), ptr::null_mut(), 0, &mut n)
        },
        0
    );
    assert_eq!(n, 0);
    assert_eq!(s.status(&c), (true, false));
}
#[test]
fn all_path_actions_share_traffic_revision_and_survive_generation_change() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    let c = s.baseline(1);
    let a = s.apply(&c);
    s.call(6, &ack(a)).unwrap();
    let config = Config {
        enabled: true,
        interval: -7,
        targets: Some(vec![Target {
            label: vec![255, 0],
            url: b"https://example.invalid/".to_vec(),
            enabled: true,
            ..Target::default()
        }]),
    };
    s.domain(PathCommand {
        action: 1,
        config: config.clone(),
        ..PathCommand::default()
    });
    s.domain(PathCommand {
        action: 2,
        ..PathCommand::default()
    });
    s.domain(PathCommand {
        action: 4,
        ..PathCommand::default()
    });
    let lease = s.path().lease_id;
    assert!(s.path().busy);
    s.domain(PathCommand {
        action: 3,
        ..PathCommand::default()
    });
    assert_eq!(s.path().lease_id, lease);
    s.domain(PathCommand {
        action: 2,
        ..PathCommand::default()
    });
    let generation = s.path().generation;
    for action in [6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21] {
        let cmd = PathCommand {
            action,
            flag: true,
            text: b"x".to_vec(),
            time_token: 88,
            generation,
            strings: Some(vec![]),
            results: Some(vec![SiteResult {
                name: b"site".to_vec(),
                state: b"ok".to_vec(),
                http_code: 403,
                ..SiteResult::default()
            }]),
            samples: Some(vec![Sample {
                id: b"connection".to_vec(),
                upload: -1,
            }]),
            ..PathCommand::default()
        };
        s.domain(cmd);
    }
    let p = s.path();
    assert_eq!(p.config, config);
    assert_eq!(p.at_token, 88);
    assert_eq!(p.status.results.as_ref().unwrap()[0].http_code, 403);
    assert_eq!(p.status.failures, Some(vec![]));
    assert_eq!(p.previous.as_ref().unwrap()[0].upload, -1);
    assert_eq!(p.throttle.as_ref().unwrap()[0].time_token, 88);
    assert_eq!(p.status.alert_id, 1);
    s.domain(PathCommand {
        action: 5,
        lease_id: lease,
        flag: false,
        ..PathCommand::default()
    });
    let p = s.path();
    assert!(!p.busy);
    assert_eq!(p.lease_id, 0);
    assert!(p.status.running);
    assert_eq!(s.read().up, 100);
    assert_eq!(s.read().down, 200);
    assert_eq!(s.export().snapshot.revision, 23);
}
#[test]
fn pending_identity_failed_domain_and_full_checkpoint() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    let payload = path_state::encode_command(&PathCommand {
        action: 2,
        ..PathCommand::default()
    });
    let c = Command {
        epoch: s.id,
        id: 1,
        kind: 3,
        path_payload: payload,
        ..Command::default()
    };
    let a = s.apply(&c);
    assert_eq!(s.status(&c), (true, true));
    assert_eq!(s.apply(&c), a);
    let cp = s.export();
    assert!(cp.pending);
    assert_eq!(cp.last_command, c);
    let mut changed = c.clone();
    changed.path_payload = path_state::encode_command(&PathCommand {
        action: 2,
        text: b"unused but bound".to_vec(),
        ..PathCommand::default()
    });
    assert_eq!(s.call(3, &command(&changed)), Err(-4));
    s.call(6, &ack(a)).unwrap();
    let invalid = Command {
        epoch: s.id,
        id: 2,
        expected_revision: 1,
        kind: 3,
        path_payload: path_state::encode_command(&PathCommand {
            action: 20,
            generation: 99,
            ..PathCommand::default()
        }),
        ..Command::default()
    };
    let before = s.export();
    assert_eq!(s.call(3, &command(&invalid)), Err(-4));
    assert_eq!(s.export(), before);
}
#[test]
fn large_state_read_resize_and_instance_isolation_closed_tombstone() {
    let _g = SERIAL.lock().unwrap();
    let mut a = Session::new();
    let mut b = Session::new();
    a.domain(PathCommand {
        action: 1,
        config: Config {
            targets: Some(vec![Target {
                label: vec![b'L'; 6000],
                ..Target::default()
            }]),
            ..Config::default()
        },
        ..PathCommand::default()
    });
    assert!(a.call(5, &[]).unwrap().len() > 4096);
    assert!(b.path().config.targets.is_none());
    assert_eq!(b.read().revision, 0);
    let old = a.id;
    assert_eq!(smartvpn_service_state_close_v3(old), 0);
    assert_eq!(a.call(1, &[]), Err(-3));
    a.id = 0;
    assert_eq!(smartvpn_service_state_close_v3(old), -3);
    assert_eq!(b.read().revision, 0);
}

#[test]
fn meta_is_explicitly_narrow_while_read_and_export_are_complete() {
    let _guard = SERIAL.lock().unwrap();
    let mut session = Session::new();
    let full = session.read();
    assert!(!full.health_payload.is_empty());
    let bytes = session.call(7, &[]).unwrap();
    let mut reader = Reader(&bytes, 0);
    let meta = reader.snapshot();
    assert_eq!(reader.1, bytes.len());
    assert!(meta.health_payload.is_empty());
    let mut expected = full.clone();
    expected.health_payload.clear();
    assert_eq!(meta, expected);
    let bytes = session.call(5, &[]).unwrap();
    let mut reader = Reader(&bytes, 0);
    assert_eq!(reader.snapshot(), full);
    // Malformed query must not change any shared revision or authoritative data.
    assert!(session.call(8, &[255]).is_err());
    assert_eq!(session.read(), full);
}

fn health_apply(session: &mut Session, h: smartvpn_core::health_state::Command) -> Ack {
    let meta = session.call(7, &[]).unwrap();
    let current = Reader(&meta, 0).snapshot();
    let c = Command {
        epoch: current.epoch,
        id: current.revision + 1,
        expected_revision: current.revision,
        kind: 4,
        health_payload: smartvpn_core::health_state::encode_command(&h).unwrap(),
        ..Command::default()
    };
    let a = session.apply(&c);
    session.call(6, &ack(a)).unwrap();
    a
}
#[test]
fn health_commands_share_global_ack_with_path_and_traffic() {
    use smartvpn_core::health_state::{self as h, Command as H};
    let _guard = SERIAL.lock().unwrap();
    let mut session = Session::new();
    let actions = [
        H {
            action: 1,
            params: h::Params {
                values: [-1, 60, 3, 30, 20, 2],
            },
            ..H::default()
        },
        H {
            action: 2,
            health: Some(Default::default()),
            ..H::default()
        },
        H {
            action: 3,
            regions: Some(Default::default()),
            ..H::default()
        },
        H {
            action: 4,
            key: b"map-key".to_vec(),
            health_record: h::HealthRecord {
                name: b"different-name".to_vec(),
                state: vec![255],
                failures: -3,
                latency: -7,
                last_success: 101,
                cooldown: 102,
                updated: 103,
                ..Default::default()
            },
            ..H::default()
        },
        H {
            action: 5,
            key: b"region-key".to_vec(),
            region_record: h::RegionRecord {
                name: b"another-name".to_vec(),
                country: b"JP".to_vec(),
                sources: Some(vec![]),
                verified: 104,
                ..Default::default()
            },
            ..H::default()
        },
        H {
            action: 8,
            flag: true,
            ..H::default()
        },
        H {
            action: 9,
            text: vec![254],
            ..H::default()
        },
        H {
            action: 10,
            text: b"batch failed but independent HTTPS passed".to_vec(),
            ..H::default()
        },
        H {
            action: 11,
            time_token: 105,
            ..H::default()
        },
        H {
            action: 12,
            job: Some(h::Job {
                running: true,
                last_error: b"old error".to_vec(),
                ..Default::default()
            }),
            ..H::default()
        },
        H {
            action: 13,
            count: 9,
            ..H::default()
        },
        H {
            action: 14,
            text: b"verified".to_vec(),
            ..H::default()
        },
        H {
            action: 15,
            text: b"finished".to_vec(),
            flag: false,
            ..H::default()
        },
    ];
    for (i, c) in actions.into_iter().enumerate() {
        assert_eq!(health_apply(&mut session, c).revision, i as u64 + 1);
    }
    let query = h::encode_query(1, b"map-key").unwrap();
    let reply = h::decode_reply(&session.call(8, &query).unwrap()).unwrap();
    assert!(reply.found);
    assert_eq!(reply.health_record.name, b"different-name");
    assert_eq!(reply.health_record.latency, -7);
    assert_eq!(reply.health_record.cooldown, 102);
    let query = h::encode_query(2, b"region-key").unwrap();
    let reply = h::decode_reply(&session.call(8, &query).unwrap()).unwrap();
    assert!(reply.found);
    assert_eq!(reply.region_record.sources, Some(vec![]));
    session.domain(smartvpn_core::path_state::Command {
        action: 8,
        text: b"path preserved".to_vec(),
        ..Default::default()
    });
    let current = session.read();
    let c = Command {
        epoch: current.epoch,
        id: current.revision + 1,
        expected_revision: current.revision,
        kind: 2,
        sampling_error: vec![253],
        ..Default::default()
    };
    let a = session.apply(&c);
    session.call(6, &ack(a)).unwrap();
    assert_eq!(session.read().revision, 15);
    assert_eq!(session.read().sampling_error, vec![253]);
    // Clear affects records only; neither existing path nor traffic is reset.
    health_apply(
        &mut session,
        H {
            action: 6,
            ..H::default()
        },
    );
    health_apply(
        &mut session,
        H {
            action: 7,
            ..H::default()
        },
    );
    assert_eq!(session.read().revision, 17);
    let query = h::encode_query(1, b"map-key").unwrap();
    assert!(
        !h::decode_reply(&session.call(8, &query).unwrap())
            .unwrap()
            .found
    );
    assert_eq!(session.path().status.note, b"path preserved");
    assert_eq!(session.read().sampling_error, vec![253]);
}

#[test]
fn health_pending_full_payload_identity_and_checkpoint_binding() {
    use smartvpn_core::health_state::{self as h, Command as H};
    let _guard = SERIAL.lock().unwrap();
    let mut session = Session::new();
    let c = Command {
        epoch: session.id,
        id: 1,
        expected_revision: 0,
        kind: 4,
        health_payload: h::encode_command(&H {
            action: 10,
            text: b"protected traffic retained".to_vec(),
            ..H::default()
        })
        .unwrap(),
        ..Command::default()
    };
    let a = session.apply(&c);
    assert_eq!(session.apply(&c), a);
    let cp = session.export();
    assert!(cp.pending);
    assert_eq!(cp.last_command, c);
    let mut changed = c.clone();
    changed.health_payload = h::encode_command(&H {
        action: 10,
        text: b"different".to_vec(),
        ..H::default()
    })
    .unwrap();
    assert!(session.call(3, &command(&changed)).is_err());
    let next = Command {
        epoch: session.id,
        id: 2,
        expected_revision: 1,
        kind: 2,
        sampling_error: b"next".to_vec(),
        ..Command::default()
    };
    assert!(session.call(3, &command(&next)).is_err());
    let mut corrupted = cp.clone();
    corrupted.last_command = changed;
    assert!(validate_checkpoint(&corrupted).is_err());
    assert_eq!(session.export(), cp);
    session.call(6, &ack(a)).unwrap();
    assert!(!session.export().pending);
    let next_ack = session.apply(&next);
    session.call(6, &ack(next_ack)).unwrap();
    assert_eq!(session.read().revision, 2);
    assert_eq!(session.read().health_payload, cp.snapshot.health_payload);
}

#[test]
fn large_health_table_narrow_reads_and_updates_keep_fixed_reply_size() {
    use smartvpn_core::health_state::{self as h, Command as H};
    let _guard = SERIAL.lock().unwrap();
    let mut session = Session::new();
    let table = (0..100000)
        .map(|i| {
            (
                format!("node-{i:06}").into_bytes(),
                h::HealthRecord::default(),
            )
        })
        .collect();
    health_apply(
        &mut session,
        H {
            action: 2,
            health: Some(table),
            ..H::default()
        },
    );
    let full_len = session.read().health_payload.len();
    assert!(full_len > 4 * 1024 * 1024);
    let meta_len = session.call(7, &[]).unwrap().len();
    assert!(meta_len < 4096);
    for i in 0..256 {
        let key = format!("node-{i:06}").into_bytes();
        health_apply(
            &mut session,
            H {
                action: 4,
                key: key.clone(),
                health_record: h::HealthRecord {
                    latency: i,
                    updated: i as u64 + 1,
                    ..Default::default()
                },
                ..H::default()
            },
        );
        let raw = session.call(8, &h::encode_query(1, &key).unwrap()).unwrap();
        assert!(raw.len() < 4096);
        let reply = h::decode_reply(&raw).unwrap();
        assert!(reply.found);
        assert_eq!(reply.health_record.latency, i);
        assert_eq!(session.call(7, &[]).unwrap().len(), meta_len);
    }
    assert_eq!(session.read().health_payload.len(), full_len);
    assert_eq!(session.export().snapshot.revision, 257);
}

#[test]
fn cumulative_health_budget_rejects_before_revision_or_state_changes() {
    use smartvpn_core::health_state::{self as h, Command as H};
    let _guard = SERIAL.lock().unwrap();
    let mut session = Session::new();
    health_apply(
        &mut session,
        H {
            action: 10,
            text: vec![b'x'; 9 << 20],
            ..H::default()
        },
    );
    let before = session.read();
    let c = Command {
        epoch: session.id,
        id: 2,
        expected_revision: 1,
        kind: 4,
        health_payload: h::encode_command(&H {
            action: 9,
            text: vec![b'y'; 8 << 20],
            ..H::default()
        })
        .unwrap(),
        ..Command::default()
    };
    assert!(session.call(3, &command(&c)).is_err());
    assert_eq!(session.read(), before);
    assert_eq!(session.export().snapshot, before);
    let mut second = Session::new();
    assert_eq!(second.read().revision, 0);
    assert!(second.read().health_payload.len() < 4096);
}
