use smartvpn_core::chain_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn chain(b: &mut Vec<u8>) {
    b.push(0);
    bytes(b, b"");
    b.extend_from_slice(&0u64.to_le_bytes());
}
fn frame(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    chain(&mut b);
    bytes(&mut b, b"saved");
    bytes(&mut b, b"");
    b.extend_from_slice(&5i64.to_le_bytes());
    b.extend_from_slice(&2u64.to_le_bytes());
    bytes(&mut b, b"a");
    bytes(&mut b, b"b");
    b
}
fn reply(kind: u8, snapshot: bool, groups: bool) -> Vec<u8> {
    let mut b = vec![kind, 1, 0];
    for v in [0i64, 10, 77] {
        b.extend_from_slice(&v.to_le_bytes());
    }
    bytes(&mut b, b"");
    bytes(&mut b, b"");
    b.extend_from_slice(&(if groups { 2u64 } else { 0 }).to_le_bytes());
    if groups {
        for (i, name) in [b"a".as_slice(), b"b"].iter().enumerate() {
            bytes(&mut b, name);
            b.extend_from_slice(&1u64.to_le_bytes());
            b.extend_from_slice(&(i as i64 + 1).to_le_bytes());
            b.extend_from_slice(&[0, 1, 1]);
        }
    }
    b.push(snapshot as u8);
    if snapshot {
        b.push(1);
        bytes(&mut b, b"");
        chain(&mut b);
        b.extend_from_slice(&0u64.to_le_bytes());
    }
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_chain_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_chain_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_chain_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_chain_flow_close_v1(self.0), 0);
        }
    }
}

#[test]
fn compile_lifecycle_steps_invalidreply_and_close() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame(5));
    assert_eq!(smartvpn_chain_flow_version_v1(), 1);
    let before = s.next();
    assert_eq!(before.1[0], 2);
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_chain_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (before.1.len(), 1));
        assert_eq!(s.next(), before);
    }
    for b in [vec![], reply(1, true, false), reply(2, false, false)] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    let b = reply(2, true, false);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    assert_eq!(s.next().1[0], 7);
    assert_eq!(s.advance(2, &reply(7, false, true)), 0);
    for step in [3, 4] {
        let e = s.next().1;
        assert_eq!(e[0], 8);
        assert_eq!(
            i64::from_le_bytes(e[9..17].try_into().unwrap()),
            if step == 3 { 0 } else { 1 }
        );
        assert_eq!(s.advance(step, &reply(8, false, false)), 0);
    }
    assert_eq!(s.next().1[0], 63);
    assert_eq!(s.advance(5, &reply(63, false, false)), 0);
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(6, &reply(0, false, false)), -2);
    let old = s.0;
    assert_eq!(smartvpn_chain_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_chain_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &b), -2);
}
#[test]
fn cap64_invalidframes_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], frame(6), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_chain_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0);
    }
    let b = frame(5);
    let mut live: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_chain_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
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
    let _again = Session::start(&b);
}
#[test]
fn disabled_update_rolls_back_after_save_failure() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::start(&frame(0));
    for (step, kind) in [(1, 1), (2, 3), (3, 4), (4, 12), (5, 14)] {
        assert_eq!(s.next().1[0], kind);
        let mut r = reply(kind, kind == 1, false);
        if kind == 14 {
            r[1] = 0;
        }
        assert_eq!(s.advance(step, &r), 0);
    }
    assert_eq!(s.next().1[0], 15);
    assert_eq!(s.advance(6, &reply(15, false, false)), 0);
    let e = s.next().1;
    assert_eq!(e[0], 63);
    assert_eq!(i64::from_le_bytes(e[1..9].try_into().unwrap()), 500);
    assert_eq!(i64::from_le_bytes(e[9..17].try_into().unwrap()), 30);
}
