use smartvpn_core::{
    labels_flow::{Effect, Node, Reply, Snapshot, Source},
    labels_flow_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
fn bytes(b: &mut Vec<u8>, s: &[u8]) {
    b.extend_from_slice(&(s.len() as u64).to_le_bytes());
    b.extend_from_slice(s);
}
fn wire(r: Reply) -> Vec<u8> {
    let mut b = vec![r.kind, r.ok as u8, r.flag as u8, r.other as u8];
    b.extend_from_slice(&r.count.to_le_bytes());
    bytes(&mut b, &r.text);
    b.push(r.snapshot.is_some() as u8);
    if let Some(s) = r.snapshot {
        b.extend_from_slice(&[s.merged as u8, s.empty as u8]);
        bytes(&mut b, &s.default_label);
        b.extend_from_slice(&(s.sources.len() as u64).to_le_bytes());
        for t in s.sources {
            b.extend_from_slice(&[t.enabled as u8, t.nonempty as u8, t.matches as u8]);
            bytes(&mut b, &t.label);
        }
        b.extend_from_slice(&(s.nodes.len() as u64).to_le_bytes());
        for n in s.nodes {
            bytes(&mut b, &n.name);
            bytes(&mut b, &n.label);
            b.push(n.manual as u8);
        }
    }
    b
}
fn decode(b: &[u8]) -> Effect {
    fn take(b: &[u8], p: &mut usize) -> Vec<u8> {
        let n = u64::from_le_bytes(b[*p..*p + 8].try_into().unwrap()) as usize;
        *p += 8;
        let v = b[*p..*p + n].to_vec();
        *p += n;
        v
    }
    let mut p = 9;
    let name = take(b, &mut p);
    let text = take(b, &mut p);
    assert_eq!(p, b.len());
    Effect {
        kind: b[0],
        index: i64::from_le_bytes(b[1..9].try_into().unwrap()),
        name,
        text,
    }
}
struct Session(u64);
impl Session {
    fn new() -> Self {
        let mut id = 0;
        assert_eq!(
            unsafe { smartvpn_labels_flow_start_v1([0].as_ptr(), 1, &mut id) },
            0
        );
        Self(id)
    }
    fn next(&self) -> (u64, Vec<u8>) {
        let (mut n, mut step) = (0, 0);
        assert_eq!(
            unsafe { smartvpn_labels_flow_next_v1(self.0, ptr::null_mut(), 0, &mut n, &mut step) },
            -3
        );
        let mut b = vec![0; n];
        assert_eq!(
            unsafe {
                smartvpn_labels_flow_next_v1(self.0, b.as_mut_ptr(), b.len(), &mut n, &mut step)
            },
            0
        );
        (step, b)
    }
    fn advance(&self, step: u64, b: &[u8]) -> i32 {
        unsafe { smartvpn_labels_flow_advance_v1(self.0, step, b.as_ptr(), b.len()) }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.0 != 0 {
            assert_eq!(smartvpn_labels_flow_close_v1(self.0), 0)
        }
    }
}
fn merged(k: u8) -> Reply {
    Reply {
        kind: k,
        ok: true,
        flag: matches!(k, 5 | 10),
        other: k == 12,
        snapshot: matches!(k, 1 | 2 | 7 | 18).then(|| Snapshot {
            merged: true,
            ..Snapshot::default()
        }),
        ..Reply::default()
    }
}
fn run(mut f: impl FnMut(&Effect) -> Reply) -> Vec<Effect> {
    let s = Session::new();
    let mut result = vec![];
    loop {
        let (step, b) = s.next();
        let e = decode(&b);
        if e.kind == 0 {
            break;
        }
        let r = f(&e);
        assert_eq!(s.advance(step, &wire(r)), 0);
        result.push(e);
        assert!(result.len() < 1000)
    }
    result
}
#[test]
fn lifecycle_repeated_next_invalid_reply_stale_and_close() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_labels_flow_version_v1(), 1);
    let mut s = Session::new();
    let first = s.next();
    for _ in 0..3 {
        assert_eq!(s.next(), first)
    }
    for b in [
        wire(Reply {
            kind: 2,
            ..Reply::default()
        }),
        wire(Reply {
            kind: 1,
            ..Reply::default()
        }),
        vec![1, 2],
        {
            let mut b = wire(merged(1));
            b.push(0);
            b
        },
    ] {
        assert_eq!(s.advance(first.0, &b), -2);
        assert_eq!(s.next(), first)
    }
    let valid = wire(Reply {
        kind: 1,
        snapshot: Some(Snapshot {
            empty: true,
            ..Snapshot::default()
        }),
        ..Reply::default()
    });
    assert_eq!(s.advance(0, &valid), -5);
    assert_eq!(s.advance(first.0, &valid), 0);
    assert_eq!(s.advance(first.0, &valid), -5);
    let (step, b) = s.next();
    assert_eq!(decode(&b).kind, 63);
    assert_eq!(
        s.advance(
            step,
            &wire(Reply {
                kind: 63,
                ..Reply::default()
            })
        ),
        0
    );
    let done = s.next();
    assert_eq!(decode(&done.1).kind, 0);
    assert_eq!(s.advance(done.0, &wire(Reply::default())), -2);
    assert_eq!(s.next(), done);
    let id = s.0;
    assert_eq!(smartvpn_labels_flow_close_v1(id), 0);
    s.0 = 0;
    assert_eq!(smartvpn_labels_flow_close_v1(id), -2);
    assert_eq!(s.advance(1, &valid), -2);
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_labels_flow_next_v1(id, ptr::null_mut(), 0, &mut n, &mut step) },
        -2
    );
}
#[test]
fn capacity_limits_and_reuse() {
    let _g = SERIAL.lock().unwrap();
    for b in [vec![], vec![1], vec![0, 0], vec![0; 8 * 1024 * 1024 + 1]] {
        let mut id = 99;
        assert_eq!(
            unsafe { smartvpn_labels_flow_start_v1(b.as_ptr(), b.len(), &mut id) },
            if b.len() > 8 * 1024 * 1024 { -1 } else { -2 }
        );
        assert_eq!(id, 0)
    }
    let mut ss: Vec<_> = (0..64).map(|_| Session::new()).collect();
    let mut id = 99;
    assert_eq!(
        unsafe { smartvpn_labels_flow_start_v1([0].as_ptr(), 1, &mut id) },
        -4
    );
    assert_eq!(id, 0);
    let old = ss.pop().unwrap();
    let oldid = old.0;
    drop(old);
    let new = Session::new();
    assert_ne!(new.0, oldid);
    let first = new.next();
    let oversized = wire(Reply {
        kind: 1,
        snapshot: Some(Snapshot {
            nodes: vec![Node::default(); 100001],
            ..Snapshot::default()
        }),
        ..Reply::default()
    });
    assert_eq!(new.advance(first.0, &oversized), -2);
    assert_eq!(new.next(), first);
    assert_eq!(new.advance(first.0, &vec![0; 8 * 1024 * 1024 + 1]), -1);
    assert_eq!(new.next(), first);
}
#[test]
fn single_route_labels_only_unlabelled_nonmanual() {
    let _g = SERIAL.lock().unwrap();
    let t = run(|e| Reply {
        kind: e.kind,
        ok: true,
        snapshot: matches!(e.kind, 1 | 2).then(|| Snapshot {
            sources: vec![
                Source {
                    matches: true,
                    ..Source::default()
                },
                Source {
                    matches: true,
                    label: b"first".to_vec(),
                    ..Source::default()
                },
                Source {
                    matches: true,
                    label: b"later".to_vec(),
                    ..Source::default()
                },
            ],
            nodes: vec![
                Node::default(),
                Node {
                    manual: true,
                    ..Node::default()
                },
                Node {
                    label: b"keep".to_vec(),
                    ..Node::default()
                },
            ],
            ..Snapshot::default()
        }),
        ..Reply::default()
    });
    assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), [1, 2, 3, 63]);
    assert_eq!(t[2].text, b"first");
    assert_eq!(t[2].index, 0);
}
#[test]
fn merged_route_dynamic_keys_labels_and_ignored_save_error() {
    let _g = SERIAL.lock().unwrap();
    let t = run(|e| {
        let mut r = merged(e.kind);
        if e.kind == 6 {
            r.count = 1
        }
        if e.kind == 7 {
            r.snapshot.as_mut().unwrap().sources = vec![
                Source {
                    enabled: true,
                    nonempty: true,
                    ..Source::default()
                },
                Source {
                    nonempty: true,
                    ..Source::default()
                },
            ]
        }
        if e.kind == 12 {
            r.flag = e.name == b"dns";
            r.other = e.name != b"proxy-groups"
        }
        if e.kind == 18 {
            r.snapshot.as_mut().unwrap().nodes = vec![
                Node {
                    name: b"a".to_vec(),
                    label: b"old".to_vec(),
                    ..Node::default()
                },
                Node {
                    manual: true,
                    ..Node::default()
                },
                Node {
                    name: b"b".to_vec(),
                    ..Node::default()
                },
            ]
        }
        if e.kind == 19 && e.name == b"a" {
            r.text = b"new".to_vec()
        }
        if e.kind == 20 {
            r.ok = false
        }
        r
    });
    let copies: Vec<_> = t
        .iter()
        .filter(|e| e.kind == 13)
        .map(|e| e.name.as_slice())
        .collect();
    assert_eq!(
        copies,
        [
            b"smartvpn-source-rules".as_slice(),
            b"smartvpn-source-labels",
            b"smartvpn-policy-sections"
        ]
    );
    let labels: Vec<_> = t
        .iter()
        .filter(|e| e.kind == 3)
        .map(|e| (e.index, e.text.as_slice()))
        .collect();
    assert_eq!(labels, [(0, b"new".as_slice()), (2, b"".as_slice())]);
    assert_eq!(t[t.len() - 2].kind, 20);
    assert_eq!(t.last().unwrap().kind, 63);
}
#[test]
fn all_fallible_stages_silent_stop_or_cleanup() {
    let _g = SERIAL.lock().unwrap();
    for fail in [4, 5, 8, 9, 10, 11, 14, 15, 16] {
        let t = run(|e| {
            let mut r = merged(e.kind);
            if e.kind == fail {
                r.ok = false
            }
            r
        });
        assert_eq!(
            t.last().unwrap(),
            &Effect {
                kind: 63,
                ..Effect::default()
            }
        );
        assert_eq!(t[t.len() - 2].kind, if fail == 16 { 17 } else { fail });
        assert!(!t.iter().any(|e| e.kind == 20));
    }
    for branch in 0..4 {
        let t = run(|e| {
            let mut r = merged(e.kind);
            if branch == 0 && e.kind == 5 {
                r.flag = false
            }
            if branch == 1 && e.kind == 5 {
                r.other = true
            }
            if branch == 2 && e.kind == 6 {
                r.count = -1
            }
            if branch == 3 && e.kind == 10 {
                r.flag = false
            }
            r
        });
        assert_eq!(
            t[t.len() - 2].kind,
            match branch {
                0 | 1 => 5,
                2 => 7,
                _ => 10,
            }
        );
    }
}
#[test]
fn hundred_thousand_nodes_through_abi_without_total_step_limit() {
    let _g = SERIAL.lock().unwrap();
    let s = Session::new();
    for k in [1, 2] {
        let (step, b) = s.next();
        assert_eq!(decode(&b).kind, k);
        let r = Reply {
            kind: k,
            snapshot: Some(Snapshot {
                default_label: b"x".to_vec(),
                nodes: if k == 2 {
                    vec![Node::default(); 100000]
                } else {
                    vec![]
                },
                ..Snapshot::default()
            }),
            ..Reply::default()
        };
        let b = wire(r);
        assert!(b.len() < 8 * 1024 * 1024);
        assert_eq!(s.advance(step, &b), 0)
    }
    let ack = wire(Reply {
        kind: 3,
        ..Reply::default()
    });
    for i in 0..100000 {
        let (step, b) = s.next();
        let e = decode(&b);
        assert_eq!(e.kind, 3);
        assert_eq!(e.index, i);
        assert_eq!(e.text, b"x");
        assert_eq!(s.advance(step, &ack), 0)
    }
    let (step, b) = s.next();
    assert_eq!(decode(&b).kind, 63);
    assert_eq!(step, 100003);
    assert_eq!(
        s.advance(
            step,
            &wire(Reply {
                kind: 63,
                ..Reply::default()
            })
        ),
        0
    );
    assert_eq!(decode(&s.next().1).kind, 0);
}
