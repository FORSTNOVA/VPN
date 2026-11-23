use smartvpn_core::embedded_kernel::{Reply, Request};
use smartvpn_core::embedded_kernel_ffi::*;
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
    string(&mut v, &q.path);
    v
}
fn reply(q: &Reply) -> Vec<u8> {
    let mut v = vec![];
    string(&mut v, &q.text);
    v.extend_from_slice(&q.number.to_le_bytes());
    v.push(q.flag as u8);
    list(&mut v, &q.names);
    bytes(&mut v, &q.bytes);
    v.push(q.error.is_some() as u8);
    string(&mut v, q.error.as_deref().unwrap_or(""));
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
            unsafe { smartvpn_embedded_kernel_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe {
                smartvpn_embedded_kernel_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step)
            },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_embedded_kernel_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn raw(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_embedded_kernel_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
    fn advance(&self, q: Reply) -> i32 {
        self.raw(self.next().0, &reply(&q))
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        smartvpn_embedded_kernel_close_v1(self.0);
    }
}
fn kind(w: &[u8]) -> &str {
    let n = u64::from_le_bytes(w[..8].try_into().unwrap()) as usize;
    std::str::from_utf8(&w[8..8 + n]).unwrap()
}
#[test]
fn malformed_start_and_reply_never_consume() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_embedded_kernel_version_v1(), 1);
    let good = request(&Request {
        op: "start".into(),
        ..Default::default()
    });
    let mut bads: Vec<_> = (0..good.len()).map(|n| good[..n].to_vec()).collect();
    let mut trailing = good.clone();
    trailing.push(0);
    bads.push(trailing);
    let mut utf = good.clone();
    utf[8] = 255;
    bads.push(utf);
    let mut count = good;
    count[..8].copy_from_slice(&u64::MAX.to_le_bytes());
    bads.push(count);
    bads.push(request(&Request {
        op: "unknown".into(),
        ..Default::default()
    }));
    for b in bads {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_embedded_kernel_start_v1(b.as_ptr(), b.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    let s = Session::new("start");
    let before = s.next();
    let good = reply(&Reply::default());
    let mut bads: Vec<_> = (0..good.len()).map(|n| good[..n].to_vec()).collect();
    let mut trailing = good.clone();
    trailing.push(0);
    bads.push(trailing);
    let mut boolean = good.clone();
    boolean[16] = 2;
    bads.push(boolean);
    let mut utf = reply(&Reply {
        text: "x".into(),
        ..Default::default()
    });
    utf[8] = 255;
    bads.push(utf);
    let mut count = good.clone();
    count[17..25].copy_from_slice(&100001u64.to_le_bytes());
    bads.push(count);
    for b in bads {
        assert_eq!(s.raw(1, &b), -2);
        assert_eq!(s.next(), before);
    }
    for step in [0, 2, u64::MAX] {
        assert_eq!(s.raw(step, &good), -5);
        assert_eq!(s.next(), before);
    }
    assert_eq!(s.raw(1, &good), 0);
    let after = s.next();
    assert_eq!(after.0, 2);
    assert_eq!(s.raw(1, &good), -5);
    assert_eq!(s.next(), after);
}
#[test]
fn next_capacity_invalid_identity_and_pointer_boundaries() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("start");
    let before = s.next();
    let mut out = vec![0xa5; before.1.len() - 1];
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe {
            smartvpn_embedded_kernel_next_v1(s.0, out.as_mut_ptr(), out.len(), &mut n, &mut step)
        },
        -3
    );
    assert_eq!((n, step), (before.1.len(), 1));
    assert!(out.iter().all(|b| *b == 0xa5));
    assert_eq!(s.next(), before);
    assert_eq!(
        unsafe {
            smartvpn_embedded_kernel_next_v1(0, out.as_mut_ptr(), out.len(), &mut n, &mut step)
        },
        -2
    );
    assert_eq!((n, step), (0, 0));
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_next_v1(s.0, ptr::null_mut(), 1, &mut n, &mut step) },
        -1
    );
    assert_eq!(
        unsafe {
            smartvpn_embedded_kernel_next_v1(s.0, ptr::null_mut(), 0, ptr::null_mut(), &mut step)
        },
        -1
    );
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_start_v1(ptr::null(), 1, &mut id) },
        -1
    );
    assert_eq!(id, 0);
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_start_v1(ptr::null(), 0, ptr::null_mut()) },
        -1
    );
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_advance_v1(s.0, 1, ptr::null(), 1) },
        -1
    );
    assert_eq!(s.next(), before);
    let b = reply(&Reply::default());
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_advance_v1(0, 1, b.as_ptr(), b.len()) },
        -2
    );
}
#[test]
fn capacity64_close_releases_without_reusing_identity() {
    let _g = SERIAL.lock().unwrap();
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new("start")).collect();
    let b = request(&Request {
        op: "start".into(),
        ..Default::default()
    });
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let oldid = old.0;
    drop(old);
    assert_eq!(smartvpn_embedded_kernel_close_v1(oldid), -2);
    let new = Session::new("start");
    assert_ne!(new.0, oldid);
    assert_eq!(new.next().0, 1);
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_next_v1(oldid, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
    assert_eq!((n, step), (0, 0));
}
fn output(w: &[u8]) -> &str {
    let mut c = 0;
    for _ in 0..3 {
        let n = u64::from_le_bytes(w[c..c + 8].try_into().unwrap()) as usize;
        c += 8 + n;
    }
    c += 8;
    let n = u64::from_le_bytes(w[c..c + 8].try_into().unwrap()) as usize;
    c += 8;
    assert_eq!(c + n, w.len());
    std::str::from_utf8(&w[c..]).unwrap()
}
fn number(b: &[u8]) -> i64 {
    let mut c = 0;
    for _ in 0..3 {
        let n = u64::from_le_bytes(b[c..c + 8].try_into().unwrap()) as usize;
        c += 8 + n;
    }
    i64::from_le_bytes(b[c..c + 8].try_into().unwrap())
}
#[test]
fn ten_operations_complete_without_external_resources() {
    let _g = SERIAL.lock().unwrap();
    for op in [
        "start",
        "stop",
        "running",
        "refusal",
        "start-logging",
        "logs",
        "event",
        "stop-logging",
        "check",
        "apply",
    ] {
        let s = Session::new(op);
        let mut completed = false;
        for _ in 0..30 {
            let before = s.next();
            if kind(&before.1) == "result" {
                assert!(output(&before.1).contains("\"hasError\":false"));
                assert_eq!(s.advance(Reply::default()), -2);
                assert_eq!(s.next(), before);
                completed = true;
                break;
            }
            assert_eq!(s.advance(Reply::default()), 0);
        }
        assert!(completed, "{op}");
    }
}
#[test]
fn start_commit_order_and_error_cleanup_order() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("start");
    for k in [
        "chdir",
        "home",
        "mkdir",
        "read-config",
        "start-logging",
        "hub-parse",
        "refusal",
        "handover",
        "lock",
        "set-running",
        "unlock",
    ] {
        let (_, b) = s.next();
        assert_eq!(kind(&b), k);
        if k == "mkdir" {
            assert_eq!(number(&b), 0o700);
        }
        if k == "set-running" {
            assert_eq!(number(&b), 1);
        }
        assert_eq!(s.advance(Reply::default()), 0);
    }
    assert_eq!(kind(&s.next().1), "result");
    let s = Session::new("start");
    for k in [
        "chdir",
        "home",
        "mkdir",
        "read-config",
        "start-logging",
        "hub-parse",
    ] {
        assert_eq!(kind(&s.next().1), k);
        assert_eq!(s.advance(Reply::default()), 0);
    }
    assert_eq!(kind(&s.next().1), "refusal");
    assert_eq!(
        s.advance(Reply {
            error: Some("synthetic\"\nerror".into()),
            ..Default::default()
        }),
        0
    );
    for k in ["shutdown", "stop-logging"] {
        assert_eq!(kind(&s.next().1), k);
        assert_eq!(s.advance(Reply::default()), 0);
    }
    let (_, b) = s.next();
    assert!(output(&b).contains("synthetic\\\"\\nerror"));
    assert!(output(&b).contains("\"hasError\":true"));
}
#[test]
fn long_log_stream_exceeds_old_cumulative_budget_and_remains_cancellable() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("logs");
    let r = reply(&Reply {
        flag: true,
        bytes: vec![255; 128 << 10],
        ..Default::default()
    });
    let empty = reply(&Reply::default());
    for _ in 0..256 {
        let before = s.next();
        assert_eq!(kind(&before.1), "next-event");
        assert_eq!(s.raw(before.0, &r), 0);
        let before = s.next();
        assert_eq!(kind(&before.1), "handle-event");
        assert_eq!(s.raw(before.0, &empty), 0);
    }
    assert_eq!(s.next().0, 513);
    assert_eq!(smartvpn_embedded_kernel_close_v1(s.0), 0);
    let (mut n, mut step) = (9, 9);
    assert_eq!(
        unsafe { smartvpn_embedded_kernel_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
    assert_eq!((n, step), (0, 0));
}
#[test]
fn maximum_frame_and_oversized_frame_are_atomic() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("logs");
    let header = reply(&Reply::default()).len();
    let b = reply(&Reply {
        flag: true,
        bytes: vec![255; LIMIT - header],
        ..Default::default()
    });
    assert_eq!(b.len(), LIMIT);
    let mut too = b.clone();
    too.push(0);
    let before = s.next();
    assert_eq!(s.raw(before.0, &too), -1);
    assert_eq!(s.next(), before);
    assert_eq!(s.raw(before.0, &b), 0);
    assert_eq!(kind(&s.next().1), "handle-event");
}
#[test]
fn byte_payload_is_opaque_and_hex_strings_not_decoded() {
    let _g = SERIAL.lock().unwrap();
    for text in ["ff00", "not-hex", "0", "é"] {
        let s = Session::new("logs");
        assert_eq!(
            s.advance(Reply {
                flag: true,
                text: text.into(),
                bytes: vec![255, 0, 254],
                ..Default::default()
            }),
            0
        );
        assert_eq!(kind(&s.next().1), "handle-event");
    }
}
#[test]
fn stop_logging_clear_precedes_unlock_and_resource_close() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("stop-logging");
    assert_eq!(s.advance(Reply::default()), 0);
    assert_eq!(kind(&s.next().1), "snapshot-log");
    assert_eq!(
        s.advance(Reply {
            number: 3,
            ..Default::default()
        }),
        0
    );
    for k in ["clear-log", "unlock", "unsubscribe", "close-log"] {
        assert_eq!(kind(&s.next().1), k);
        assert_eq!(s.advance(Reply::default()), 0);
    }
    assert_eq!(kind(&s.next().1), "result");
}
