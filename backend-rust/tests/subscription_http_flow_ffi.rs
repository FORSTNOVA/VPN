use smartvpn_core::{
    subscription_http_flow::{Reply, Request},
    subscription_http_flow_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s)
}
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![
        r.kind,
        r.ok as u8,
        r.flag as u8,
        r.clash as u8,
        r.json as u8,
        r.uri as u8,
        r.info as u8,
        r.host as u8,
        r.path as u8,
    ];
    for v in [r.count, r.other] {
        b.extend_from_slice(&v.to_le_bytes())
    }
    for s in [&r.text, &r.network, &r.tls] {
        bytes(&mut b, s)
    }
    b.extend_from_slice(&(r.names.len() as u64).to_le_bytes());
    for s in r.names {
        bytes(&mut b, &s)
    }
    b.extend_from_slice(&(r.nodes.len() as u64).to_le_bytes());
    for n in r.nodes {
        b.push(n.vmess as u8);
        bytes(&mut b, &n.network);
        b.extend([n.tls as u8, n.ws_host as u8, n.ws_path as u8])
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
        let b = [q.op];
        let mut h = 0;
        assert_eq!(
            unsafe { smartvpn_subscription_http_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
            0
        );
        Self(h)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut st) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_subscription_http_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut st)
            },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_subscription_http_flow_next_v1(
                    self.0,
                    b.as_mut_ptr(),
                    b.len(),
                    &mut n,
                    &mut st,
                )
            },
            0
        );
        (st, b)
    }
    fn reply(&self, st: u64, r: Reply) -> i32 {
        let b = wire(r);
        unsafe { smartvpn_subscription_http_flow_advance_v1(self.0, st, b.as_ptr(), b.len()) }
    }
}
impl Drop for S {
    fn drop(&mut self) {
        assert_eq!(smartvpn_subscription_http_flow_close_v1(self.0), 0)
    }
}
#[test]
fn repeated_next_stale_step_and_rejected_reply() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request { op: 3 });
    let (st, e) = s.next();
    assert_eq!(s.next(), (st, e.clone()));
    assert_eq!(s.reply(st, ok(2)), -2);
    assert_eq!(s.next(), (st, e));
    assert_eq!(s.reply(st, ok(23)), 0);
    assert_eq!(s.reply(st, ok(23)), -5);
    assert_eq!(s.next().1[0], 63)
}
#[test]
fn capacity_release_and_closed_handle() {
    let _g = SERIAL.lock().unwrap();
    let mut v = vec![];
    for _ in 0..64 {
        v.push(S::new(Request { op: 4 }))
    }
    let mut h = 0;
    assert_eq!(
        unsafe { smartvpn_subscription_http_flow_start_v1([4].as_ptr(), 1, &mut h) },
        -4
    );
    let old = v.pop().unwrap().0;
    assert_eq!(smartvpn_subscription_http_flow_close_v1(old), -2);
    let _s = S::new(Request { op: 4 });
}
#[test]
fn long_loop_no_history_cap_or_timer_rebuild() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request { op: 6 });
    let (st, e) = s.next();
    assert_eq!(e[0], 38);
    assert_eq!(s.reply(st, ok(38)), 0);
    for _ in 0..50001 {
        let (st, e) = s.next();
        assert_eq!(e[0], 39);
        assert_eq!(
            s.reply(
                st,
                Reply {
                    flag: true,
                    ..ok(39)
                }
            ),
            0
        );
        let (st, e) = s.next();
        assert_eq!(e[0], 41);
        assert_eq!(s.reply(st, ok(41)), 0)
    }
    let (st, _) = s.next();
    assert_eq!(s.reply(st, ok(39)), 0);
    let (st, e) = s.next();
    assert_eq!(e[0], 40);
    assert_eq!(s.reply(st, ok(40)), 0);
    assert_eq!(s.next().1[0], 63)
}
#[test]
fn malformed_and_oversize_wire_do_not_advance() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request { op: 3 });
    let (st, e) = s.next();
    let b = wire(ok(23));
    for end in 0..b.len() {
        assert_ne!(
            unsafe { smartvpn_subscription_http_flow_advance_v1(s.0, st, b.as_ptr(), end) },
            0
        )
    }
    let mut bad = b.clone();
    bad[1] = 2;
    assert_ne!(
        unsafe { smartvpn_subscription_http_flow_advance_v1(s.0, st, bad.as_ptr(), bad.len()) },
        0
    );
    let large = vec![0; 8 * 1024 * 1024 + 1];
    assert_ne!(
        unsafe { smartvpn_subscription_http_flow_advance_v1(s.0, st, large.as_ptr(), large.len()) },
        0
    );
    assert_eq!(s.next(), (st, e));
}
#[test]
fn diagnostics_child_and_cleanup() {
    let _g = SERIAL.lock().unwrap();
    let s = S::new(Request { op: 2 });
    let mut trace = vec![];
    for _ in 0..60 {
        let (st, e) = s.next();
        let k = e[0];
        if k == 63 {
            break;
        }
        trace.push(k);
        let mut r = ok(k);
        if k == 11 {
            r.flag = true
        }
        if k == 20 {
            r.ok = false
        }
        assert_eq!(s.reply(st, r), 0)
    }
    assert_eq!(&trace[..3], [1, 11, 2]);
    assert_eq!(&trace[trace.len() - 2..], [32, 33]);
    assert!(trace.contains(&23));
    assert!(trace.contains(&31));
}
