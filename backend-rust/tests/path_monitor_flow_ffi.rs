use smartvpn_core::path_monitor_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn request(op: u8) -> Vec<u8> {
    let mut b = vec![op, 1, 1];
    b.extend_from_slice(&120i64.to_le_bytes());
    b.extend_from_slice(&0u64.to_le_bytes());
    b.extend_from_slice(&0u64.to_le_bytes());
    b
}
fn reply(k: u8, s: bool, event: &[u8]) -> Vec<u8> {
    let mut b = vec![k, 1, matches!(k, 5 | 18 | 25 | 26) as u8];
    b.extend_from_slice(&0i64.to_le_bytes());
    b.extend_from_slice(&1i64.to_le_bytes());
    bytes(&mut b, event);
    bytes(&mut b, b"");
    b.extend_from_slice(&0i64.to_le_bytes());
    b.push(0);
    bytes(&mut b, b"");
    bytes(&mut b, b"");
    b.push(0);
    b.extend_from_slice(&0i64.to_le_bytes());
    b.push(0);
    b.extend_from_slice(&0u64.to_le_bytes());
    b.push(s as u8);
    if s {
        b.extend_from_slice(&[1, 0, 1, 0]);
        b.extend_from_slice(&120i64.to_le_bytes());
        bytes(&mut b, b"");
        bytes(&mut b, b"");
        b.extend_from_slice(&0u64.to_le_bytes());
        b.extend_from_slice(&0u64.to_le_bytes());
    }
    b.extend_from_slice(&0u64.to_le_bytes());
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let b = request(op);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_path_monitor_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 4096];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_path_monitor_flow_next_v1(
                    self.0,
                    b.as_mut_ptr(),
                    b.len(),
                    &mut n,
                    &mut step,
                )
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_path_monitor_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_path_monitor_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn lifecycle_invalid_reply_and_stale_steps() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(7);
    let old = s.next();
    assert_eq!(smartvpn_path_monitor_flow_version_v1(), 2);
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_path_monitor_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step)
            },
            -3
        );
        assert_eq!((n, step), (old.1.len(), 1));
        assert_eq!(s.next(), old)
    }
    for b in [vec![], reply(2, true, b""), reply(1, false, b"")] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), old)
    }
    let b = reply(1, true, b"");
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for k in [23, 2, 63] {
        let (step, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.advance(step, &reply(k, false, b"")), 0)
    }
    assert_eq!(s.next().1[0], 0);
    let id = s.0;
    assert_eq!(smartvpn_path_monitor_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_path_monitor_flow_close_v1(id), -2);
}
#[test]
fn capacity_and_input_limits() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], request(10), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_path_monitor_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut live: Vec<_> = (0..64).map(|_| Session::new(0)).collect();
    let b = request(0);
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_path_monitor_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = live.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::new(0);
    assert_ne!(oldid, new.0);
}
#[test]
fn ten_operations_and_more_than_100k_steps() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..10 {
        let s = Session::new(op);
        let mut waits = 0;
        let mut steps = 0;
        loop {
            let (step, e) = s.next();
            let k = e[0];
            if k == 0 {
                break;
            }
            let event = if k == 12 {
                waits += 1;
                if waits <= 30000 {
                    b"baseline".as_slice()
                } else {
                    b"cancel"
                }
            } else {
                b""
            };
            assert_eq!(s.advance(step, &reply(k, matches!(k, 1 | 3 | 4), event)), 0);
            steps += 1;
            assert!(steps < 150000)
        }
        if op == 2 {
            assert!(steps > 100000)
        }
    }
}
