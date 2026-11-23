use smartvpn_core::traffic_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn request(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    for _ in 0..3 {
        bytes(&mut b, b"")
    }
    b
}
fn reply(k: u8, ok: bool, flag: bool, s: Option<[u8; 3]>) -> Vec<u8> {
    let mut b = vec![k, ok as u8, flag as u8];
    b.extend_from_slice(&0i64.to_le_bytes());
    b.extend_from_slice(&0i64.to_le_bytes());
    bytes(&mut b, b"");
    b.push(s.is_some() as u8);
    if let Some(s) = s {
        b.extend_from_slice(&s)
    }
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let b = request(op);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_traffic_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 1024];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_traffic_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_traffic_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_traffic_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn lifecycle_invalid_reply_stale_step_and_close() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(0);
    assert_eq!(smartvpn_traffic_flow_version_v1(), 1);
    let before = s.next();
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_traffic_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (before.1.len(), 1));
        assert_eq!(s.next(), before)
    }
    for b in [
        vec![],
        reply(2, true, true, Some([1; 3])),
        reply(1, true, true, None),
        reply(1, true, true, Some([2; 3])),
    ] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before)
    }
    let b = reply(1, true, true, Some([1; 3]));
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for k in [3, 4, 6, 7, 8, 9, 63] {
        let (step, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.advance(step, &reply(k, true, true, None)), 0)
    }
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(s.next().0, &reply(0, true, true, None)), -2);
    let id = s.0;
    assert_eq!(smartvpn_traffic_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_traffic_flow_close_v1(id), -2);
}
#[test]
fn capacity64_limits_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], request(6), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_traffic_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new(2)).collect();
    let b = request(2);
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_traffic_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let previous = old.0;
    drop(old);
    let replacement = Session::new(2);
    assert_ne!(previous, replacement.0);
}
#[test]
fn all_operations_and_long_loop() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..6 {
        let s = Session::new(op);
        let mut ticks = 0;
        let mut steps = 0;
        loop {
            let (step, e) = s.next();
            let k = e[0];
            if k == 0 {
                break;
            }
            let snapshot = if matches!(k, 1 | 2) {
                Some([1, 1, 1])
            } else {
                None
            };
            let flag = if k == 16 {
                ticks += 1;
                ticks <= 20000
            } else {
                true
            };
            assert_eq!(s.advance(step, &reply(k, true, flag, snapshot)), 0);
            steps += 1;
        }
        if op == 4 {
            assert!(steps > 100000)
        }
    }
}
