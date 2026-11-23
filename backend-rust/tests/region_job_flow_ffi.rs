use smartvpn_core::region_job_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn frame(op: u8) -> Vec<u8> {
    let mut b = vec![op, 0, 0];
    bytes(&mut b, b"");
    b.extend_from_slice(&0i64.to_le_bytes());
    b
}
fn reply(kind: u8, snapshot: Option<[u8; 5]>, names: &[Vec<u8>]) -> Vec<u8> {
    let mut b = vec![kind, 1, u8::from(kind == 8), 0];
    for v in [0i64, 10, 0] {
        b.extend_from_slice(&v.to_le_bytes());
    }
    for _ in 0..3 {
        bytes(&mut b, b"");
    }
    b.extend_from_slice(&(names.len() as u64).to_le_bytes());
    for n in names {
        bytes(&mut b, n);
    }
    b.extend_from_slice(&0u64.to_le_bytes());
    b.push(snapshot.is_some() as u8);
    if let Some(s) = snapshot {
        b.extend_from_slice(&s);
        b.extend_from_slice(&0u64.to_le_bytes());
    }
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_region_job_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 2048];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_region_job_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, s: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_region_job_flow_advance_v1(self.0, s, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_region_job_flow_close_v1(self.0), 0);
        }
    }
}

#[test]
fn start_snapshot_before_unlock_spawn_and_invalid_reply_no_consume() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame(0));
    assert_eq!(smartvpn_region_job_flow_version_v1(), 1);
    let before = s.next();
    for _ in 0..3 {
        let (mut n, mut st) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_region_job_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut st) },
            -3
        );
        assert_eq!((n, st), (before.1.len(), 1));
        assert_eq!(s.next(), before);
    }
    for b in [
        vec![],
        reply(2, Some([0, 1, 0, 0, 0]), &[]),
        reply(1, None, &[]),
    ] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    let b = reply(1, Some([0, 1, 0, 0, 0]), &[]);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for (step, kind) in [(2, 4), (3, 5), (4, 6), (5, 2), (6, 7), (7, 63)] {
        assert_eq!(s.next().1[0], kind);
        assert_eq!(s.advance(step, &reply(kind, None, &[])), 0);
    }
    assert_eq!(s.next().1[0], 0);
    assert_eq!(s.advance(8, &reply(0, None, &[])), -2);
    let old = s.0;
    assert_eq!(smartvpn_region_job_flow_close_v1(old), 0);
    s.0 = 0;
    assert_eq!(smartvpn_region_job_flow_close_v1(old), -2);
    assert_eq!(s.advance(1, &b), -2);
}
#[test]
fn cap64_reuse_invalid_start_and_input_limit() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], frame(3), vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_region_job_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0);
    }
    let b = frame(1);
    let mut live: Vec<_> = (0..64).map(|_| Session::start(&b)).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_region_job_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = live.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::start(&b);
    assert_ne!(oldid, new.0);
    drop(new);
    drop(live);
    let _again = Session::start(&b);
}
#[test]
fn long_job_above_100k_steps_is_not_replayed_or_cut_off() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::start(&frame(1));
    let nodes: Vec<Vec<u8>> = (0..25000).map(|i| i.to_string().into_bytes()).collect();
    let (mut steps, mut saved) = (0, 0);
    loop {
        let (step, e) = s.next();
        let kind = e[0];
        if kind == 0 {
            break;
        }
        let r = reply(
            kind,
            if kind == 1 {
                Some([0, 1, 1, 1, 0])
            } else {
                None
            },
            if kind == 12 { &nodes } else { &[] },
        );
        assert_eq!(s.advance(step, &r), 0);
        if kind == 19 {
            saved += 1;
        }
        steps += 1;
        assert!(steps < 300000);
    }
    assert_eq!(saved, 25000);
    assert!(steps > 100000);
}
