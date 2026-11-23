use smartvpn_core::{
    app_flow::{Health, Region, Reply, Request},
    app_flow_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn start(q: Request) -> Vec<u8> {
    let mut b = vec![q.op];
    for v in q.params {
        b.extend_from_slice(&v.to_le_bytes())
    }
    bytes(&mut b, &q.limit);
    b.extend_from_slice(&q.interval.to_le_bytes());
    b
}
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![
        r.kind,
        r.ok as u8,
        r.flag as u8,
        r.other as u8,
        r.proxy_on as u8,
        r.webrtc as u8,
        r.dns as u8,
        r.job_nil as u8,
        r.chain_enabled as u8,
        r.mode_supported as u8,
    ];
    for s in [
        &r.selection_mode,
        &r.selected_node,
        &r.locked_region,
        &r.locked_node,
        &r.mode,
        &r.default_mode,
        &r.text,
    ] {
        bytes(&mut b, s)
    }
    b.extend_from_slice(&r.count.to_le_bytes());
    for v in r.params {
        b.extend_from_slice(&v.to_le_bytes())
    }
    b.extend_from_slice(&(r.health.len() as u64).to_le_bytes());
    for h in r.health {
        bytes(&mut b, &h.state);
        b.push(h.cooldown as u8)
    }
    b.extend_from_slice(&(r.regions.len() as u64).to_le_bytes());
    for v in r.regions {
        bytes(&mut b, &v.country);
        b.extend([v.verified as u8, v.expired as u8, v.cooldown as u8])
    }
    b
}
fn ok(k: u8) -> Reply {
    Reply {
        kind: k,
        ok: true,
        ..Default::default()
    }
}
struct S(u64);
impl S {
    fn new(q: Request) -> Self {
        let b = start(q);
        let mut h = 0;
        assert_eq!(
            unsafe { smartvpn_app_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        Self(h)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut st) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_app_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut st) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe { smartvpn_app_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut st) },
            0
        );
        (st, b)
    }
    fn reply(&self, st: u64, r: Reply) -> i32 {
        let b = wire(r);
        unsafe { smartvpn_app_flow_advance_v1(self.0, st, b.as_ptr(), b.len()) }
    }
}
impl Drop for S {
    fn drop(&mut self) {
        assert_eq!(smartvpn_app_flow_close_v1(self.0), 0)
    }
}
#[test]
fn readonly_next_rejected_shape_and_stale_step_preserve_effect() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request::default());
    let a = s.next();
    assert_eq!(a, s.next());
    assert_ne!(s.reply(a.0, ok(3)), 0);
    assert_eq!(s.next(), a);
    let mut b = wire(ok(1));
    b[1] = 2;
    assert_ne!(
        unsafe { smartvpn_app_flow_advance_v1(s.0, a.0, b.as_ptr(), b.len()) },
        0
    );
    assert_eq!(s.next(), a);
    assert_eq!(s.reply(a.0, ok(1)), 0);
    let next = s.next();
    assert_ne!(s.reply(a.0, ok(1)), 0);
    assert_eq!(s.next(), next);
}
#[test]
fn cap64_close_reuse_and_invalid_handles() {
    let _g = SERIAL.lock().unwrap();
    let mut v = (0..64)
        .map(|_| S::new(Request::default()))
        .collect::<Vec<_>>();
    let b = start(Request::default());
    let mut h = 7;
    assert_eq!(
        unsafe { smartvpn_app_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -4
    );
    assert_eq!(h, 0);
    let old = v.pop().unwrap();
    let id = old.0;
    drop(old);
    let fresh = S::new(Request::default());
    assert_ne!(id, fresh.0);
    assert_ne!(smartvpn_app_flow_close_v1(id), 0);
}
#[test]
fn long_loop_uses_single_timer_and_no_step_limit() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 7,
        interval: 99,
        ..Default::default()
    });
    let (st, b) = s.next();
    assert_eq!(b[0], 33);
    assert_eq!(s.reply(st, ok(33)), 0);
    for _ in 0..50001 {
        let (st, b) = s.next();
        assert_eq!(b[0], 34);
        assert_eq!(
            s.reply(
                st,
                Reply {
                    flag: true,
                    ..ok(34)
                }
            ),
            0
        );
        let (st, b) = s.next();
        assert_eq!(b[0], 41);
        assert_eq!(s.reply(st, ok(41)), 0)
    }
    let (st, b) = s.next();
    assert_eq!(b[0], 34);
    assert!(st > 100000);
    assert_eq!(s.reply(st, ok(34)), 0);
    let (st, b) = s.next();
    assert_eq!(b[0], 35);
    assert_eq!(s.reply(st, ok(35)), 0);
    assert_eq!(s.next().1[0], 63);
}
#[test]
fn hundred_thousand_records_snapshot_has_small_output_and_no_repeated_queries() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 1,
        ..Default::default()
    });
    let (st, b) = s.next();
    assert_eq!(b[0], 13);
    assert_eq!(
        s.reply(
            st,
            Reply {
                health: vec![
                    Health {
                        state: b"OTHER".to_vec(),
                        cooldown: true
                    };
                    100000
                ],
                regions: vec![
                    Region {
                        country: b"JP".to_vec(),
                        verified: true,
                        ..Default::default()
                    };
                    100000
                ],
                ..ok(13)
            }
        ),
        0
    );
    for _ in 0..2 {
        let (st, b) = s.next();
        assert_eq!(b[0], 14);
        assert_eq!(
            s.reply(
                st,
                Reply {
                    text: b"JP".to_vec(),
                    ..ok(14)
                }
            ),
            0
        )
    }
    let (_, b) = s.next();
    assert_eq!(b[0], 63);
    assert!(b.len() < 512);
}
#[test]
fn invalid_input_size_opcode_and_trailing_bytes_fail_before_effects() {
    let _g = SERIAL.lock().unwrap();
    let mut h = 7;
    for b in [
        start(Request {
            op: 255,
            ..Default::default()
        }),
        {
            let mut b = start(Request::default());
            b.push(0);
            b
        },
        vec![0; (8 << 20) + 1],
    ] {
        assert_ne!(
            unsafe { smartvpn_app_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        assert_eq!(h, 0);
    }
}
#[test]
fn params_save_failure_has_no_restart_and_keeps_commit_before_response() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 2,
        ..Default::default()
    });
    let mut t = vec![];
    loop {
        let (st, b) = s.next();
        let k = b[0];
        if k == 63 {
            break;
        }
        t.push(k);
        let r = if k == 18 {
            Reply {
                flag: true,
                ..ok(k)
            }
        } else if k == 19 {
            Reply {
                kind: k,
                ..Default::default()
            }
        } else {
            ok(k)
        };
        assert_eq!(s.reply(st, r), 0)
    }
    assert_eq!(t, vec![1, 17, 18, 19, 15, 2]);
}
