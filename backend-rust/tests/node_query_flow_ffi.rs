use smartvpn_core::node_query_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn request(op: u8) -> Vec<u8> {
    let mut b = vec![op, 0];
    b.extend_from_slice(&1i64.to_le_bytes());
    b
}
fn reply(k: u8, metadata: bool, before: bool) -> Vec<u8> {
    let mut b = vec![k, 1, before as u8];
    b.extend_from_slice(&200i64.to_le_bytes());
    b.extend_from_slice(&1i64.to_le_bytes());
    bytes(&mut b, b"");
    b.extend_from_slice(&0u64.to_le_bytes());
    b.extend_from_slice(&0u64.to_le_bytes());
    b.push(metadata as u8);
    if metadata {
        for _ in 0..3 {
            b.extend_from_slice(&0u64.to_le_bytes())
        }
        b.push(0);
        bytes(&mut b, b"");
        b.push(0);
        b.extend_from_slice(&0i64.to_le_bytes());
    }
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let b = request(op);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_node_query_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 4096];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_node_query_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_node_query_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_node_query_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn lifecycle_repeated_next_invalid_reply_and_stale() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(0);
    let first = s.next();
    assert_eq!(smartvpn_node_query_flow_version_v1(), 1);
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_node_query_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (first.1.len(), first.0));
        assert_eq!(s.next(), first)
    }
    assert_eq!(s.advance(1, &reply(2, false, false)), -2);
    assert_eq!(s.next(), first);
    assert_eq!(s.advance(0, &reply(1, false, false)), -5);
    assert_eq!(s.advance(1, &reply(1, false, false)), 0);
    assert_eq!(s.advance(1, &reply(1, false, false)), -5);
    let (step, e) = s.next();
    assert_eq!(e[0], 3);
    assert_eq!(s.advance(step, &reply(3, false, false)), 0);
    let old = s.next();
    assert_eq!(old.1[0], 5);
    assert_eq!(s.advance(old.0, &reply(5, false, false)), -2);
    assert_eq!(s.next(), old);
    assert_eq!(s.advance(old.0, &reply(5, true, false)), 0);
    for k in [2, 16, 63] {
        let (step, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.advance(step, &reply(k, false, false)), 0)
    }
    assert_eq!(s.next().1[0], 0);
    let id = s.0;
    assert_eq!(smartvpn_node_query_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_node_query_flow_close_v1(id), -2);
    assert_eq!(
        unsafe { smartvpn_node_query_flow_advance_v1(id, 1, first.1.as_ptr(), first.1.len()) },
        -2
    );
}
#[test]
fn capacity_reuse_input_limits() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], request(6), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_node_query_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new(0)).collect();
    let b = request(0);
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_node_query_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
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
fn six_operations_and_long_poll_without_total_step_cap() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..6 {
        let s = Session::new(op);
        let mut polls = 0;
        let mut steps = 0;
        loop {
            let (step, e) = s.next();
            let k = e[0];
            if k == 0 {
                break;
            }
            let before = if k == 14 {
                polls += 1;
                polls <= 10000
            } else {
                false
            };
            assert_eq!(s.advance(step, &reply(k, k == 5, before)), 0);
            steps += 1;
            assert!(steps < 200000)
        }
        if op == 4 {
            assert!(steps > 100000);
            assert_eq!(polls, 10001)
        }
    }
}
