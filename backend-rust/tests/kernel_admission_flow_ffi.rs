use smartvpn_core::{kernel_admission_flow::Reply, kernel_admission_flow_ffi::*};
use std::{ptr, sync::Mutex};
static LOCK: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    b.extend_from_slice(&(v.len() as u64).to_le_bytes());
    b.extend_from_slice(v);
}
fn start(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    bytes(&mut b, &[]);
    b.extend_from_slice(&0i64.to_le_bytes());
    b.extend_from_slice(&(-1i64).to_le_bytes());
    b
}
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![r.ok as u8, r.flag as u8, r.other as u8];
    bytes(&mut b, &r.text);
    b.extend_from_slice(&r.count.to_le_bytes());
    b.extend_from_slice(&(r.names.len() as u64).to_le_bytes());
    for n in r.names {
        bytes(&mut b, &n);
    }
    b.extend_from_slice(&r.token.to_le_bytes());
    b
}
struct S(u64);
impl S {
    fn new(op: u8) -> Self {
        let b = start(op);
        let mut h = 0;
        assert_eq!(
            unsafe { smartvpn_kernel_admission_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        Self(h)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut st) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_kernel_admission_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut st)
            },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_kernel_admission_flow_next_v1(self.0, b.as_mut_ptr(), n, &mut n, &mut st)
            },
            0
        );
        (st, b)
    }
    fn advance(&self, st: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_kernel_admission_flow_advance_v1(self.0, st, b.as_ptr(), b.len()) }
    }
}
impl Drop for S {
    fn drop(&mut self) {
        assert_eq!(smartvpn_kernel_admission_flow_close_v1(self.0), 0);
    }
}
#[test]
fn readonly_next_rejected_reply_and_stale() {
    let _g = LOCK.lock().unwrap();
    let s = S::new(0);
    let before = s.next();
    assert_eq!(before, s.next());
    let bad = wire(Reply::default());
    assert_eq!(s.advance(before.0, &bad), -2);
    assert_eq!(before, s.next());
    let good = wire(Reply {
        ok: true,
        text: vec![255],
        ..Reply::default()
    });
    assert_eq!(s.advance(before.0, &good), 0);
    assert_eq!(s.advance(before.0, &good), -5);
}
#[test]
fn all_truncations_and_bad_bool_are_atomic() {
    let _g = LOCK.lock().unwrap();
    let s = S::new(3);
    let before = s.next();
    let b = wire(Reply {
        ok: true,
        ..Reply::default()
    });
    for n in 0..b.len() {
        assert_ne!(s.advance(before.0, &b[..n]), 0);
        assert_eq!(s.next(), before);
    }
    let mut bad = b.clone();
    bad[1] = 2;
    assert_ne!(s.advance(before.0, &bad), 0);
    assert_eq!(s.next(), before);
    assert_eq!(s.advance(before.0, &b), 0);
}
#[test]
fn capacity_and_closed_handle() {
    let _g = LOCK.lock().unwrap();
    let mut all = Vec::new();
    for _ in 0..64 {
        all.push(S::new(7));
    }
    let b = start(7);
    let mut h = 0;
    assert_eq!(
        unsafe { smartvpn_kernel_admission_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -4
    );
    let old = all.pop().unwrap();
    let id = old.0;
    drop(old);
    assert_eq!(smartvpn_kernel_admission_flow_close_v1(id), -2);
    let next = S::new(7);
    assert_ne!(id, next.0);
}
#[test]
fn bounds_before_state_change() {
    let _g = LOCK.lock().unwrap();
    let s = S::new(3);
    let before = s.next();
    let huge = vec![0; 8 * 1024 * 1024 + 1];
    assert_ne!(s.advance(before.0, &huge), 0);
    assert_eq!(s.next(), before);
    let mut b = wire(Reply {
        ok: true,
        ..Reply::default()
    });
    b[19..27].copy_from_slice(&100001u64.to_le_bytes());
    assert_ne!(s.advance(before.0, &b), 0);
    assert_eq!(s.next(), before);
}
#[test]
fn ready_60001_failed_attempts_has_no_step_budget() {
    let _g = LOCK.lock().unwrap();
    let s = S::new(7);
    let ok = wire(Reply {
        ok: true,
        ..Reply::default()
    });
    let yes = wire(Reply {
        ok: true,
        flag: true,
        ..Reply::default()
    });
    let fail = wire(Reply::default());
    assert_eq!(s.advance(s.next().0, &ok), 0);
    for _ in 0..60001 {
        for b in [&yes, &fail, &ok] {
            assert_eq!(s.advance(s.next().0, b), 0);
        }
    }
    assert_eq!(s.advance(s.next().0, &ok), 0);
    let (_, out) = s.next();
    assert_eq!(&out[8..14], b"result");
}
