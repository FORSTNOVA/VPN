use smartvpn_core::quota_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn frame() -> Vec<u8> {
    let mut b = vec![];
    bytes(&mut b, b"x");
    b.push(0);
    bytes(&mut b, b"");
    bytes(&mut b, b"");
    b.extend_from_slice(&0u16.to_le_bytes());
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_quota_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_quota_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_quota_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_quota_flow_close_v1(self.0), 0);
        }
    }
}
fn entries() -> Vec<u8> {
    let mut b = vec![1];
    b.extend_from_slice(&1u64.to_le_bytes());
    bytes(&mut b, b"x");
    bytes(&mut b, b"url");
    b
}
fn strings(v: &[&[u8]]) -> Vec<u8> {
    let mut b = vec![2];
    b.extend_from_slice(&(v.len() as u64).to_le_bytes());
    for s in v {
        bytes(&mut b, s);
    }
    b
}
#[test]
fn repeated_reads_rejected_replies_stale_steps_and_close() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame());
    assert_eq!(smartvpn_quota_flow_version_v1(), 1);
    let before = s.next();
    assert_eq!(before, (1, vec![1]));
    for _ in 0..8 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_quota_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (1, 1));
        assert_eq!(s.next(), before);
    }
    for b in [vec![], vec![0], vec![99], vec![1, 0], vec![5, 2]] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    assert_eq!(s.advance(0, &entries()), -5);
    assert_eq!(s.advance(1, &entries()), 0);
    assert_eq!(s.advance(1, &entries()), -5);
    assert_eq!(s.next().1[0], 2);
    assert_eq!(s.advance(2, &strings(&[])), 0);
    assert_eq!(s.next().1, vec![3]);
    assert_eq!(s.advance(3, &strings(&[b"direct"])), 0);
    assert_eq!(s.next().1, vec![4]);
    assert_eq!(s.advance(4, &[0]), 0);
    assert_eq!(s.next().1, vec![7]);
    assert_eq!(s.advance(5, &entries()), 0);
    assert_eq!(s.next().1[0], 9);
    assert_eq!(s.advance(6, &[0]), 0);
    assert_eq!(s.next().1, vec![10]);
    assert_eq!(s.advance(7, &[5, 1]), 0);
    assert_eq!(s.next().1[0], 11);
    assert_eq!(s.advance(8, &[0]), 0);
    assert_eq!(s.next().1, vec![12]);
    assert_eq!(s.advance(9, &[0]), -2);
    let old = s.0;
    assert_eq!(smartvpn_quota_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_quota_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &entries()), -2);
    let (mut n, mut st) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_quota_flow_next_v1(old, ptr::null_mut(), 0, &mut n, &mut st) },
        -2
    );
}
#[test]
fn capacity_input_limits_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    let b = frame();
    for invalid in [
        vec![],
        b[..b.len() - 1].to_vec(),
        {
            let mut b = b.clone();
            b.push(1);
            b
        },
        vec![0; 8 * 1024 * 1024 + 1],
    ] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_quota_flow_start_v1(invalid.as_ptr(), invalid.len(), &mut id) },
            if invalid.len() > 8 * 1024 * 1024 {
                -1
            } else {
                -2
            }
        );
        assert_eq!(id, 0);
    }
    let mut sessions: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_quota_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::start(&b);
    assert_ne!(oldid, new.0);
    drop(new);
    drop(sessions);
    let _reused = Session::start(&b);
}
