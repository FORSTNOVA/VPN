use smartvpn_core::kernel_exit_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn reply(kind: u8, ok: bool, snapshot: Option<[u8; 4]>) -> Vec<u8> {
    let mut b = vec![kind, ok as u8];
    bytes(&mut b, if ok { b"" } else { b"proxy-failure" });
    b.push(snapshot.is_some() as u8);
    if let Some(s) = snapshot {
        b.extend_from_slice(&s);
    }
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_exit_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_exit_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_exit_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_exit_flow_close_v1(self.0), 0);
        }
    }
}

#[test]
fn lifecycle_preserves_identity_and_cleanup_order() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&[0]);
    assert_eq!(smartvpn_exit_flow_version_v1(), 1);
    let before = s.next();
    assert_eq!(before.1[0], 1);
    for _ in 0..5 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_exit_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (before.1.len(), 1));
        assert_eq!(s.next(), before);
    }
    for b in [
        vec![],
        vec![1],
        reply(2, true, Some([1; 4])),
        reply(1, true, None),
        reply(1, true, Some([2; 4])),
    ] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    let lock = reply(1, true, Some([1; 4]));
    assert_eq!(s.advance(0, &lock), -5);
    assert_eq!(s.advance(1, &lock), 0);
    assert_eq!(s.advance(1, &lock), -5);
    for (step, kind) in [
        (2, 2),
        (3, 3),
        (4, 4),
        (5, 5),
        (6, 6),
        (7, 7),
        (8, 8),
        (9, 9),
        (10, 10),
        (11, 11),
        (12, 63),
    ] {
        let e = s.next();
        assert_eq!(e.1[0], kind);
        if kind == 9 {
            assert_eq!(&e.1[9..], b"proxy-failure");
        }
        assert_eq!(s.advance(step, &reply(kind, kind != 8, None)), 0);
    }
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(13, &reply(0, true, None)), -2);
    let old = s.0;
    assert_eq!(smartvpn_exit_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_exit_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &lock), -2);
    let stale = Session::start(&[0]);
    assert_eq!(stale.advance(1, &reply(1, true, Some([0, 1, 1, 1]))), 0);
    assert_eq!(stale.next().1[0], 63);
}
#[test]
fn cap64_invalidstart_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], vec![1], vec![0, 0], vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_exit_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0);
    }
    let b = [0];
    let mut live: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_exit_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
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
