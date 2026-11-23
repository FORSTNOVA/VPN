use smartvpn_core::region_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn frame(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    bytes(&mut b, b"");
    b.push(1);
    b
}
fn reply(kind: u8, snapshot: bool, records: bool) -> Vec<u8> {
    let mut b = vec![kind, 1, 0];
    bytes(&mut b, b"");
    bytes(&mut b, b"");
    b.extend_from_slice(&10i64.to_le_bytes());
    b.extend_from_slice(&(if records { 2u64 } else { 0 }).to_le_bytes());
    if records {
        for (name, age) in [(b"z".as_slice(), 11i64), (b"a".as_slice(), 10i64)] {
            bytes(&mut b, name);
            bytes(&mut b, b"US");
            b.push(1);
            b.extend_from_slice(&age.to_le_bytes());
            b.push(0);
            b.extend_from_slice(&1i64.to_le_bytes());
        }
    }
    b.push(snapshot as u8);
    if snapshot {
        b.push(0);
        bytes(&mut b, b"US");
        bytes(&mut b, b"");
    }
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_region_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_region_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_region_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_region_flow_close_v1(self.0), 0);
        }
    }
}

#[test]
fn lock_clear_lifecycle_rejects_without_consuming() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame(0));
    assert_eq!(smartvpn_region_flow_version_v1(), 1);
    let before = s.next();
    for _ in 0..3 {
        let (mut n, mut st) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_region_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut st) },
            -3
        );
        assert_eq!((n, st), (before.1.len(), 1));
        assert_eq!(s.next(), before);
    }
    for b in [vec![], reply(2, true, false), reply(1, false, false)] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    let b = reply(1, true, false);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for (step, kind) in [(2, 3), (3, 4), (4, 5), (5, 13), (6, 12), (7, 2), (8, 63)] {
        assert_eq!(s.next().1[0], kind);
        assert_eq!(s.advance(step, &reply(kind, false, false)), 0);
    }
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(9, &reply(0, false, false)), -2);
    let old = s.0;
    assert_eq!(smartvpn_region_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_region_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &b), -2);
}
#[test]
fn report_indices_and_ttl_flags_cross_abi() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::start(&frame(1));
    assert_eq!(s.advance(1, &reply(1, true, false)), 0);
    assert_eq!(s.next().1[0], 2);
    assert_eq!(s.advance(2, &reply(2, false, true)), 0);
    let b = s.next().1;
    assert_eq!(b[0], 63);
    let mut expected = vec![63];
    expected.extend_from_slice(&200i64.to_le_bytes());
    expected.extend_from_slice(&5i64.to_le_bytes());
    bytes(&mut expected, b"US");
    bytes(&mut expected, b"");
    expected.extend_from_slice(&2u64.to_le_bytes());
    expected.extend_from_slice(&1i64.to_le_bytes());
    expected.extend_from_slice(&0i64.to_le_bytes());
    expected.extend_from_slice(&2u64.to_le_bytes());
    expected.extend_from_slice(&[0, 1]);
    expected.extend_from_slice(&1u64.to_le_bytes());
    expected.extend_from_slice(&1i64.to_le_bytes());
    assert_eq!(b, expected);
}
#[test]
fn cap64_reuse_and_input_bounds() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], frame(2), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_region_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0);
    }
    let b = frame(0);
    let mut live: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_region_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
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
