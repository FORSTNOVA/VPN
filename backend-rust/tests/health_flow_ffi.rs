use smartvpn_core::health_flow_ffi::*;
use std::{
    ptr,
    sync::Mutex,
    time::{Duration, Instant},
};
static SERIAL: Mutex<()> = Mutex::new(());
fn int(b: &mut Vec<u8>, n: i64) {
    b.extend_from_slice(&n.to_le_bytes());
}
fn frame(count: usize, patrol: bool) -> Vec<u8> {
    let mut b = vec![];
    int(&mut b, count as i64);
    for _ in 0..count {
        b.extend_from_slice(&[1, 1, 1, 0]);
        for n in [1, 0, 0, 10] {
            int(&mut b, n);
        }
    }
    int(&mut b, if patrol { 0 } else { -1 });
    b.extend_from_slice(&[0, 1, 1, 0]);
    int(&mut b, 2);
    int(&mut b, 3);
    if patrol {
        b.extend_from_slice(&[0, 0]);
        int(&mut b, count as i64);
        for _ in 0..count {
            int(&mut b, 10);
        }
    } else {
        b.push(3);
    }
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_health_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        assert_ne!(id, 0);
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let mut b = vec![0; 128];
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_health_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_health_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_health_flow_close_v1(self.0), 0);
        }
    }
}
#[test]
fn repeat_read_stale_steps_invalid_replies_and_closed_handles() {
    let _guard = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame(2, false));
    assert_eq!(smartvpn_health_flow_version_v1(), 1);
    let expected = s.next();
    assert_eq!(expected.0, 1);
    assert_eq!(expected.1[0], 5);
    for _ in 0..10 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_health_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (expected.1.len(), 1));
        assert_eq!(s.next(), expected);
    }
    for invalid in [
        &[0][..],
        &[1, 2][..],
        &[1][..],
        &[99][..],
        &[1, 1, 0][..],
        &[][..],
    ] {
        assert_eq!(s.advance(1, invalid), -2);
        assert_eq!(s.next(), expected);
    }
    assert_eq!(s.advance(0, &[1, 1]), -5);
    assert_eq!(s.advance(2, &[1, 1]), -5);
    assert_eq!(s.advance(1, &[1, 1]), 0);
    let committed = s.next();
    assert_eq!(committed.0, 2);
    assert_eq!(committed.1[0], 7);
    assert_eq!(s.advance(1, &[1, 1]), -5);
    assert_eq!(s.next(), committed);
    while s.next().1[0] != 10 {
        let (step, _) = s.next();
        assert_eq!(s.advance(step, &[0]), 0);
    }
    let terminal = s.next();
    assert_eq!(s.advance(terminal.0, &[0]), -2);
    assert_eq!(s.next(), terminal);
    let old = s.0;
    assert_eq!(smartvpn_health_flow_close_v1(old), 0);
    s.0 = 0;
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_health_flow_next_v1(old, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
    assert_eq!(smartvpn_health_flow_close_v1(old), -2);
    assert_eq!(
        unsafe { smartvpn_health_flow_advance_v1(old, 1, [0].as_ptr(), 1) },
        -2
    );
    let fresh = Session::start(&frame(1, false));
    assert_ne!(fresh.0, old);
}
#[test]
fn eight_session_capacity_and_slot_reuse() {
    let _guard = SERIAL.lock().unwrap();
    let b = frame(1, false);
    let mut sessions: Vec<_> = (0..8).map(|_| Session::start(&b)).collect();
    let mut ninth = 123;
    assert_eq!(
        unsafe { smartvpn_health_flow_start_v1(b.as_ptr(), b.len(), &mut ninth) },
        -4
    );
    assert_eq!(ninth, 0);
    let removed = sessions.pop().unwrap();
    let old = removed.0;
    drop(removed);
    let new = Session::start(&b);
    assert_ne!(new.0, old);
    for s in &sessions {
        assert_eq!(s.next().0, 1);
    }
    drop(new);
    drop(sessions);
}
#[test]
fn malformed_start_limits_and_reply_shape_preserve_state() {
    let _guard = SERIAL.lock().unwrap();
    let good = frame(1, false);
    for b in [
        vec![],
        good[..good.len() - 1].to_vec(),
        {
            let mut b = good.clone();
            b.push(0);
            b
        },
        {
            let mut b = good.clone();
            b[8] = 2;
            b
        },
        {
            let mut b = good.clone();
            b[..8].copy_from_slice(&100001i64.to_le_bytes());
            b
        },
    ] {
        let mut id = 77;
        assert_eq!(
            unsafe { smartvpn_health_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    let mut id = 77;
    assert_eq!(
        unsafe { smartvpn_health_flow_start_v1(good.as_ptr(), (8 << 20) + 1, &mut id) },
        -1
    );
    assert_eq!(id, 0);
    // A valid measure request rejects a success vector with the wrong node count.
    let mut b = frame(1, false);
    b[36..44].copy_from_slice(&0i64.to_le_bytes());
    let s = Session::start(&b);
    let before = s.next();
    assert_eq!(before.1[0], 3);
    let mut reply = vec![2, 1];
    int(&mut reply, 0);
    assert_eq!(s.advance(before.0, &reply), -2);
    assert_eq!(s.next(), before);
}
#[test]
fn hundred_thousand_steps_execute_without_full_flow_clone() {
    let _guard = SERIAL.lock().unwrap();
    let start = Instant::now();
    let s = Session::start(&frame(100000, true));
    let mut seen = vec![false; 100000];
    let mut count = 0;
    loop {
        let (step, e) = s.next();
        match e[0] {
            10 => break,
            1 => {}
            2 => {
                assert_eq!(e.len(), 43);
                let node = i64::from_le_bytes(e[1..9].try_into().unwrap()) as usize;
                assert!(!seen[node]);
                seen[node] = true;
                count += 1;
            }
            op => panic!("unexpected effect {op}"),
        };
        assert_eq!(s.advance(step, &[0]), 0);
    }
    assert_eq!(count, 100000);
    assert!(
        start.elapsed() < Duration::from_secs(30),
        "100k flow exceeded generous linear-work regression budget: {:?}",
        start.elapsed()
    );
}
