use smartvpn_core::manual_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn frame(op: u8) -> Vec<u8> {
    let mut b = vec![op, 1];
    bytes(&mut b, b"");
    b
}
fn node(b: &mut Vec<u8>, token: i64) {
    b.extend_from_slice(&token.to_le_bytes());
    bytes(b, b"same");
    b.push(1);
    b.extend_from_slice(&1i64.to_le_bytes());
}
fn reply(kind: u8, snapshot: bool, count: usize) -> Vec<u8> {
    let mut b = vec![kind, 1, 1];
    b.extend_from_slice(&99i64.to_le_bytes());
    b.extend_from_slice(&4294967296i64.to_le_bytes());
    bytes(&mut b, b"");
    node(&mut b, 9);
    b.extend_from_slice(&(count as u64).to_le_bytes());
    for i in 0..count {
        node(&mut b, i as i64 + 1);
    }
    b.push(snapshot as u8);
    if snapshot {
        b.push(1);
        b.extend_from_slice(&0u64.to_le_bytes());
        b.extend_from_slice(&0u64.to_le_bytes());
    }
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_manual_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_manual_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_manual_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_manual_flow_close_v1(self.0), 0);
        }
    }
}

#[test]
fn lifecycle_rejected_reply_no_consume_and_close() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame(0));
    assert_eq!(smartvpn_manual_flow_version_v1(), 1);
    let before = s.next();
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_manual_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (before.1.len(), 1));
        assert_eq!(s.next(), before);
    }
    for b in [vec![], reply(1, true, 0), reply(2, false, 0)] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    let b = reply(2, true, 0);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for (step, kind) in [(2, 11), (3, 12), (4, 13), (5, 63)] {
        assert_eq!(s.next().1[0], kind);
        assert_eq!(s.advance(step, &reply(kind, false, 0)), 0);
    }
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(6, &reply(0, false, 0)), -2);
    let old = s.0;
    assert_eq!(smartvpn_manual_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_manual_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &b), -2);
}
#[test]
fn cap64_reuse_and_invalid_frames() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], frame(7), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_manual_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0);
    }
    let b = frame(1);
    let mut live: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_manual_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
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
#[test]
fn large_metadata_only_load_keeps_duplicates() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::start(&frame(0));
    let (mut steps, mut appends) = (0, 0);
    loop {
        let (step, e) = s.next();
        let kind = e[0];
        if kind == 0 {
            break;
        }
        assert_eq!(
            s.advance(
                step,
                &reply(kind, kind == 2, if kind == 13 { 50000 } else { 0 })
            ),
            0
        );
        if kind == 7 {
            appends += 1;
        }
        steps += 1;
        assert!(steps < 110000);
    }
    assert_eq!(appends, 50000);
    assert!(steps > 100000);
}
