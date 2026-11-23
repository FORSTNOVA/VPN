use smartvpn_core::{
    registry_flow::{Entry, Reply, Request, Snapshot},
    registry_flow_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn int(b: &mut Vec<u8>, n: i64) {
    b.extend_from_slice(&n.to_le_bytes());
}
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    int(b, s.len() as i64);
    b.extend_from_slice(s);
}
fn entries(b: &mut Vec<u8>, es: &[Entry]) {
    int(b, es.len() as i64);
    for e in es {
        bytes(b, &e.id);
        bytes(b, &e.url);
        bytes(b, &e.label);
        b.extend_from_slice(&[e.enabled as u8, e.has_enabled as u8]);
    }
}
fn snapshot(b: &mut Vec<u8>, s: &Snapshot) {
    entries(b, &s.entries);
    bytes(b, &s.active_url);
    b.extend_from_slice(&[s.from_cache as u8, s.store as u8]);
}
fn start_frame(s: &Snapshot, r: &Request) -> Vec<u8> {
    let mut b = vec![];
    snapshot(&mut b, s);
    b.push(r.op);
    for v in [
        &r.id,
        &r.url,
        &r.label,
        &r.prefix,
        &r.hash,
        &r.default_label,
    ] {
        bytes(&mut b, v);
    }
    b.extend_from_slice(&r.presence.to_le_bytes());
    b.extend_from_slice(&[r.enabled as u8, r.valid as u8, r.has_info as u8]);
    for n in [r.hours, r.node_count, r.label_runes] {
        int(&mut b, n);
    }
    b
}
fn reply_frame(r: &Reply) -> Vec<u8> {
    let mut b = vec![r.ok as u8];
    int(&mut b, r.value);
    b.push(r.flag as u8);
    bytes(&mut b, &r.data);
    entries(&mut b, &r.entries);
    b.push(r.snapshot.is_some() as u8);
    if let Some(s) = &r.snapshot {
        snapshot(&mut b, s);
    }
    b
}
fn entry(id: &[u8], url: &[u8]) -> Entry {
    Entry {
        id: id.to_vec(),
        url: url.to_vec(),
        label: id.to_vec(),
        enabled: true,
        has_enabled: true,
    }
}
fn state() -> Snapshot {
    Snapshot {
        entries: vec![entry(b"a", b"ua"), entry(b"b", b"ub")],
        active_url: b"ua".to_vec(),
        store: true,
        ..Snapshot::default()
    }
}
fn request(op: u8) -> Request {
    Request {
        op,
        id: b"a".to_vec(),
        valid: true,
        ..Request::default()
    }
}
#[derive(Debug, PartialEq, Eq)]
struct Frame {
    kind: u8,
    index: i64,
    aux: i64,
    data: Vec<u8>,
    raw: Vec<u8>,
}
struct Session(u64);
impl Session {
    fn start(s: &Snapshot, r: &Request) -> Self {
        let b = start_frame(s, r);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_registry_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Frame) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_registry_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step)
            },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_registry_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        let length = i64::from_le_bytes(b[17..25].try_into().unwrap()) as usize;
        (
            step,
            Frame {
                kind: b[0],
                index: i64::from_le_bytes(b[1..9].try_into().unwrap()),
                aux: i64::from_le_bytes(b[9..17].try_into().unwrap()),
                data: b[25..25 + length].to_vec(),
                raw: b,
            },
        )
    }
    fn advance_raw(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_registry_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
    fn advance(&self, step: u64, r: &Reply) -> i32 {
        self.advance_raw(step, &reply_frame(r))
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_registry_flow_close_v1(self.0), 0);
        }
    }
}
fn trace(s: &Snapshot, r: &Request, mut answer: impl FnMut(&Frame) -> Reply) -> Vec<Frame> {
    let session = Session::start(s, r);
    let mut t = vec![];
    for _ in 0..100 {
        let (step, e) = session.next();
        if e.kind == 0 {
            return t;
        }
        let reply = answer(&e);
        t.push(e);
        assert_eq!(session.advance(step, &reply), 0);
    }
    panic!("nonterminating")
}
fn ack() -> Reply {
    Reply {
        ok: true,
        ..Reply::default()
    }
}
#[test]
fn lifecycle_repeated_reads_stale_steps_invalid_reply_and_capacity() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_registry_flow_version_v1(), 1);
    let mut req = request(13);
    req.id = b"__merged__".to_vec();
    let mut session = Session::start(&state(), &req);
    let first = session.next();
    for _ in 0..5 {
        assert_eq!(session.next(), first);
    }
    assert_eq!(session.advance_raw(1, &[]), -2);
    assert_eq!(session.next(), first);
    assert_eq!(session.advance(0, &ack()), -5);
    assert_eq!(session.advance(1, &ack()), 0);
    assert_eq!(session.advance(1, &ack()), -5);
    let pending = session.next();
    assert_eq!(pending.1.kind, 10);
    assert_eq!(session.advance(2, &ack()), -2);
    assert_eq!(session.next(), pending);
    let r = Reply {
        snapshot: Some(state()),
        ..ack()
    };
    assert_eq!(session.advance(2, &r), 0);
    let old = session.0;
    assert_eq!(smartvpn_registry_flow_close_v1(old), 0);
    session.0 = 0;
    assert_eq!(smartvpn_registry_flow_close_v1(old), -2);
    assert_eq!(
        unsafe { smartvpn_registry_flow_advance_v1(old, 3, [0].as_ptr(), 1) },
        -2
    );
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_registry_flow_next_v1(old, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
    let mut sessions: Vec<_> = (0..8)
        .map(|_| Session::start(&state(), &request(1)))
        .collect();
    let b = start_frame(&state(), &request(1));
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_registry_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    drop(sessions.pop());
    let fresh = Session::start(&state(), &request(1));
    assert_ne!(fresh.0, old);
}
#[test]
fn malformed_and_size_limits_reject_without_consuming() {
    let _g = SERIAL.lock().unwrap();
    let b = start_frame(&state(), &request(1));
    for v in [
        vec![],
        b[..b.len() - 1].to_vec(),
        {
            let mut v = b.clone();
            v.push(0);
            v
        },
        {
            let mut v = b.clone();
            v[..8].copy_from_slice(&100001i64.to_le_bytes());
            v
        },
        {
            let mut v = b.clone();
            v[..8].copy_from_slice(&(-1i64).to_le_bytes());
            v
        },
    ] {
        let mut id = 9;
        assert_eq!(
            unsafe { smartvpn_registry_flow_start_v1(v.as_ptr(), v.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    let mut id = 9;
    assert_eq!(
        unsafe { smartvpn_registry_flow_start_v1(b.as_ptr(), (8 << 20) + 1, &mut id) },
        -1
    );
    assert_eq!(id, 0);
    let session = Session::start(&state(), &request(1));
    let before = session.next();
    assert_eq!(
        unsafe {
            smartvpn_registry_flow_advance_v1(session.0, before.0, b.as_ptr(), (8 << 20) + 1)
        },
        -1
    );
    assert_eq!(session.next(), before);
    let mut r = reply_frame(&ack());
    r[0] = 2;
    assert_eq!(session.advance_raw(before.0, &r), -2);
    assert_eq!(session.next(), before);
}
#[test]
fn load_and_save_dynamic_secret_failure_branches() {
    let _g = SERIAL.lock().unwrap();
    let mut e = entry(b"", b"cipher");
    e.has_enabled = false;
    e.enabled = false;
    let loaded = vec![entry(b"empty", b""), entry(b"bad", b"bad"), e];
    let t = trace(&state(), &request(0), |e| {
        let mut r = ack();
        match e.kind {
            1 => r.entries = loaded.clone(),
            2 => {
                r.ok = e.data != b"bad";
                r.data = b"plain\x00\xff".to_vec();
            }
            3 => r.entries = vec![entry(b"generated", b"unused")],
            _ => {}
        }
        r
    });
    assert_eq!(
        t.iter().map(|e| e.kind).collect::<Vec<_>>(),
        vec![32, 1, 2, 2, 3, 33, 20, 63]
    );
    let appended = t.iter().find(|e| e.kind == 33).unwrap();
    assert!(appended.raw.ends_with(&[1, 0]));
    let t = trace(&state(), &request(1), |e| Reply {
        ok: e.kind != 4,
        ..Reply::default()
    });
    assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![4, 63]);
    assert_eq!(t.last().unwrap().index, 22);
    assert_eq!(t.last().unwrap().aux, 500);
}
#[test]
fn activate_cache_and_delete_partial_rollback_cross_ffi() {
    let _g = SERIAL.lock().unwrap();
    for cache_ok in [false, true] {
        let mut req = request(13);
        req.id = b"b".to_vec();
        let mut actual = state();
        actual.active_url = b"ub".to_vec();
        let t = trace(&state(), &req, |e| {
            let mut r = ack();
            match e.kind {
                8 => {
                    r.ok = false;
                    r.data = b"fetch error".to_vec();
                    r.snapshot = Some(actual.clone());
                }
                9 => {
                    r.ok = cache_ok;
                    r.value = 7;
                    r.data = b"cache error".to_vec();
                    r.snapshot = Some(actual.clone());
                }
                19 => r.value = 123,
                _ => {}
            }
            r
        });
        assert_eq!(t.last().unwrap().index, if cache_ok { 26 } else { 14 });
        if !cache_ok {
            assert_eq!(t.last().unwrap().data, b"fetch error");
        } else {
            assert_eq!(t.last().unwrap().data, 7i64.to_le_bytes());
        }
    }
    let mut s = state();
    s.active_url = b"__merged__".to_vec();
    let t = trace(&s, &request(12), |e| {
        let mut r = ack();
        if e.kind == 11 {
            let mut partial = s.clone();
            partial.entries.remove(0);
            partial.from_cache = true;
            r.ok = false;
            r.data = b"merge error".to_vec();
            r.snapshot = Some(partial);
        }
        r
    });
    assert_eq!(
        t.iter().map(|e| e.kind).collect::<Vec<_>>(),
        vec![6, 36, 11, 35, 4, 4, 5, 63]
    );
    assert_eq!(t.last().unwrap().index, 13);
    assert_eq!(t.last().unwrap().aux, 502);
}
#[test]
fn record_timestamp_keeps_full_signed_integer_precision() {
    let _g = SERIAL.lock().unwrap();
    for timestamp in [i64::MIN, (1i64 << 53) + 7, i64::MAX] {
        let t = trace(&state(), &request(9), |e| Reply {
            value: if e.kind == 19 { timestamp } else { 0 },
            ..ack()
        });
        let record = t.iter().find(|e| e.kind == 40).unwrap();
        assert_eq!(record.aux, timestamp);
    }
}
