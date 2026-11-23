use smartvpn_core::{china_flow::Reply, china_flow_ffi::*};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    b.extend_from_slice(&(v.len() as u64).to_le_bytes());
    b.extend_from_slice(v)
}
fn list(b: &mut Vec<u8>, v: &[Vec<u8>]) {
    b.extend_from_slice(&(v.len() as u64).to_le_bytes());
    for x in v {
        bytes(b, x)
    }
}
fn start(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    b.extend_from_slice(&0i64.to_le_bytes());
    b.extend_from_slice(&0i64.to_le_bytes());
    list(&mut b, &[]);
    b
}
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![r.kind, r.ok as u8, r.flag as u8];
    b.extend_from_slice(&r.value.to_le_bytes());
    b.extend_from_slice(&r.token.to_le_bytes());
    bytes(&mut b, &r.text);
    bytes(&mut b, &r.error);
    list(&mut b, &r.names);
    list(&mut b, &r.ranges);
    let s = r.state;
    b.push(s.status.available as u8);
    b.extend_from_slice(&s.status.entries.to_le_bytes());
    for v in [&s.status.updated_at, &s.status.source, &s.status.error] {
        bytes(&mut b, v)
    }
    b.extend([
        s.fetching as u8,
        s.probed as u8,
        s.rule_ok as u8,
        s.active as u8,
        r.tun as u8,
        r.domestic as u8,
        r.import as u8,
    ]);
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let b = start(op);
        let mut h = 0;
        assert_eq!(
            unsafe { smartvpn_china_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        Self(h)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_china_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_china_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn advance(&self, step: u64, r: Reply) -> i32 {
        let b = wire(r);
        unsafe { smartvpn_china_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_china_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn repeated_next_wrong_reply_and_stale_step_do_not_repeat_effect() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_china_flow_version_v1(), 1);
    let s = Session::new(5);
    let a = s.next();
    assert_eq!(a, s.next());
    assert_eq!(a.1[0], 16);
    assert_ne!(
        s.advance(
            a.0,
            Reply {
                kind: 17,
                ok: true,
                ..Default::default()
            }
        ),
        0
    );
    assert_eq!(a, s.next());
    assert_eq!(
        s.advance(
            a.0,
            Reply {
                kind: 16,
                ok: true,
                ..Default::default()
            }
        ),
        0
    );
    let b = s.next();
    assert_eq!(b.1[0], 17);
    assert_ne!(
        s.advance(
            a.0,
            Reply {
                kind: 16,
                ok: true,
                ..Default::default()
            }
        ),
        0
    );
    assert_eq!(b, s.next());
}
#[test]
fn sixty_four_session_cap_release_reuse_and_stale_handle() {
    let _g = SERIAL.lock().unwrap();
    let mut ss = (0..64).map(|_| Session::new(9)).collect::<Vec<_>>();
    let b = start(9);
    let mut h = 999;
    assert_eq!(
        unsafe { smartvpn_china_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -4
    );
    assert_eq!(h, 0);
    let old = ss.pop().unwrap();
    let id = old.0;
    drop(old);
    let replacement = Session::new(9);
    assert_ne!(id, replacement.0);
    assert_ne!(smartvpn_china_flow_close_v1(id), 0);
}
#[test]
fn invalid_wire_limits_and_booleans_leave_session_unchanged() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new(5);
    let before = s.next();
    let mut bad = wire(Reply {
        kind: 16,
        ..Default::default()
    });
    bad[1] = 2;
    assert_ne!(
        unsafe { smartvpn_china_flow_advance_v1(s.0, before.0, bad.as_ptr(), bad.len()) },
        0
    );
    assert_eq!(s.next(), before);
    let huge = vec![0; (8 << 20) + 1];
    let mut h = 7;
    assert_ne!(
        unsafe { smartvpn_china_flow_start_v1(huge.as_ptr(), huge.len(), &mut h) },
        0
    );
    assert_eq!(h, 0);
    let mut bad = start(9);
    bad.extend([0]);
    assert_ne!(
        unsafe { smartvpn_china_flow_start_v1(bad.as_ptr(), bad.len(), &mut h) },
        0
    );
}
#[test]
fn actual_probe_failure_cleanup_is_once_and_ordered() {
    let _g = SERIAL.lock().unwrap();
    for fail in [16, 17, 18, 19, 20] {
        let s = Session::new(5);
        let mut trace = vec![];
        loop {
            let (step, b) = s.next();
            let k = b[0];
            if k == 63 {
                assert_eq!(b[17], 0);
                break;
            }
            trace.push(k);
            assert_eq!(
                s.advance(
                    step,
                    Reply {
                        kind: k,
                        ok: k != fail,
                        error: if k == fail {
                            b"failure".to_vec()
                        } else {
                            vec![]
                        },
                        ..Default::default()
                    }
                ),
                0
            )
        }
        if fail <= 17 {
            assert!(!trace.contains(&21))
        } else {
            assert!(trace.ends_with(&[21, 22]))
        }
    }
}
#[test]
fn actual_hundred_thousand_empty_mirrors_bounded_stack() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new(1);
    let (step, b) = s.next();
    assert_eq!(b[0], 10);
    assert_eq!(
        s.advance(
            step,
            Reply {
                kind: 10,
                ok: true,
                names: vec![b"x".to_vec(); 100000],
                ..Default::default()
            }
        ),
        0
    );
    let (step, b) = s.next();
    assert_eq!(b[0], 31);
    assert_eq!(
        s.advance(
            step,
            Reply {
                kind: 31,
                ok: true,
                text: b"no source".to_vec(),
                ..Default::default()
            }
        ),
        0
    );
    assert_eq!(s.next().1[0], 63);
}
