use smartvpn_core::lifecycle_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn request(op: u8) -> Vec<u8> {
    vec![op]
}
fn reply(k: u8, snapshot: bool) -> Vec<u8> {
    let mut b = vec![k, 1, 0];
    b.extend_from_slice(&123i64.to_le_bytes());
    b.extend_from_slice(&1i64.to_le_bytes());
    bytes(&mut b, b"");
    b.extend_from_slice(&0u64.to_le_bytes());
    b.extend_from_slice(&0u64.to_le_bytes());
    b.push(snapshot as u8);
    if snapshot {
        b.extend_from_slice(&[0; 9]);
        for _ in 0..4 {
            b.extend_from_slice(&0i64.to_le_bytes())
        }
        bytes(&mut b, b"");
        bytes(&mut b, b"");
    }
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let b = request(op);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_lifecycle_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 4096];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_lifecycle_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_lifecycle_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_lifecycle_flow_close_v1(self.0), 0)
        }
    }
}

#[test]
fn lifecycle_repeated_next_invalid_reply_and_stale() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(1);
    let first = s.next();
    assert_eq!(smartvpn_lifecycle_flow_version_v1(), 1);
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_lifecycle_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (first.1.len(), first.0));
        assert_eq!(s.next(), first)
    }
    for b in [vec![], reply(1, true), reply(2, false)] {
        assert_eq!(s.advance(first.0, &b), -2);
        assert_eq!(s.next(), first)
    }
    assert_eq!(s.advance(0, &reply(2, true)), -5);
    assert_eq!(s.advance(first.0, &reply(2, true)), 0);
    assert_eq!(s.advance(first.0, &reply(2, true)), -5);
    let id = s.0;
    assert_eq!(smartvpn_lifecycle_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_lifecycle_flow_close_v1(id), -2);
}
#[test]
fn capacity_reuse_and_input_limits() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], request(11), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_lifecycle_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new(0)).collect();
    let b = request(0);
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_lifecycle_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::new(0);
    assert_ne!(new.0, oldid);
}
#[test]
fn eleven_operations_terminate_with_fake_effects() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..11 {
        let s = Session::new(op);
        let mut count = 0;
        loop {
            let (step, e) = s.next();
            if e[0] == 0 {
                break;
            }
            let k = e[0];
            assert_eq!(s.advance(step, &reply(k, matches!(k, 2 | 4 | 21))), 0);
            count += 1;
            assert!(count < 200)
        }
    }
}
