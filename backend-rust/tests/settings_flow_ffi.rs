use smartvpn_core::settings_flow_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn frame(presence: u16, invalid: u16, flags: [u8; 13]) -> Vec<u8> {
    let mut b = presence.to_le_bytes().to_vec();
    b.extend_from_slice(&invalid.to_le_bytes());
    b.extend_from_slice(&flags);
    b
}
struct Session(u64);
impl Session {
    fn start(b: &[u8]) -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_settings_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
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
                smartvpn_settings_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        b.truncate(n);
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_settings_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_settings_flow_close_v1(self.0), 0);
        }
    }
}
fn ok(e: &[u8]) -> Vec<u8> {
    match e[0] {
        1 => vec![1, 0],
        7 | 9 => vec![1, 1],
        _ => vec![0],
    }
}
fn trace(b: &[u8], mut reply: impl FnMut(&[u8]) -> Vec<u8>) -> Vec<Vec<u8>> {
    let s = Session::start(b);
    let mut out = vec![];
    for _ in 0..40 {
        let (step, e) = s.next();
        if e == [14] {
            return out;
        }
        let r = reply(&e);
        out.push(e);
        assert_eq!(s.advance(step, &r), 0);
    }
    panic!("nonterminating session")
}
#[test]
fn repeat_reads_stale_steps_invalid_replies_and_close() {
    let _guard = SERIAL.lock().unwrap();
    let mut s = Session::start(&frame(0, 0, [0; 13]));
    assert_eq!(smartvpn_settings_flow_version_v1(), 1);
    let expected = s.next();
    assert_eq!(expected, (1, vec![1, 0]));
    for _ in 0..10 {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_settings_flow_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        assert_eq!((n, step), (2, 1));
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
    assert_eq!(s.advance(0, &[1, 0]), -5);
    assert_eq!(s.advance(2, &[1, 0]), -5);
    assert_eq!(s.advance(1, &[1, 0]), 0);
    let committed = s.next();
    assert_eq!(committed.0, 2);
    assert_eq!(committed.1[0], 2);
    assert_eq!(s.advance(1, &[1, 0]), -5);
    assert_eq!(s.advance(2, &[1, 1]), -2);
    assert_eq!(s.next(), committed);
    while s.next().1 != [14] {
        let (step, e) = s.next();
        assert_eq!(s.advance(step, &ok(&e)), 0);
    }
    let terminal = s.next();
    assert_eq!(s.advance(terminal.0, &[0]), -2);
    assert_eq!(s.next(), terminal);
    let old = s.0;
    assert_eq!(smartvpn_settings_flow_close_v1(old), 0);
    s.0 = 0;
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_settings_flow_next_v1(old, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
    assert_eq!(smartvpn_settings_flow_close_v1(old), -2);
    assert_eq!(
        unsafe { smartvpn_settings_flow_advance_v1(old, 1, [0].as_ptr(), 1) },
        -2
    );
    let fresh = Session::start(&frame(0, 0, [0; 13]));
    assert_ne!(fresh.0, old);
}
#[test]
fn capacity_released_and_invalid_frames_do_not_take_slots() {
    let _guard = SERIAL.lock().unwrap();
    let b = frame(0, 0, [0; 13]);
    for invalid in [
        vec![],
        b[..16].to_vec(),
        {
            let mut v = b.clone();
            v.push(0);
            v
        },
        frame(0x800, 0, [0; 13]),
        frame(0, 0x800, [0; 13]),
        {
            let mut v = b.clone();
            v[4] = 2;
            v
        },
    ] {
        let mut id = 77;
        assert_eq!(
            unsafe { smartvpn_settings_flow_start_v1(invalid.as_ptr(), invalid.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    let mut id = 77;
    assert_eq!(
        unsafe { smartvpn_settings_flow_start_v1(b.as_ptr(), 1025, &mut id) },
        -1
    );
    assert_eq!(id, 0);
    let mut sessions: Vec<_> = (0..8).map(|_| Session::start(&b)).collect();
    assert_eq!(
        unsafe { smartvpn_settings_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    drop(sessions.pop());
    let replacement = Session::start(&b);
    assert_eq!(replacement.next().0, 1);
}
#[test]
fn dynamic_cache_and_save_branches_have_exact_effect_order() {
    let _guard = SERIAL.lock().unwrap();
    for cache_found in [false, true] {
        for cache_ok in [false, true] {
            for store in [false, true] {
                for final_ok in [false, true] {
                    let mut flags = [0; 13];
                    flags[8] = 1;
                    flags[10] = store as u8;
                    flags[11] = cache_found as u8;
                    flags[12] = 1;
                    let t = trace(&frame(1 << 9, 0, flags), |e| match e[0] {
                        9 => vec![1, cache_ok as u8],
                        7 => vec![1, if e[1] == 1 { final_ok as u8 } else { 0 }],
                        _ => ok(e),
                    });
                    let mut expected = vec![vec![1, 0], vec![2, 0, 2, 0, 0, 0], vec![3], vec![4]];
                    if store {
                        expected.extend([vec![5], vec![6]]);
                    }
                    expected.extend([vec![7, 0], vec![8, 0]]);
                    if cache_found {
                        expected.push(vec![9]);
                    }
                    if cache_found && cache_ok {
                        expected.push(vec![8, 1]);
                    } else {
                        expected.extend([vec![10, 0], vec![10, 1], vec![10, 2]]);
                    }
                    expected.extend([
                        vec![11],
                        vec![12],
                        vec![7, 1],
                        vec![13, if final_ok { 0 } else { 10 }],
                    ]);
                    assert_eq!(t, expected);
                }
            }
        }
    }
    for (changed, merged) in [(false, false), (true, true)] {
        let mut flags = [0; 13];
        flags[8] = changed as u8;
        flags[9] = merged as u8;
        assert_eq!(
            trace(&frame(0, 0, flags), ok),
            vec![vec![1, 0], vec![2, 0, 0, 0, 0, 0], vec![7, 1], vec![13, 0]]
        );
    }
}
#[test]
fn runtime_queries_validation_priority_and_presence_survive_wire() {
    let _guard = SERIAL.lock().unwrap();
    let mut queries = 0;
    let t = trace(&frame(1, 0, [0; 13]), |e| {
        if e[0] == 1 {
            queries += 1;
            vec![1, (queries == 2) as u8]
        } else {
            ok(e)
        }
    });
    assert_eq!(t, vec![vec![1, 1], vec![1, 0], vec![13, 9]]);
    assert_eq!(
        trace(&frame(0x7ff, 0x7ff, [0; 13]), |e| if e[0] == 1 {
            vec![1, 1]
        } else {
            ok(e)
        }),
        vec![vec![1, 1], vec![13, 1]]
    );
    for (bit, code) in [(1, 2), (2, 4), (7, 5), (8, 6), (9, 7), (10, 8)] {
        assert_eq!(
            trace(&frame(1 << bit, 1 << bit, [0; 13]), ok),
            vec![vec![13, code]]
        );
    }
    // Explicit false values override true defaults; absence preserves them.
    let mut flags = [0; 13];
    flags[1] = 1;
    flags[2] = 1;
    flags[3] = 1;
    assert_eq!(trace(&frame(0, 0, flags), ok)[1], vec![2, 0, 0, 1, 1, 1]);
    assert_eq!(
        trace(&frame((1 << 4) | (1 << 5) | (1 << 6), 0, flags), ok)[1],
        vec![2, 112, 0, 0, 0, 0]
    );
}
