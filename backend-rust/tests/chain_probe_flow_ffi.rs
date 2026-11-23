use smartvpn_core::chain_probe_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn reply(k: u8, ok: bool, flag: bool, s: bool) -> Vec<u8> {
    let mut b = vec![k, ok as u8, flag as u8];
    b.extend_from_slice(&2i64.to_le_bytes());
    b.extend_from_slice(
        &(if k == 21 {
            204i64
        } else if k == 33 {
            2
        } else {
            5
        })
        .to_le_bytes(),
    );
    b.extend_from_slice(&0i64.to_le_bytes());
    bytes(&mut b, b"");
    b.push(s as u8);
    if s {
        b.extend_from_slice(&[0, 0, 0, 1]);
        b.extend_from_slice(&2i64.to_le_bytes());
        b.push(0)
    }
    b
}
struct Session(u64);
impl Session {
    fn new(op: u8) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_chain_probe_flow_start_v1(&op, 1, &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 4096];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_chain_probe_flow_next_v1(
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
        unsafe { smartvpn_chain_probe_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_chain_probe_flow_close_v1(self.0), 0)
        }
    }
}
#[test]
fn lifecycle_and_invalid_reply_nonconsumption() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new(4);
    assert_eq!(smartvpn_chain_probe_flow_version_v1(), 1);
    let before = s.next();
    for _ in 0..3 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_chain_probe_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step)
            },
            -3
        );
        assert_eq!((n, step), (before.1.len(), 1));
        assert_eq!(s.next(), before)
    }
    for b in [
        vec![],
        reply(1, true, true, true),
        reply(3, true, true, false),
    ] {
        assert_eq!(s.advance(1, &b), -2);
        assert_eq!(s.next(), before)
    }
    let b = reply(3, true, true, true);
    assert_eq!(s.advance(0, &b), -5);
    assert_eq!(s.advance(1, &b), 0);
    assert_eq!(s.advance(1, &b), -5);
    for k in [12, 20, 21, 22, 23, 24, 11, 12, 20, 21, 22, 23, 24, 63] {
        let (step, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.advance(step, &reply(k, true, true, false)), 0)
    }
    assert_eq!(s.next().1[0], 0);
    let id = s.0;
    assert_eq!(smartvpn_chain_probe_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_chain_probe_flow_close_v1(id), -2);
}
#[test]
fn capacity_bound_and_release_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], vec![5], vec![0, 0], vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 42;
        assert_eq!(
            unsafe { smartvpn_chain_probe_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut live: Vec<_> = (0..64).map(|_| Session::new(2)).collect();
    let mut id = 42;
    assert_eq!(
        unsafe { smartvpn_chain_probe_flow_start_v1(&2, 1, &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = live.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::new(2);
    assert_ne!(new.0, oldid);
}
#[test]
fn all_five_workflows_and_reverse_cleanup() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..5 {
        let s = Session::new(op);
        let mut seen = vec![];
        for _ in 0..150 {
            let (step, e) = s.next();
            let k = e[0];
            if k == 0 {
                break;
            }
            seen.push((k, i64::from_le_bytes(e[1..9].try_into().unwrap())));
            assert_eq!(
                s.advance(step, &reply(k, true, true, matches!(k, 1 | 3 | 5 | 7))),
                0
            )
        }
        assert_eq!(s.next().1[0], 0);
        if matches!(op, 0 | 2 | 3) {
            let cleanup: Vec<_> = seen
                .iter()
                .copied()
                .filter(|(k, _)| matches!(k, 25..=27))
                .collect();
            assert_eq!(cleanup, [(25, 0), (26, 1), (26, 0), (27, 0)])
        }
        if op == 0 {
            let tail: Vec<_> = seen.iter().rev().take(3).map(|e| e.0).collect();
            assert_eq!(tail, [63, 37, 36])
        }
    }
}
