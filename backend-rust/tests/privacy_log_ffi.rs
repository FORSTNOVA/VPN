use smartvpn_core::privacy_log::{Reply, Request};
use smartvpn_core::privacy_log_ffi::*;
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
const LIMIT: usize = 64 << 20;
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
            unsafe { smartvpn_privacy_log_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_privacy_log_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_privacy_log_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn raw(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_privacy_log_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
    fn advance(&self, q: Reply) -> i32 {
        self.raw(self.next().0, &reply(&q))
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        smartvpn_privacy_log_close_v1(self.0);
    }
}
fn kind(w: &[u8]) -> &str {
    let n = u64::from_le_bytes(w[..8].try_into().unwrap()) as usize;
    std::str::from_utf8(&w[8..8 + n]).unwrap()
}
#[test]
fn malformed_start_and_reply_never_consume() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_privacy_log_version_v1(), 1);
    let good = request(&Request {
        op: "scrub".into(),
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
            unsafe { smartvpn_privacy_log_start_v1(b.as_ptr(), b.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    let s = Session::new("scrub");
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
    let s = Session::new("scrub");
    let before = s.next();
    let mut out = vec![0xa5; before.1.len() - 1];
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe {
            smartvpn_privacy_log_next_v1(s.0, out.as_mut_ptr(), out.len(), &mut n, &mut step)
        },
        -3
    );
    assert_eq!((n, step), (before.1.len(), 1));
    assert!(out.iter().all(|b| *b == 0xa5));
    assert_eq!(s.next(), before);
    assert_eq!(
        unsafe { smartvpn_privacy_log_next_v1(0, out.as_mut_ptr(), out.len(), &mut n, &mut step) },
        -2
    );
    assert_eq!((n, step), (0, 0));
    assert_eq!(
        unsafe { smartvpn_privacy_log_next_v1(s.0, ptr::null_mut(), 1, &mut n, &mut step) },
        -1
    );
    assert_eq!(
        unsafe {
            smartvpn_privacy_log_next_v1(s.0, ptr::null_mut(), 0, ptr::null_mut(), &mut step)
        },
        -1
    );
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_privacy_log_start_v1(ptr::null(), 1, &mut id) },
        -1
    );
    assert_eq!(id, 0);
    assert_eq!(
        unsafe { smartvpn_privacy_log_start_v1(ptr::null(), 0, ptr::null_mut()) },
        -1
    );
    assert_eq!(
        unsafe { smartvpn_privacy_log_advance_v1(s.0, 1, ptr::null(), 1) },
        -1
    );
    assert_eq!(s.next(), before);
    let b = reply(&Reply::default());
    assert_eq!(
        unsafe { smartvpn_privacy_log_advance_v1(0, 1, b.as_ptr(), b.len()) },
        -2
    );
}
#[test]
fn capacity64_close_releases_without_reusing_identity() {
    let _g = SERIAL.lock().unwrap();
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new("scrub")).collect();
    let b = request(&Request {
        op: "scrub".into(),
        ..Default::default()
    });
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_privacy_log_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let oldid = old.0;
    drop(old);
    assert_eq!(smartvpn_privacy_log_close_v1(oldid), -2);
    let new = Session::new("scrub");
    assert_ne!(new.0, oldid);
    assert_eq!(new.next().0, 1);
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe { smartvpn_privacy_log_next_v1(oldid, ptr::null_mut(), 0, &mut n, &mut step) },
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
fn hex(v: &[u8]) -> String {
    v.iter().map(|b| format!("{b:02x}")).collect()
}
fn effect_text(b: &[u8]) -> &str {
    let n = u64::from_le_bytes(b[..8].try_into().unwrap()) as usize;
    let p = 8 + n;
    let n = u64::from_le_bytes(b[p..p + 8].try_into().unwrap()) as usize;
    std::str::from_utf8(&b[p + 8..p + 8 + n]).unwrap()
}
#[test]
fn frame64_and_cumulative128_mib_are_atomic() {
    let _g = SERIAL.lock().unwrap();
    let base = request(&Request {
        op: "scrub".into(),
        ..Default::default()
    })
    .len();
    let s = Session::from_request(Request {
        op: "scrub".into(),
        path: "x".repeat(LIMIT - base),
    });
    let overhead = reply(&Reply::default()).len();
    let wire = reply(&Reply {
        bytes: vec![0; LIMIT - overhead],
        ..Default::default()
    });
    assert_eq!(wire.len(), LIMIT);
    let mut too = wire.clone();
    too.push(0);
    assert_eq!(s.raw(1, &too), -1);
    assert_eq!(s.next().0, 1);
    assert_eq!(s.raw(1, &wire), 0);
    let before = s.next();
    assert_eq!(s.advance(Reply::default()), -4);
    assert_eq!(s.next(), before);
    let q = request(&Request {
        op: "scrub".into(),
        path: "x".repeat(LIMIT - base + 1),
    });
    let mut id = 9;
    assert_eq!(
        unsafe { smartvpn_privacy_log_start_v1(q.as_ptr(), q.len(), &mut id) },
        -1
    );
    assert_eq!(id, 0);
}
#[test]
fn invalid_hex_and_raw_utf8_bytes_trim_dedup() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("secrets");
    let before = s.next();
    for text in ["0", "gg", "é"] {
        assert_eq!(
            s.advance(Reply {
                names: vec![text.into()],
                ..Default::default()
            }),
            -2
        );
        assert_eq!(s.next(), before);
    }
    let mut raw = vec![255];
    raw.extend_from_slice(b"123456789012");
    let mut spaced = "\u{0085}".as_bytes().to_vec();
    spaced.extend_from_slice(&raw);
    spaced.extend_from_slice("\u{3000}".as_bytes());
    assert_eq!(
        s.advance(Reply {
            names: vec![hex(&spaced), hex(&raw), hex(b"short"), hex(b"abcdefghijkl")],
            ..Default::default()
        }),
        0
    );
    let (_, b) = s.next();
    assert_eq!(kind(&b), "result");
    assert!(output(&b).contains("\"indices\":[0,3]"));
}
#[test]
fn write_raw_bytes_and_sink_failure_preserve_original_count() {
    let _g = SERIAL.lock().unwrap();
    for fail in [false, true] {
        let s = Session::new("write");
        assert_eq!(
            s.advance(Reply {
                bytes: vec![255, 0, 255],
                names: vec!["ff".into()],
                ..Default::default()
            }),
            0
        );
        let (_, b) = s.next();
        assert_eq!(kind(&b), "sink");
        assert_eq!(effect_text(&b), hex(b"<redacted>\0<redacted>"));
        assert_eq!(
            s.advance(Reply {
                error: fail.then(|| "sink failed".into()),
                ..Default::default()
            }),
            0
        );
        let (_, b) = s.next();
        let out = output(&b);
        assert!(out.contains(if fail { "\"status\":0" } else { "\"status\":3" }));
        assert!(out.contains(if fail {
            "\"hasError\":true"
        } else {
            "\"hasError\":false"
        }));
        assert_eq!(s.advance(Reply::default()), 0);
        let before = s.next();
        assert_eq!(kind(&before.1), "done");
        assert_eq!(s.advance(Reply::default()), -2);
        assert_eq!(s.next(), before);
    }
}
#[test]
fn scrub_large_log_keeps_tail_and_redacts_without_real_io() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("scrub");
    let secret = b"secret-token-123";
    assert_eq!(
        s.advance(Reply {
            names: vec![hex(secret)],
            ..Default::default()
        }),
        0
    );
    let mut body = vec![b'x'; 9 << 20];
    body.push(b'\n');
    body.extend_from_slice(b"tail ");
    body.extend_from_slice(secret);
    body.extend_from_slice(b" end\n");
    assert_eq!(
        s.advance(Reply {
            bytes: body,
            ..Default::default()
        }),
        0
    );
    let (_, b) = s.next();
    assert_eq!(kind(&b), "write-temp");
    // No earlier newline exists before the cutoff: preserve exactly the last 8 MiB,
    // including the surviving partial line, then redact the terminal secret.
    let text = effect_text(&b);
    assert_eq!(
        text.len(),
        2 * ((8 << 20) - secret.len() + b"<redacted>".len())
    );
    assert!(text.starts_with("78787878"));
    assert!(text.ends_with(&hex(b"\ntail <redacted> end\n")));
    assert_eq!(s.advance(Reply::default()), 0);
    assert_eq!(kind(&s.next().1), "rename");
    assert_eq!(s.advance(Reply::default()), 0);
    let (_, b) = s.next();
    assert_eq!(kind(&b), "log-summary");
    assert!(effect_text(&b).contains("redacted 1 subscription URL(s)"));
    assert_eq!(s.advance(Reply::default()), 0);
    assert_eq!(kind(&s.next().1), "result");
}
#[test]
fn oversized_hex_effect_is_bounded_and_cancellable() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("write");
    assert_eq!(
        s.advance(Reply {
            bytes: vec![0; 32 << 20],
            ..Default::default()
        }),
        0
    );
    let (mut n, mut step) = (99, 99);
    for _ in 0..2 {
        assert_eq!(
            unsafe { smartvpn_privacy_log_next_v1(s.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -4
        );
        assert_eq!((n, step), (0, 0));
    }
    assert_eq!(smartvpn_privacy_log_close_v1(s.0), 0);
    assert_eq!(smartvpn_privacy_log_close_v1(s.0), -2);
}
