use smartvpn_core::path_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn request(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    for s in [b"manual".as_slice(), b"node", b"US", b"test", b"evidence"] {
        bytes(&mut b, s)
    }
    b
}
fn target(b: &mut Vec<u8>, token: i64, name: &[u8], baseline: bool) {
    b.extend_from_slice(&token.to_le_bytes());
    bytes(b, name);
    b.extend_from_slice(&[1, baseline as u8]);
}
fn reply(k: u8, s: bool) -> Vec<u8> {
    let mut b = vec![k, 1, 1, 1, 1];
    bytes(&mut b, if k == 5 { b"US" } else { b"node" });
    bytes(&mut b, b"");
    b.extend_from_slice(&0i64.to_le_bytes());
    b.extend_from_slice(&100i64.to_le_bytes());
    b.extend_from_slice(&101i64.to_le_bytes());
    bytes(&mut b, b"test");
    b.push(1);
    b.extend_from_slice(&0u64.to_le_bytes());
    b.push(s as u8);
    if s {
        b.extend_from_slice(&[0, 1, 0, 0]);
        bytes(&mut b, b"US");
        bytes(&mut b, b"");
        bytes(&mut b, b"");
        b.extend_from_slice(&0u64.to_le_bytes());
        b.extend_from_slice(&1u64.to_le_bytes());
        target(&mut b, 3, b"Extra", false);
        b.extend_from_slice(&2u64.to_le_bytes());
        target(&mut b, 1, b"G", true);
        target(&mut b, 2, b"Y", true)
    }
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let b = request(op);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_path_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 4096];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_path_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_path_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_path_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn lifecycle_repeated_next_rejected_reply_and_stale_step() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(0);
    assert_eq!(smartvpn_path_flow_version_v1(), 1);
    let old = s.next();
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_path_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (old.1.len(), 1));
        assert_eq!(s.next(), old)
    }
    for b in [vec![], reply(2, true), reply(1, false)] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), old)
    }
    let b = reply(1, true);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for _ in 0..100 {
        let (step, e) = s.next();
        if e[0] == 0 {
            break;
        }
        assert_eq!(s.advance(step, &reply(e[0], e[0] == 1)), 0)
    }
    assert_eq!(s.next().1[0], 0);
    let id = s.0;
    assert_eq!(smartvpn_path_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_path_flow_close_v1(id), -2);
}
#[test]
fn capacity64_invalid_request_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], request(4), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_path_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut live: Vec<_> = (0..64).map(|_| Session::new(3)).collect();
    let b = request(3);
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_path_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = live.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::new(3);
    assert_ne!(new.0, oldid);
}
#[test]
fn all_four_operations_real_abi() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..4 {
        let s = Session::new(op);
        let mut seen = vec![];
        for _ in 0..150 {
            let (step, e) = s.next();
            let k = e[0];
            if k == 0 {
                break;
            }
            seen.push(k);
            assert_eq!(s.advance(step, &reply(k, k == 1)), 0)
        }
        assert_eq!(s.next().1[0], 0);
        assert_eq!(seen.last(), Some(&63));
        if op == 3 {
            assert_eq!(seen, [1, 3, 4, 2, 63]);
        } else {
            assert!(seen.contains(&8));
            assert!(!seen.contains(&12));
        }
    }
}
