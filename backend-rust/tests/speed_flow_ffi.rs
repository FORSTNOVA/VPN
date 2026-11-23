use smartvpn_core::{
    speed_flow::{Reply, Request},
    speed_flow_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    b.extend_from_slice(&(v.len() as u64).to_le_bytes());
    b.extend_from_slice(v)
}
fn start(q: Request) -> Vec<u8> {
    let mut b = vec![q.op];
    for n in [q.mega_bytes, q.allowance, q.budget, q.remaining, q.length] {
        b.extend_from_slice(&n.to_le_bytes())
    }
    b
}
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![r.kind, r.ok as u8, r.flag as u8];
    bytes(&mut b, &r.text);
    for n in [r.bytes, r.started, r.elapsed] {
        b.extend_from_slice(&n.to_le_bytes())
    }
    b.extend_from_slice(&r.rate.to_le_bytes());
    b
}
struct Session(u64);
impl Session {
    fn new(q: Request) -> Self {
        let b = start(q);
        let mut h = 0;
        assert_eq!(
            unsafe { smartvpn_speed_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        Self(h)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_speed_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_speed_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn advance(&self, step: u64, r: Reply) -> i32 {
        let b = wire(r);
        unsafe { smartvpn_speed_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_speed_flow_close_v1(self.0), 0)
        }
    }
}
fn ok(k: u8) -> Reply {
    Reply {
        kind: k,
        ok: true,
        ..Default::default()
    }
}
#[test]
fn readonly_next_invalid_reply_and_stale_step() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_speed_flow_version_v1(), 1);
    let s = Session::new(Request {
        op: 3,
        ..Default::default()
    });
    let a = s.next();
    assert_eq!(a, s.next());
    assert_ne!(s.advance(a.0, ok(12)), 0);
    assert_eq!(a, s.next());
    assert_eq!(s.advance(a.0, ok(11)), 0);
    let b = s.next();
    assert_ne!(s.advance(a.0, ok(11)), 0);
    assert_eq!(b, s.next());
}
#[test]
fn capacity_preflight_and_close_reuse_do_not_start_extra_worker() {
    let _g = SERIAL.lock().unwrap();
    let mut ss = (0..64)
        .map(|_| {
            Session::new(Request {
                op: 3,
                ..Default::default()
            })
        })
        .collect::<Vec<_>>();
    let mut h = 7;
    let b = start(Request {
        op: 3,
        ..Default::default()
    });
    assert_eq!(
        unsafe { smartvpn_speed_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -4
    );
    assert_eq!(h, 0);
    let old = ss.pop().unwrap();
    let id = old.0;
    drop(old);
    let fresh = Session::new(Request {
        op: 4,
        ..Default::default()
    });
    assert_ne!(fresh.0, id);
    assert_ne!(smartvpn_speed_flow_close_v1(id), 0);
    assert!(ss.iter().all(|s| s.next().0 == 1));
}
#[test]
fn invalid_boolean_trailing_and_oversize_rejected() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new(Request {
        op: 3,
        ..Default::default()
    });
    let before = s.next();
    let mut b = wire(ok(11));
    b[1] = 2;
    assert_ne!(
        unsafe { smartvpn_speed_flow_advance_v1(s.0, before.0, b.as_ptr(), b.len()) },
        0
    );
    assert_eq!(s.next(), before);
    let mut h = 7;
    let huge = vec![0; (8 << 20) + 1];
    assert_ne!(
        unsafe { smartvpn_speed_flow_start_v1(huge.as_ptr(), huge.len(), &mut h) },
        0
    );
    assert_eq!(h, 0);
    let mut b = start(Request::default());
    b.push(0);
    assert_ne!(
        unsafe { smartvpn_speed_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        0
    );
}
#[test]
fn download_positive_eof_counts_then_closes() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new(Request {
        op: 3,
        allowance: 1,
        ..Default::default()
    });
    let mut trace = vec![];
    loop {
        let (st, b) = s.next();
        let k = b[0];
        if k == 63 {
            break;
        }
        trace.push(k);
        let r = if k == 13 {
            Reply {
                kind: k,
                bytes: 65536,
                ..Default::default()
            }
        } else {
            ok(k)
        };
        assert_eq!(s.advance(st, r), 0)
    }
    assert_eq!(trace, vec![11, 12, 13, 14, 16]);
}
#[test]
fn zero_length_read_still_commits_cursor_and_atomic_clock_count() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new(Request {
        op: 5,
        remaining: 4,
        length: 0,
        ..Default::default()
    });
    let mut trace = vec![];
    loop {
        let (st, b) = s.next();
        let k = b[0];
        if k == 63 {
            assert_eq!(b.last(), Some(&0));
            break;
        }
        trace.push((k, i64::from_le_bytes(b[10..18].try_into().unwrap())));
        assert_eq!(s.advance(st, ok(k)), 0)
    }
    assert_eq!(trace, vec![(21, 0), (22, 4), (23, 0)]);
}
#[test]
fn independent_parallel_worker_sessions_do_not_share_state() {
    let _g = SERIAL.lock().unwrap();
    std::thread::scope(|scope| {
        for n in 0..16 {
            scope.spawn(move || {
                let s = Session::new(Request {
                    op: 5,
                    remaining: n + 1,
                    length: 100,
                    ..Default::default()
                });
                loop {
                    let (st, b) = s.next();
                    let k = b[0];
                    if k == 63 {
                        assert_eq!(i64::from_le_bytes(b[10..18].try_into().unwrap()), n + 1);
                        break;
                    }
                    assert_eq!(s.advance(st, ok(k)), 0)
                }
            });
        }
    });
}
#[test]
fn legitimate_stream_can_advance_beyond_old_step_limit() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new(Request {
        op: 3,
        allowance: i64::MAX,
        ..Default::default()
    });
    for _ in 0..2 {
        let (st, b) = s.next();
        assert_eq!(s.advance(st, ok(b[0])), 0)
    }
    for _ in 0..50001 {
        let (st, b) = s.next();
        assert_eq!(b[0], 13);
        assert_eq!(s.advance(st, ok(13)), 0);
        let (st, b) = s.next();
        assert_eq!(b[0], 15);
        assert_eq!(s.advance(st, ok(15)), 0)
    }
    let (st, b) = s.next();
    assert_eq!(b[0], 13);
    assert!(st > 100000);
    assert_eq!(
        s.advance(
            st,
            Reply {
                kind: 13,
                ..Default::default()
            }
        ),
        0
    );
    let (st, b) = s.next();
    assert_eq!(b[0], 16);
    assert_eq!(s.advance(st, ok(16)), 0);
    assert_eq!(s.next().1[0], 63);
}
