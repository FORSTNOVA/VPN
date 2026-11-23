use smartvpn_core::converter_flow::{Field, Node, Reply};
use smartvpn_core::converter_flow_ffi::*;
use std::sync::Mutex;
static SERIAL: Mutex<()> = Mutex::new(());
fn int(b: &mut Vec<u8>, v: i64) {
    b.extend(v.to_le_bytes())
}
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    int(b, v.len() as i64);
    b.extend(v)
}
fn node(b: &mut Vec<u8>, n: &Node) {
    for v in [&n.name, &n.kind, &n.network, &n.source_label] {
        bytes(b, v)
    }
    b.extend([n.tls as u8, n.ws_host as u8, n.ws_path as u8])
}
fn field(b: &mut Vec<u8>, f: &Field) {
    bytes(b, &f.key);
    int(b, f.reference);
    int(b, f.trim_ref);
    bytes(b, &f.text);
    bytes(b, &f.trim);
    b.extend([f.empty as u8, f.boolean as u8]);
    int(b, f.integer);
    int(b, f.count);
    b.push(f.trim_empty as u8);
    bytes(b, &f.lower)
}
fn reply(r: Reply) -> Vec<u8> {
    let mut b = vec![r.ok as u8];
    int(&mut b, r.token);
    int(&mut b, r.count);
    bytes(&mut b, &r.text);
    b.push(r.flag as u8);
    int(&mut b, r.tokens.len() as i64);
    for t in &r.tokens {
        int(&mut b, *t)
    }
    int(&mut b, r.fields.len() as i64);
    for f in &r.fields {
        field(&mut b, f)
    }
    node(&mut b, &r.node);
    int(&mut b, r.names.len() as i64);
    for n in &r.names {
        bytes(&mut b, n)
    }
    int(&mut b, r.error_token);
    b.extend([
        (!r.tokens.is_empty() || r.tokens_present) as u8,
        (!r.fields.is_empty() || r.fields_present) as u8,
        (!r.names.is_empty() || r.names_present) as u8,
    ]);
    b
}
fn request(op: u8, name: &[u8]) -> Vec<u8> {
    let mut b = vec![op];
    int(&mut b, 1);
    int(&mut b, 0);
    bytes(&mut b, name);
    b.push(0);
    b
}
struct Session(u64);
impl Drop for Session {
    fn drop(&mut self) {
        smartvpn_converter_flow_close_v1(self.0);
    }
}
fn open(op: u8, name: &[u8]) -> Session {
    let b = request(op, name);
    let mut h = 0;
    assert_eq!(
        unsafe { smartvpn_converter_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        0
    );
    Session(h)
}
fn next(s: &Session) -> (Vec<u8>, u64) {
    let mut b = vec![0; 2048];
    let (mut n, mut step) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_converter_flow_next_v1(s.0, b.as_mut_ptr(), b.len(), &mut n, &mut step) },
        0
    );
    b.truncate(n);
    (b, step)
}
fn kind(b: &[u8]) -> &[u8] {
    let n = i64::from_le_bytes(b[..8].try_into().unwrap()) as usize;
    &b[8..8 + n]
}
fn send(s: &Session, r: Reply) -> i32 {
    let step = next(s).1;
    let b = reply(r);
    unsafe { smartvpn_converter_flow_advance_v1(s.0, step, b.as_ptr(), b.len()) }
}
fn ok() -> Reply {
    Reply {
        ok: true,
        ..Reply::default()
    }
}
#[test]
fn step_readonly_capacity_and_closed() {
    let _g = SERIAL.lock().unwrap();
    assert_eq!(smartvpn_converter_flow_version_v1(), 2);
    let s = open(0, b"");
    let old = next(&s);
    assert_eq!(next(&s), old);
    let (mut n, mut st) = (0, 0);
    assert_eq!(
        unsafe { smartvpn_converter_flow_next_v1(s.0, std::ptr::null_mut(), 0, &mut n, &mut st) },
        -3
    );
    assert_eq!(n, old.0.len());
    assert_eq!(st, old.1);
    assert_eq!(send(&s, ok()), 0);
    let b = reply(ok());
    assert_eq!(
        unsafe { smartvpn_converter_flow_advance_v1(s.0, st, b.as_ptr(), b.len()) },
        -5
    );
    assert_eq!(kind(&next(&s).0), b"result");
    assert_eq!(smartvpn_converter_flow_close_v1(s.0), 0);
    assert_eq!(smartvpn_converter_flow_close_v1(s.0), -2);
}
#[test]
fn sessions64_and_slot_reuse() {
    let _g = SERIAL.lock().unwrap();
    let mut ss: Vec<_> = (0..64).map(|_| open(0, b"")).collect();
    let mut h = 9;
    let b = request(0, b"");
    assert_eq!(
        unsafe { smartvpn_converter_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -4
    );
    assert_eq!(h, 0);
    let gone = ss.pop().unwrap().0;
    let s = open(0, b"");
    assert_ne!(gone, s.0);
}
#[test]
fn malformed_bool_truncation_and_presence_do_not_consume() {
    let _g = SERIAL.lock().unwrap();
    let s = open(0, b"");
    let old = next(&s);
    let b = reply(ok());
    for n in 0..b.len() {
        assert_eq!(
            unsafe { smartvpn_converter_flow_advance_v1(s.0, old.1, b.as_ptr(), n) },
            -2
        );
        assert_eq!(next(&s), old)
    }
    let mut bad = b.clone();
    bad[0] = 2;
    assert_eq!(
        unsafe { smartvpn_converter_flow_advance_v1(s.0, old.1, bad.as_ptr(), bad.len()) },
        -2
    );
    let mut bad = reply(Reply {
        tokens: vec![1],
        ..ok()
    });
    let n = bad.len();
    bad[n - 3] = 0;
    assert_eq!(
        unsafe { smartvpn_converter_flow_advance_v1(s.0, old.1, bad.as_ptr(), bad.len()) },
        -2
    );
    assert_eq!(next(&s), old);
    let b = request(5, b"");
    let mut h = 9;
    assert_eq!(
        unsafe { smartvpn_converter_flow_start_v1(b.as_ptr(), b.len(), &mut h) },
        -2
    );
    assert_eq!(h, 0);
}
#[test]
fn frame_and_list_bounds_rejected_preconsumption() {
    let _g = SERIAL.lock().unwrap();
    let s = open(0, b"");
    let old = next(&s);
    let b = reply(Reply {
        tokens: vec![1; 100001],
        ..ok()
    });
    assert_eq!(
        unsafe { smartvpn_converter_flow_advance_v1(s.0, old.1, b.as_ptr(), b.len()) },
        -2
    );
    let b = vec![0; (8 << 20) + 1];
    assert_eq!(
        unsafe { smartvpn_converter_flow_advance_v1(s.0, old.1, b.as_ptr(), b.len()) },
        -1
    );
    assert_eq!(next(&s), old);
}
#[test]
fn arbitrary_name_bytes_and_all_five_ops() {
    let _g = SERIAL.lock().unwrap();
    for op in 0..5 {
        let s = open(op, &[255, 0, 254]);
        for _ in 0..20 {
            let (b, _) = next(&s);
            let k = kind(&b);
            if k == b"done" {
                break;
            }
            let mut r = ok();
            match k {
                b"base64" => r.ok = false,
                b"body-facts" => r.flag = true,
                _ => {}
            }
            assert_eq!(send(&s, r), 0);
        }
        assert_eq!(kind(&next(&s).0), b"done");
    }
}
#[test]
fn hundred_thousand_outbounds_linear_resume_without_history_cap() {
    let _g = SERIAL.lock().unwrap();
    let s = open(0, b"");
    assert_eq!(
        send(
            &s,
            Reply {
                tokens: (1..=100000).collect(),
                ..ok()
            }
        ),
        0
    );
    let empty = reply(ok());
    for _ in 0..100000 {
        let (b, step) = next(&s);
        assert_eq!(kind(&b), b"decode-base");
        assert_eq!(
            unsafe { smartvpn_converter_flow_advance_v1(s.0, step, empty.as_ptr(), empty.len()) },
            0
        );
    }
    for token in [100001, 100002, 100003] {
        assert_eq!(send(&s, Reply { token, ..ok() }), 0)
    }
    assert_eq!(kind(&next(&s).0), b"result");
    assert!(next(&s).1 > 100000);
}
