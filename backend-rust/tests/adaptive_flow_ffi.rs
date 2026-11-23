use smartvpn_core::adaptive_flow::Reply;
use smartvpn_core::adaptive_flow_ffi::*;
use std::sync::Mutex;
static SERIAL: Mutex<()> = Mutex::new(());
fn request(op: u8) -> Vec<u8> {
    let mut b = vec![op];
    b.extend_from_slice(&[0; 24]);
    b
}
fn reply(r: Reply) -> Vec<u8> {
    let mut b = vec![r.kind, r.ok as u8, r.flag as u8, r.other as u8];
    for v in [r.value, r.index, r.token] {
        b.extend(v.to_le_bytes())
    }
    b.push(r.event);
    for v in [r.elapsed, r.body_len] {
        b.extend(v.to_le_bytes())
    }
    b.extend([r.response as u8, r.id_match as u8, r.question_match as u8]);
    for v in [
        r.questions,
        r.answer_questions,
        r.rcode,
        r.state.len() as i64,
    ] {
        b.extend(v.to_le_bytes())
    }
    b.extend(r.state);
    b.extend(r.bits.to_le_bytes());
    b
}
struct Session(u64);
impl Drop for Session {
    fn drop(&mut self) {
        smartvpn_adaptive_flow_close_v1(self.0);
    }
}
fn open(op: u8) -> Session {
    let b = request(op);
    let mut h = 0;
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        0
    );
    Session(h)
}
fn next(s: &Session) -> (Vec<u8>, u64) {
    let mut b = vec![0; 128];
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_next_v1(s.0, b.as_mut_ptr(), b.len(), &mut n, &mut step) },
        0
    );
    b.truncate(n);
    (b, step)
}
fn send(s: &Session, r: Reply) -> i32 {
    let step = next(s).1;
    let b = reply(r);
    unsafe { smartvpn_adaptive_flow_advance_v1(s.0, step, b.as_ptr(), b.len()) }
}
fn ack(s: &Session, k: u8) {
    assert_eq!(next(s).0[0], k);
    assert_eq!(
        send(
            s,
            Reply {
                kind: k,
                ok: true,
                ..Reply::default()
            }
        ),
        0
    )
}
#[test]
fn readonly_next_strict_step_rejected_reply_atomic() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_adaptive_flow_version_v1(), 1);
    let s = open(0);
    let before = next(&s);
    assert_eq!(next(&s), before);
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_next_v1(s.0, std::ptr::null_mut(), 0, &mut n, &mut step) },
        -3
    );
    assert_eq!(n, 36);
    assert_eq!(step, before.1);
    assert_eq!(
        send(
            &s,
            Reply {
                kind: 99,
                ok: true,
                ..Reply::default()
            }
        ),
        -2
    );
    assert_eq!(next(&s), before);
    ack(&s, 1);
    let b = reply(Reply {
        kind: 1,
        ok: true,
        ..Reply::default()
    });
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_advance_v1(s.0, step, b.as_ptr(), b.len()) },
        -5
    );
    assert_eq!(next(&s).0[0], 2)
}
#[test]
fn capacity_and_closed_handles() {
    let _g = SERIAL.lock().unwrap();
    let mut sessions: Vec<_> = (0..64).map(|_| open(4)).collect();
    let b = request(4);
    let mut h = 99;
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -4
    );
    assert_eq!(h, 0);
    let gone = sessions.pop().unwrap().0;
    assert_eq!(smartvpn_adaptive_flow_close_v1(gone), -2);
    let replacement = open(4);
    assert_ne!(replacement.0, gone);
    drop(sessions)
}
#[test]
fn malformed_inputs_and_max_frame_are_preconsumption() {
    let _g = SERIAL.lock().unwrap();
    let b = request(0);
    for n in 0..b.len() {
        let mut h = 99;
        assert_eq!(
            unsafe { smartvpn_adaptive_flow_start_v1(b.as_ptr(), n, &mut h) },
            -2
        );
        assert_eq!(h, 0)
    }
    let mut h = 0;
    let b = request(12);
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -2
    );
    let s = open(0);
    let original = next(&s);
    let b = reply(Reply {
        kind: 1,
        ok: true,
        ..Reply::default()
    });
    for n in 0..b.len() {
        assert_eq!(
            unsafe { smartvpn_adaptive_flow_advance_v1(s.0, original.1, b.as_ptr(), n) },
            -2
        );
        assert_eq!(next(&s), original)
    }
    let mut bad = b;
    bad[1] = 2;
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_advance_v1(s.0, original.1, bad.as_ptr(), bad.len()) },
        -2
    );
    let b = vec![0; (8 << 20) + 1];
    assert_eq!(
        unsafe { smartvpn_adaptive_flow_advance_v1(s.0, original.1, b.as_ptr(), b.len()) },
        -1
    );
    assert_eq!(next(&s), original)
}
#[test]
fn long_monitor_has_no_step_budget_or_history() {
    let _g = SERIAL.lock().unwrap();
    let s = open(4);
    ack(&s, 34);
    for _ in 0..60_001 {
        assert_eq!(
            send(
                &s,
                Reply {
                    kind: 36,
                    ok: true,
                    flag: true,
                    ..Reply::default()
                }
            ),
            0
        );
        ack(&s, 37)
    }
    ack(&s, 36);
    ack(&s, 35);
    assert_eq!(next(&s).0[0], 254);
    assert!(next(&s).1 > 120_000);
    ack(&s, 254);
    assert_eq!(next(&s).0[0], 255)
}
#[test]
fn complete_all_ops_offline_and_health_raw_bytes() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..12 {
        let s = open(op);
        let mut steps = 0;
        let mut completions = 0;
        loop {
            let k = next(&s).0[0];
            if k == 255 {
                break;
            }
            let mut r = Reply {
                kind: k,
                ok: true,
                ..Reply::default()
            };
            match k {
                3 => r.rcode = 200,
                7 => {
                    r.bits = 7;
                    r.response = true;
                    r.id_match = true;
                    r.question_match = true;
                    r.questions = 1;
                    r.answer_questions = 1
                }
                18 | 19 => r.event = 3,
                22 => {
                    r.index = if steps % 2 == 0 { 0 } else { 1 };
                }
                26 => r.flag = true,
                27 => {
                    r.index = 7890;
                    r.value = 1
                }
                28 => r.state = vec![255, 254],
                _ => {}
            }
            if k == 22 {
                r.index = completions;
                completions += 1;
            }
            assert_eq!(send(&s, r), 0, "op {op} kind {k} step {}", next(&s).1);
            steps += 1;
            assert!(steps < 40)
        }
    }
}
