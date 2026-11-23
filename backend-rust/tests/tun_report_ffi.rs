use smartvpn_core::tun_report::{Reply, Request};
use smartvpn_core::tun_report_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
const LIMIT: usize = 8 << 20;
fn bytes(v: &mut Vec<u8>, b: &[u8]) {
    v.extend_from_slice(&(b.len() as u64).to_le_bytes());
    v.extend_from_slice(b);
}
fn string(v: &mut Vec<u8>, s: &str) {
    bytes(v, s.as_bytes());
}
fn list(v: &mut Vec<u8>, l: &[String]) {
    v.extend_from_slice(&(l.len() as u64).to_le_bytes());
    for s in l {
        string(v, s);
    }
}
fn request(q: &Request) -> Vec<u8> {
    let mut v = vec![];
    string(&mut v, &q.op);
    string(&mut v, &q.name);
    v.push(q.ipv6 as u8);
    v
}
fn reply(q: &Reply) -> Vec<u8> {
    let mut v = vec![q.flag as u8];
    v.extend_from_slice(&q.number.to_le_bytes());
    string(&mut v, &q.text);
    list(&mut v, &q.names);
    list(&mut v, &q.other);
    v.push(q.error.is_some() as u8);
    string(&mut v, q.error.as_deref().unwrap_or(""));
    v.push(q.wintun.installed as u8);
    for s in [
        &q.wintun.path,
        &q.wintun.version,
        &q.wintun.sha256,
        &q.wintun.signer,
        &q.wintun.detail,
        &q.adapter.name,
    ] {
        string(&mut v, s)
    }
    v.push(q.adapter.present as u8);
    string(&mut v, &q.adapter.status);
    v.push(q.adapter.ipv4_route as u8);
    v.push(q.adapter.ipv6_route as u8);
    list(&mut v, &q.adapter.route_via);
    for s in [
        &q.exit_address,
        &q.exit_error,
        &q.region_country,
        &q.region_exit,
    ] {
        string(&mut v, s)
    }
    v.push(q.names_nil as u8);
    v.push(q.other_nil as u8);
    v
}
struct Session(u64);
impl Session {
    fn new(op: &str) -> Self {
        Self::from_request(Request {
            op: op.into(),
            ..Default::default()
        })
    }
    fn from_request(q: Request) -> Self {
        let b = request(&q);
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_tun_report_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_tun_report_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_tun_report_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn raw(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_tun_report_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
    fn advance(&self, q: Reply) -> i32 {
        self.raw(self.next().0, &reply(&q))
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        smartvpn_tun_report_close_v1(self.0);
    }
}
#[test]
fn strict_start_shapes_utf8_and_pointer_bounds() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_tun_report_version_v1(), 1);
    let good = request(&Request {
        op: "dns".into(),
        ..Default::default()
    });
    let mut bads: Vec<Vec<u8>> = (0..good.len()).map(|n| good[..n].to_vec()).collect();
    let mut trailing = good.clone();
    trailing.push(0);
    bads.push(trailing);
    let mut boolean = good.clone();
    *boolean.last_mut().unwrap() = 2;
    bads.push(boolean);
    let mut utf = good.clone();
    utf[8] = 255;
    bads.push(utf);
    bads.push(request(&Request {
        op: "unknown".into(),
        ..Default::default()
    }));
    let mut negative = good;
    negative[..8].copy_from_slice(&(-1i64).to_le_bytes());
    bads.push(negative);
    for b in bads {
        let mut id = 999;
        assert_eq!(
            unsafe { smartvpn_tun_report_start_v1(b.as_ptr(), b.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    unsafe {
        let mut id = 99;
        assert_eq!(smartvpn_tun_report_start_v1(ptr::null(), 1, &mut id), -1);
        assert_eq!(id, 0);
        assert_eq!(
            smartvpn_tun_report_start_v1(ptr::null(), LIMIT + 1, &mut id),
            -1
        );
        assert_eq!(
            smartvpn_tun_report_start_v1(ptr::null(), 0, ptr::null_mut()),
            -1
        );
    }
}
#[test]
fn strict_replies_do_not_consume_step_and_next_is_readonly() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("dns");
    let before = s.next();
    let good = reply(&Reply::default());
    let mut bads: Vec<Vec<u8>> = (0..good.len()).map(|n| good[..n].to_vec()).collect();
    let mut trailing = good.clone();
    trailing.push(0);
    bads.push(trailing);
    let mut boolean = good.clone();
    boolean[0] = 2;
    bads.push(boolean);
    let mut utf = reply(&Reply {
        text: "x".into(),
        ..Default::default()
    });
    utf[17] = 255;
    bads.push(utf);
    bads.push(reply(&Reply {
        names: vec!["x".into()],
        names_nil: true,
        ..Default::default()
    }));
    bads.push(reply(&Reply {
        other: vec!["x".into()],
        other_nil: true,
        ..Default::default()
    }));
    for b in bads {
        assert_eq!(s.raw(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    for step in [0, 2, u64::MAX] {
        assert_eq!(s.raw(step, &good), -5);
        assert_eq!(s.next(), before);
    }
    let mut out = vec![0; before.1.len() - 1];
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_tun_report_next_v1(s.0, out.as_mut_ptr(), out.len(), &mut n, &mut step) },
        -3
    );
    assert_eq!((n, step), (before.1.len(), 1));
    assert_eq!(s.next(), before);
    assert_eq!(s.raw(1, &good), 0);
    let after = s.next();
    assert_eq!(after.0, 2);
    assert_eq!(s.raw(1, &good), -5);
    assert_eq!(s.next(), after);
    assert_eq!(
        unsafe { smartvpn_tun_report_next_v1(s.0, ptr::null_mut(), 0, ptr::null_mut(), &mut step) },
        -1
    );
}
#[test]
fn capacity_64_close_frees_slot_without_reusing_old_identity() {
    let _g = SERIAL.lock().unwrap();
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new("dns")).collect();
    let b = request(&Request {
        op: "dns".into(),
        ..Default::default()
    });
    let mut id = 9;
    assert_eq!(
        unsafe { smartvpn_tun_report_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let old_id = old.0;
    drop(old);
    assert_eq!(smartvpn_tun_report_close_v1(old_id), -2);
    let replacement = Session::new("dns");
    assert_ne!(replacement.0, old_id);
    assert_eq!(replacement.next().0, 1);
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe { smartvpn_tun_report_next_v1(old_id, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
    assert_eq!((n, step), (0, 0));
    assert_eq!(
        unsafe { smartvpn_tun_report_advance_v1(old_id, 1, b.as_ptr(), b.len()) },
        -2
    );
}
#[test]
fn reply_count_256_boundary_is_atomic() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("dns");
    assert_eq!(
        s.advance(Reply {
            names: vec!["x".into(); 1000],
            ..Default::default()
        }),
        0
    );
    for _ in 1..256 {
        assert_eq!(s.advance(Reply::default()), 0);
    }
    let before = s.next();
    assert_eq!(before.0, 257);
    for _ in 0..2 {
        assert_eq!(s.advance(Reply::default()), -2);
        assert_eq!(s.next(), before);
    }
}
#[test]
fn eight_mib_wire_and_accumulated_payload_boundaries() {
    let _g = SERIAL.lock().unwrap();
    let base = request(&Request {
        op: "dns".into(),
        ..Default::default()
    })
    .len();
    let s = Session::from_request(Request {
        op: "dns".into(),
        name: "x".repeat(LIMIT - base),
        ..Default::default()
    });
    assert_eq!(s.next().0, 1);
    drop(s);
    let too = request(&Request {
        op: "dns".into(),
        name: "x".repeat(LIMIT - base + 1),
        ..Default::default()
    });
    let mut id = 9;
    assert_eq!(
        unsafe { smartvpn_tun_report_start_v1(too.as_ptr(), too.len(), &mut id) },
        -1
    );
    assert_eq!(id, 0);
    let s = Session::new("dns");
    let overhead = reply(&Reply::default()).len() + 9; // one list element: eight-byte length plus x
    let first = Reply {
        names: vec!["x".into()],
        text: "x".repeat(LIMIT - overhead),
        ..Default::default()
    };
    let wire = reply(&first);
    assert_eq!(wire.len(), LIMIT);
    let mut oversized = wire.clone();
    oversized.push(0);
    assert_eq!(s.raw(1, &oversized), -1);
    assert_eq!(s.next().0, 1);
    // Exactly 8 MiB is accepted by wire validation but rejected by retained-memory accounting.
    assert_eq!(s.raw(1, &wire), -2);
    assert_eq!(s.next().0, 1);
    let retained = std::mem::size_of::<Reply>() + 1000 * (std::mem::size_of::<String>() + 1);
    assert_eq!(
        s.advance(Reply {
            names: vec!["x".into(); 1000],
            text: "x".repeat(LIMIT - retained),
            ..Default::default()
        }),
        0
    );
    let before = s.next();
    // Even an empty reply needs struct storage once the exact retained limit is reached.
    assert_eq!(s.advance(Reply::default()), -2);
    assert_eq!(s.next(), before);
}
#[test]
fn all_operations_finish_purely_and_reject_advancing_done() {
    let _g = SERIAL.lock().unwrap();
    for op in [
        "windows-status",
        "windows-checks",
        "android-status",
        "android-checks",
        "dns",
        "node",
        "path",
        "region",
    ] {
        let s = Session::new(op);
        let mut done = false;
        for _ in 0..100 {
            let before = s.next();
            let kind_len = u64::from_le_bytes(before.1[..8].try_into().unwrap()) as usize;
            let kind = &before.1[8..8 + kind_len];
            if kind == b"done" {
                assert_eq!(s.advance(Reply::default()), -2);
                assert_eq!(s.next(), before);
                done = true;
                break;
            }
            assert_eq!(s.advance(Reply::default()), 0);
        }
        assert!(done, "{op}");
    }
}
