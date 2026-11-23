use smartvpn_core::kernel_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn reply(kind: u8, snapshot: bool) -> Vec<u8> {
    let mut b = vec![kind, 1, 0];
    b.extend_from_slice(&0i64.to_le_bytes());
    b.extend_from_slice(&0i64.to_le_bytes());
    bytes(&mut b, b"");
    b.push(snapshot as u8);
    if snapshot {
        bytes(&mut b, b"system-proxy");
        bytes(&mut b, b"__merged__");
        for v in [0i64, 8888, 8888, 9999] {
            b.extend_from_slice(&v.to_le_bytes());
        }
        b.extend_from_slice(&[1, 1, 1]);
    }
    b.push(0);
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_kernel_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_kernel_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_kernel_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_kernel_flow_close_v1(self.0), 0);
        }
    }
}

#[test]
fn borrowed_stop_sequence_rejected_reply_and_stale_step() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&[1]);
    assert_eq!(smartvpn_kernel_flow_version_v1(), 1);
    let before = s.next();
    assert_eq!(before.1[0], 1);
    for _ in 0..5 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_kernel_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (before.1.len(), 1));
        assert_eq!(s.next(), before);
    }
    for b in [vec![], vec![1], reply(2, true), reply(1, false)] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    assert_eq!(s.advance(0, &reply(1, true)), -5);
    assert_eq!(s.advance(1, &reply(1, true)), 0);
    assert_eq!(s.advance(1, &reply(1, true)), -5);
    for (step, kind) in [(2, 27), (3, 29), (4, 30), (5, 31), (6, 33), (7, 63)] {
        assert_eq!(s.next().1[0], kind);
        assert_eq!(s.advance(step, &reply(kind, false)), 0);
    }
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(8, &reply(0, false)), -2);
    let old = s.0;
    assert_eq!(smartvpn_kernel_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_kernel_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &reply(1, true)), -2);
}
#[test]
fn cap64_invalidstart_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], vec![2], vec![0, 0], vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_kernel_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0);
    }
    let b = [0];
    let mut live: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_kernel_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = live.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::start(&b);
    assert_ne!(new.0, oldid);
    drop(new);
    drop(live);
    let _new = Session::start(&b);
}
