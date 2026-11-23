use smartvpn_core::{
    bootstrap_flow::{Reply, Request},
    bootstrap_flow_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![
        r.kind,
        r.ok as u8,
        r.flag as u8,
        r.other as u8,
        r.elevated as u8,
        r.connected as u8,
    ];
    for n in [r.count, r.value, r.other_value, r.token] {
        b.extend_from_slice(&n.to_le_bytes())
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
        let mut b = vec![q.op, q.elevated as u8, q.enable as u8, q.authorized as u8];
        b.extend_from_slice(&q.wait.to_le_bytes());
        b.extend_from_slice(&q.shutdown_wait.to_le_bytes());
        let mut h = 0;
        assert_eq!(
            unsafe { smartvpn_bootstrap_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        Self(h)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut st) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_bootstrap_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut st) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_bootstrap_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut st)
            },
            0
        );
        (st, b)
    }
    fn reply(&self, st: u64, r: Reply) -> i32 {
        let b = wire(r);
        unsafe { smartvpn_bootstrap_flow_advance_v1(self.0, st, b.as_ptr(), b.len()) }
    }
}
impl Drop for S {
    fn drop(&mut self) {
        assert_eq!(smartvpn_bootstrap_flow_close_v1(self.0), 0)
    }
}
#[test]
fn repeated_read_and_invalid_ack_do_not_consume() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 1,
        ..Default::default()
    });
    let (st, e) = s.next();
    assert_eq!(s.next(), (st, e.clone()));
    assert_eq!(s.reply(st, ok(13)), -2);
    assert_eq!(s.next(), (st, e));
    assert_eq!(s.reply(st, ok(12)), 0);
    assert_eq!(s.reply(st, ok(12)), -5);
}
#[test]
fn cap64_release_and_no_recreation() {
    let _g = SERIAL.lock().unwrap();
    let mut sessions = vec![];
    for _ in 0..64 {
        sessions.push(S::new(Request {
            op: 1,
            ..Default::default()
        }))
    }
    let q = [0; 20];
    let mut h = 0;
    assert_eq!(
        unsafe { smartvpn_bootstrap_flow_start_v1(q.as_ptr(), q.len(), &mut h) },
        -4
    );
    let old = sessions.pop().unwrap().0;
    assert_eq!(smartvpn_bootstrap_flow_close_v1(old), -2);
    let _new = S::new(Request::default());
}
#[test]
fn lock_retry_hundred_thousand_steps_no_budget() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 1,
        ..Default::default()
    });
    for k in [12, 13] {
        let (st, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.reply(st, ok(k)), 0)
    }
    for _ in 0..40001 {
        for r in [
            Reply {
                kind: 14,
                ok: false,
                flag: true,
                ..Default::default()
            },
            Reply {
                flag: true,
                ..ok(17)
            },
            ok(18),
        ] {
            let (st, _) = s.next();
            assert_eq!(s.reply(st, r), 0)
        }
    }
    for k in [14, 15] {
        let (st, e) = s.next();
        assert_eq!(e[0], k);
        assert_eq!(s.reply(st, ok(k)), 0)
    }
    assert_eq!(s.next().1[0], 254);
}
#[test]
fn malformed_reply_and_start_fail_before_effect() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 3,
        ..Default::default()
    });
    let (st, e) = s.next();
    let b = wire(ok(4));
    for n in 0..b.len() {
        assert_ne!(
            unsafe { smartvpn_bootstrap_flow_advance_v1(s.0, st, b.as_ptr(), n) },
            0
        )
    }
    let mut bad = b.clone();
    bad[1] = 2;
    assert_ne!(
        unsafe { smartvpn_bootstrap_flow_advance_v1(s.0, st, bad.as_ptr(), bad.len()) },
        0
    );
    assert_eq!(s.next(), (st, e));
    let mut h = 0;
    let q = [255; 20];
    assert_ne!(
        unsafe { smartvpn_bootstrap_flow_start_v1(q.as_ptr(), q.len(), &mut h) },
        0
    );
}
#[test]
fn fixed_ports_cover_jni_cleanup_and_proxy_script_fallback() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request {
        op: 8,
        ..Default::default()
    });
    let (st, e) = s.next();
    assert_eq!(e[0], 32);
    assert_eq!(
        s.reply(
            st,
            Reply {
                kind: 32,
                ok: false,
                ..Default::default()
            }
        ),
        0
    );
    let (st, e) = s.next();
    assert_eq!(e[0], 79);
    assert_eq!(s.reply(st, ok(79)), 0);
    assert_eq!(s.next().1[0], 33);
    let s = S::new(Request {
        op: 16,
        ..Default::default()
    });
    let mut trace = vec![];
    for _ in 0..20 {
        let (st, e) = s.next();
        if e[0] == 254 {
            break;
        }
        let k = e[0];
        trace.push(k);
        let mut r = ok(k);
        if k == 70 {
            r.flag = true
        }
        if k == 51 {
            r.ok = false;
            r.token = 77
        }
        assert_eq!(s.reply(st, r), 0)
    }
    assert_eq!(&trace[trace.len() - 2..], [20, 66]);
}
