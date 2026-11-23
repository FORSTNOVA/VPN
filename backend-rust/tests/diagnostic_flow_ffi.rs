use smartvpn_core::diagnostic_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn reply(k: u8, ok: bool, flag: bool, s: bool) -> Vec<u8> {
    let mut b = vec![k, ok as u8, flag as u8];
    b.extend_from_slice(&1i64.to_le_bytes());
    b.extend_from_slice(&0i64.to_le_bytes());
    bytes(&mut b, b"node");
    bytes(&mut b, b"");
    b.extend_from_slice(&1u64.to_le_bytes());
    bytes(&mut b, b"node");
    b.push(s as u8);
    if s {
        b.push(0);
        bytes(&mut b, b"node");
        for n in [2i64, 2, 3] {
            b.extend_from_slice(&n.to_le_bytes())
        }
    }
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_diagnostic_flow_start_v1(&op, 1, &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 4096];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_diagnostic_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_diagnostic_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_diagnostic_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn lifecycle_and_nonconsuming_invalid_reply() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(0);
    assert_eq!(smartvpn_diagnostic_flow_version_v1(), 1);
    let old = s.next();
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_diagnostic_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (old.1.len(), 1));
        assert_eq!(s.next(), old)
    }
    for b in [
        vec![],
        reply(2, true, true, true),
        reply(1, true, true, false),
    ] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), old)
    }
    let b = reply(1, true, true, true);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for k in [
        2, 8, 23, 9, 11, 12, 13, 14, 15, 16, 17, 18, 19, 25, 20, 21, 24, 63,
    ] {
        let (step, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.advance(step, &reply(k, true, true, false)), 0)
    }
    assert_eq!(s.next().1[0], 0);
    let id = s.0;
    assert_eq!(smartvpn_diagnostic_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_diagnostic_flow_close_v1(id), -2);
}
#[test]
fn capacity_and_input_boundaries() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], vec![5], vec![0, 0], vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_diagnostic_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new(2)).collect();
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_diagnostic_flow_start_v1(&2, 1, &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let next = Session::new(2);
    assert_ne!(next.0, oldid);
}
#[test]
fn standalone_and_temporary_cleanup() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..5 {
        let s = Session::new(op);
        let mut seen = vec![];
        for _ in 0..80 {
            let (step, e) = s.next();
            let k = e[0];
            if k == 0 {
                break;
            }
            seen.push(k);
            assert_eq!(
                s.advance(step, &reply(k, true, k != 2, matches!(k, 1 | 4))),
                0
            )
        }
        assert_eq!(s.next().1[0], 0);
        if op == 0 {
            assert_eq!(&seen[seen.len() - 4..], &[21, 22, 24, 63])
        } else {
            assert!(!seen.contains(&22));
            assert!(!seen.contains(&24))
        }
    }
}
