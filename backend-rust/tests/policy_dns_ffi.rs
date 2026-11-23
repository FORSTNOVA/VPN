use smartvpn_core::policy_dns::{Reply, Request};
use smartvpn_core::policy_dns_ffi::*;
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
            unsafe { smartvpn_policy_dns_start_v1(b.as_ptr(), b.len(), &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_policy_dns_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_policy_dns_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn raw(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_policy_dns_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
    fn advance(&self, q: Reply) -> i32 {
        self.raw(self.next().0, &reply(&q))
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        smartvpn_policy_dns_close_v1(self.0);
    }
}
fn kind(w: &[u8]) -> &str {
    let n = u64::from_le_bytes(w[..8].try_into().unwrap()) as usize;
    std::str::from_utf8(&w[8..8 + n]).unwrap()
}
#[test]
fn malformed_start_and_reply_never_consume() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_policy_dns_version_v1(), 1);
    let good = request(&Request {
        op: "http".into(),
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
            unsafe { smartvpn_policy_dns_start_v1(b.as_ptr(), b.len(), &mut id) },
            -2
        );
        assert_eq!(id, 0);
    }
    let s = Session::new("http");
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
    let s = Session::new("http");
    let before = s.next();
    let mut out = vec![0xa5; before.1.len() - 1];
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe { smartvpn_policy_dns_next_v1(s.0, out.as_mut_ptr(), out.len(), &mut n, &mut step) },
        -3
    );
    assert_eq!((n, step), (before.1.len(), 1));
    assert!(out.iter().all(|b| *b == 0xa5));
    assert_eq!(s.next(), before);
    assert_eq!(
        unsafe { smartvpn_policy_dns_next_v1(0, out.as_mut_ptr(), out.len(), &mut n, &mut step) },
        -2
    );
    assert_eq!((n, step), (0, 0));
    assert_eq!(
        unsafe { smartvpn_policy_dns_next_v1(s.0, ptr::null_mut(), 1, &mut n, &mut step) },
        -1
    );
    assert_eq!(
        unsafe { smartvpn_policy_dns_next_v1(s.0, ptr::null_mut(), 0, ptr::null_mut(), &mut step) },
        -1
    );
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_policy_dns_start_v1(ptr::null(), 1, &mut id) },
        -1
    );
    assert_eq!(id, 0);
    assert_eq!(
        unsafe { smartvpn_policy_dns_start_v1(ptr::null(), 0, ptr::null_mut()) },
        -1
    );
    assert_eq!(
        unsafe { smartvpn_policy_dns_advance_v1(s.0, 1, ptr::null(), 1) },
        -1
    );
    assert_eq!(s.next(), before);
    let b = reply(&Reply::default());
    assert_eq!(
        unsafe { smartvpn_policy_dns_advance_v1(0, 1, b.as_ptr(), b.len()) },
        -2
    );
}
#[test]
fn capacity64_close_releases_without_reusing_identity() {
    let _g = SERIAL.lock().unwrap();
    let mut sessions: Vec<_> = (0..64).map(|_| Session::new("http")).collect();
    let b = request(&Request {
        op: "http".into(),
        ..Default::default()
    });
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_policy_dns_start_v1(b.as_ptr(), b.len(), &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = sessions.pop().unwrap();
    let oldid = old.0;
    drop(old);
    assert_eq!(smartvpn_policy_dns_close_v1(oldid), -2);
    let new = Session::new("http");
    assert_ne!(new.0, oldid);
    assert_eq!(new.next().0, 1);
    let (mut n, mut step) = (99, 99);
    assert_eq!(
        unsafe { smartvpn_policy_dns_next_v1(oldid, ptr::null_mut(), 0, &mut n, &mut step) },
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
fn facts(v: &[&str]) -> Reply {
    Reply {
        names: v.iter().map(|s| s.to_string()).collect(),
        ..Default::default()
    }
}
#[test]
fn wire_eight_mib_and_cumulative_sixteen_mib_limits() {
    let _g = SERIAL.lock().unwrap();
    let base = request(&Request {
        op: "http".into(),
        ..Default::default()
    })
    .len();
    let s = Session::from_request(Request {
        op: "http".into(),
        path: "x".repeat(LIMIT - base),
    });
    let header = reply(&Reply::default()).len();
    let b = reply(&Reply {
        bytes: vec![0; LIMIT - header],
        ..Default::default()
    });
    assert_eq!(b.len(), LIMIT);
    let mut too = b.clone();
    too.push(0);
    assert_eq!(s.raw(1, &too), -1);
    assert_eq!(s.next().0, 1);
    assert_eq!(s.raw(1, &b), 0);
    let before = s.next();
    assert_eq!(s.advance(Reply::default()), -4);
    assert_eq!(s.next(), before);
    let q = request(&Request {
        op: "http".into(),
        path: "x".repeat(LIMIT - base + 1),
    });
    let mut id = 9;
    assert_eq!(
        unsafe { smartvpn_policy_dns_start_v1(q.as_ptr(), q.len(), &mut id) },
        -1
    );
    assert_eq!(id, 0);
}
#[test]
fn all_nine_operations_complete_and_preserve_nil_shapes() {
    let _g = SERIAL.lock().unwrap();
    for op in [
        "http", "targets", "rules", "subrules", "suffix", "merge", "domains", "hostname",
        "entrydns",
    ] {
        let s = Session::new(op);
        let mut done = false;
        let mut result = false;
        for _ in 0..32 {
            let before = s.next();
            match kind(&before.1) {
                "done" => {
                    assert_eq!(s.advance(Reply::default()), -2);
                    assert_eq!(s.next(), before);
                    done = true;
                    break;
                }
                "result" => {
                    let out = output(&before.1);
                    assert!(out.contains("\"hasError\":false"));
                    match op {
                        "rules" => assert!(out.contains("\"preview\":null,\"active\":null")),
                        "entrydns" => assert!(out.contains("\"policy\":null")),
                        "subrules" => assert!(out.contains("\"rules\":[]")),
                        "merge" | "domains" => assert!(out.contains("\"domains\":[]")),
                        "http" => assert!(out.contains("\"rules\":null")),
                        _ => {}
                    }
                    result = true;
                    assert_eq!(s.advance(Reply::default()), 0)
                }
                "source" => {
                    let r = match op {
                        "http" | "rules" => facts(&["0", "0", "0", "0"]),
                        "targets" | "subrules" => facts(&["0", "0", "0"]),
                        "suffix" => facts(&["a.example.com", "0"]),
                        "entrydns" => facts(&["0", "0"]),
                        _ => facts(&["0"]),
                    };
                    assert_eq!(s.advance(r), 0)
                }
                _ => assert_eq!(s.advance(Reply::default()), 0),
            }
        }
        assert!(result && done, "{op}");
    }
}
#[test]
fn deep_graph_and_cycles_have_bounded_stack_and_stable_outputs() {
    let _g = SERIAL.lock().unwrap();
    let depth = 6000;
    let mut names = vec!["0".into(), depth.to_string()];
    for i in 0..depth {
        names.extend([
            format!("g{i}"),
            "0".into(),
            "0".into(),
            "1".into(),
            if i + 1 == depth {
                "DIRECT".into()
            } else {
                format!("g{}", i + 1)
            },
        ]);
    }
    names.extend(["1".into(), "g0".into()]);
    let s = Session::new("targets");
    assert_eq!(
        s.advance(Reply {
            names,
            ..Default::default()
        }),
        0
    );
    let (_, b) = s.next();
    assert_eq!(kind(&b), "result");
    assert!(output(&b).contains("\"g0\":\"DIRECT\""));
    let s = Session::new("targets");
    assert_eq!(
        s.advance(facts(&[
            "0", "2", "a", "0", "0", "1", "b", "b", "0", "0", "1", "a", "2", "a", "b"
        ])),
        0
    );
    let (_, b) = s.next();
    assert!(output(&b).contains("\"a\":\"\""));
    assert!(output(&b).contains("\"b\":\"\""));
}
#[test]
fn invalid_fact_counts_do_not_consume_source() {
    let _g = SERIAL.lock().unwrap();
    for op in [
        "targets", "rules", "subrules", "suffix", "merge", "domains", "hostname", "entrydns",
    ] {
        let s = Session::new(op);
        let before = s.next();
        for r in [
            Reply::default(),
            facts(&["-1"]),
            facts(&["18446744073709551615"]),
        ] {
            assert_eq!(s.advance(r), -2, "{op}");
            assert_eq!(s.next(), before);
        }
    }
}
#[test]
fn http_matching_uses_narrow_ports_and_preserves_nil() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new("http");
    for (expected, r) in [
        ("lock", Reply::default()),
        (
            "subscription",
            Reply {
                flag: true,
                ..Default::default()
            },
        ),
        ("match-read", Reply::default()),
        (
            "match-nodes",
            Reply {
                number: 1,
                ..Default::default()
            },
        ),
        (
            "match-source",
            Reply {
                text: "__merged__".into(),
                ..Default::default()
            },
        ),
        (
            "subscription",
            Reply {
                number: 1,
                ..Default::default()
            },
        ),
        (
            "read-policy",
            Reply {
                text: "null".into(),
                ..Default::default()
            },
        ),
        ("source", facts(&["0", "0", "0", "0"])),
        (
            "enabled",
            Reply {
                flag: true,
                ..Default::default()
            },
        ),
        (
            "running",
            Reply {
                flag: true,
                ..Default::default()
            },
        ),
    ] {
        assert_eq!(kind(&s.next().1), expected);
        assert_eq!(s.advance(r), 0);
    }
    let (_, b) = s.next();
    assert_eq!(kind(&b), "result");
    let out = output(&b);
    assert!(out.contains("\"rules\":null"));
    assert!(out.contains("\"enabled\":true"));
    assert!(out.contains("\"running\":true"));
}
