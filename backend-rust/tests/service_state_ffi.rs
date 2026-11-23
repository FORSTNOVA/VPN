use smartvpn_core::{
    service_state::{validate_checkpoint, Ack, Checkpoint, Command, Snapshot},
    service_state_ffi::*,
};
use std::{ptr, sync::Mutex};
static SERIAL: Mutex<()> = Mutex::new(());
const CAP: usize = 4 << 20;
fn n(b: &mut Vec<u8>, v: u64) {
    b.extend_from_slice(&v.to_le_bytes())
}
fn bytes(b: &mut Vec<u8>, v: &[u8]) {
    n(b, v.len() as u64);
    b.extend_from_slice(v)
}
fn command(c: &Command) -> Vec<u8> {
    let mut b = vec![];
    for v in [c.epoch, c.id, c.expected_revision] {
        n(&mut b, v)
    }
    b.push(c.kind);
    b.push(c.has_baseline as u8);
    for v in [c.time_token, c.up as u64, c.down as u64] {
        n(&mut b, v)
    }
    bytes(&mut b, &c.sampling_error);
    b
}
fn ack(a: Ack) -> Vec<u8> {
    let mut b = vec![];
    for v in [a.epoch, a.id, a.revision] {
        n(&mut b, v)
    }
    b
}
struct Reader<'a>(&'a [u8], usize);
impl Reader<'_> {
    fn u(&mut self) -> u64 {
        let p = self.1;
        self.1 += 8;
        u64::from_le_bytes(self.0[p..p + 8].try_into().unwrap())
    }
    fn flag(&mut self) -> bool {
        let p = self.1;
        self.1 += 1;
        self.0[p] != 0
    }
    fn bytes(&mut self) -> Vec<u8> {
        let n = self.u() as usize;
        let p = self.1;
        self.1 += n;
        self.0[p..p + n].to_vec()
    }
    fn snapshot(&mut self) -> Snapshot {
        Snapshot {
            epoch: self.u(),
            revision: self.u(),
            has_baseline: self.flag(),
            time_token: self.u(),
            up: self.u() as i64,
            down: self.u() as i64,
            sampling_error: self.bytes(),
            path_payload: vec![],
            health_payload: vec![],
            control_payload: vec![],
            catalog_payload: vec![],
            china_payload: vec![],
        }
    }
    fn cmd(&mut self) -> Command {
        let epoch = self.u();
        let id = self.u();
        let expected_revision = self.u();
        let p = self.1;
        self.1 += 1;
        Command {
            epoch,
            id,
            expected_revision,
            kind: self.0[p],
            has_baseline: self.flag(),
            time_token: self.u(),
            up: self.u() as i64,
            down: self.u() as i64,
            sampling_error: self.bytes(),
            path_payload: vec![],
            health_payload: vec![],
            control_payload: vec![],
            catalog_payload: vec![],
            china_payload: vec![],
        }
    }
    fn ack(&mut self) -> Ack {
        Ack {
            epoch: self.u(),
            id: self.u(),
            revision: self.u(),
        }
    }
}
struct Session {
    id: u64,
    out: Vec<u8>,
}
impl Session {
    fn new() -> Self {
        let mut id = 0;
        assert_eq!(unsafe { smartvpn_service_state_open_v1(&mut id) }, 0);
        Self { id, out: vec![] }
    }
    fn call(&mut self, op: u8, b: &[u8]) -> Result<Vec<u8>, i32> {
        self.out.resize(CAP, 0);
        let mut n = usize::MAX;
        let code = unsafe {
            smartvpn_service_state_call_v1(
                self.id,
                op,
                b.as_ptr(),
                b.len(),
                self.out.as_mut_ptr(),
                self.out.len(),
                &mut n,
            )
        };
        if code == 0 {
            assert!(n <= CAP);
            Ok(self.out[..n].to_vec())
        } else {
            assert_eq!(n, 0);
            Err(code)
        }
    }
    fn read(&mut self) -> Snapshot {
        let b = self.call(1, &[]).unwrap();
        let mut r = Reader(&b, 0);
        let s = r.snapshot();
        assert_eq!(r.1, b.len());
        s
    }
    fn apply(&mut self, c: &Command) -> Ack {
        let b = self.call(3, &command(c)).unwrap();
        let mut r = Reader(&b, 0);
        let a = r.ack();
        assert_eq!(r.1, b.len());
        a
    }
    fn status(&mut self, c: &Command) -> (bool, bool) {
        let b = self.call(4, &command(c)).unwrap();
        let mut r = Reader(&b, 0);
        let found = r.flag();
        let pending = if found {
            assert_eq!(r.cmd(), *c);
            assert_eq!(
                r.ack(),
                Ack {
                    epoch: c.epoch,
                    id: c.id,
                    revision: c.expected_revision + 1
                }
            );
            r.flag()
        } else {
            false
        };
        assert_eq!(r.1, b.len());
        (found, pending)
    }
    fn export(&mut self) -> Checkpoint {
        let b = self.call(5, &[]).unwrap();
        let mut r = Reader(&b, 0);
        let snapshot = r.snapshot();
        let has_last = r.flag();
        let (last_command, last_ack) = if has_last {
            (r.cmd(), r.ack())
        } else {
            (Command::default(), Ack::default())
        };
        let pending = r.flag();
        assert_eq!(r.1, b.len());
        let c = Checkpoint {
            snapshot,
            has_last,
            last_command,
            last_ack,
            pending,
        };
        validate_checkpoint(&c).unwrap();
        c
    }
    fn baseline(&self, id: u64) -> Command {
        Command {
            epoch: self.id,
            id,
            expected_revision: id - 1,
            kind: 1,
            has_baseline: true,
            time_token: id,
            up: 100,
            down: 200,
            ..Command::default()
        }
    }
}
impl Drop for Session {
    fn drop(&mut self) {
        if self.id != 0 {
            assert_eq!(smartvpn_service_state_close_v1(self.id), 0)
        }
    }
}
#[test]
fn committed_reply_loss_status_ack_loss_and_exact_identity() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    assert_eq!(smartvpn_service_state_version_v1(), 1);
    let c = s.baseline(1);
    let a = s.apply(&c);
    assert_eq!(s.status(&c), (true, true));
    assert_eq!(s.apply(&c), a);
    assert_eq!(s.call(3, &command(&s.baseline(2))), Err(-4));
    assert!(s.export().pending);
    s.call(6, &ack(a)).unwrap();
    assert_eq!(s.status(&c), (true, false));
    s.call(6, &ack(a)).unwrap();
    assert_eq!(s.apply(&c), a);
    assert!(!s.export().pending);
    assert_eq!(s.read().revision, 1);
    for bad in [
        Ack {
            epoch: a.epoch + 1,
            ..a
        },
        Ack { id: 2, ..a },
        Ack { revision: 2, ..a },
    ] {
        assert_eq!(s.call(6, &ack(bad)), Err(-4))
    }
    let mut changed = c.clone();
    changed.sampling_error = b"ignored payload is still identity".to_vec();
    assert_eq!(s.call(3, &command(&changed)), Err(-4));
    assert_eq!(s.call(4, &command(&changed)), Err(-4));
    let next = s.baseline(2);
    s.apply(&next);
    assert_eq!(s.read().revision, 2);
}
#[test]
fn malformed_and_small_output_prevent_mutation() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    let c = s.baseline(1);
    let b = command(&c);
    let before = s.export();
    let mut out = vec![0; CAP - 1];
    let mut written = 77;
    assert_eq!(
        unsafe {
            smartvpn_service_state_call_v1(
                s.id,
                3,
                b.as_ptr(),
                b.len(),
                out.as_mut_ptr(),
                out.len(),
                &mut written,
            )
        },
        -1
    );
    assert_eq!(written, 0);
    assert_eq!(s.export(), before);
    let mut trailing = b.clone();
    trailing.push(0);
    assert_eq!(s.call(3, &trailing), Err(-4));
    let mut over = c;
    over.sampling_error = vec![0; (1 << 20) + 1];
    assert_eq!(s.call(3, &command(&over)), Err(-4));
    assert_eq!(s.export(), before);
    assert_eq!(
        unsafe {
            smartvpn_service_state_call_v1(
                s.id,
                3,
                ptr::null(),
                1,
                out.as_mut_ptr(),
                out.len(),
                &mut written,
            )
        },
        -1
    );
}
#[test]
fn isolated_instances_capacity_and_closed_handle_never_reopens() {
    let _g = SERIAL.lock().unwrap();
    let mut live: Vec<_> = (0..64).map(|_| Session::new()).collect();
    let mut id = 42;
    assert_eq!(unsafe { smartvpn_service_state_open_v1(&mut id) }, -3);
    assert_eq!(id, 0);
    let c = live[0].baseline(1);
    live[0].apply(&c);
    assert_eq!(live[0].read().revision, 1);
    assert_eq!(live[1].read().revision, 0);
    let mut old = live.pop().unwrap();
    let oldid = old.id;
    assert_eq!(smartvpn_service_state_close_v1(oldid), 0);
    assert_eq!(old.call(1, &[]), Err(-3));
    assert_eq!(smartvpn_service_state_close_v1(oldid), -3);
    old.id = 0;
    let mut fresh = Session::new();
    assert_ne!(fresh.id, oldid);
    assert_eq!(fresh.read().revision, 0);
}
#[test]
fn rates_are_readonly_with_wrapping_and_explicit_unsupported() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    let c = s.baseline(1);
    let a = s.apply(&c);
    s.call(6, &ack(a)).unwrap();
    let before = s.export();
    for (sec, up, down, expected, supported) in [
        (2.0, 110, 230, (5, 15), true),
        (0.0, 110, 230, (0, 0), true),
        (1.0, 99, 230, (0, 0), true),
        (1e-9, i64::MAX, i64::MAX, (0, 0), false),
    ] {
        let mut b = vec![];
        n(&mut b, f64::to_bits(sec));
        n(&mut b, up as u64);
        n(&mut b, down as u64);
        let v = s.call(2, &b).unwrap();
        let mut r = Reader(&v, 0);
        assert_eq!((r.u() as i64, r.u() as i64), expected);
        assert_eq!(r.flag(), supported);
        assert_eq!(s.export(), before)
    }
}
#[test]
fn ten_thousand_commands_keep_one_checkpoint_slot() {
    let _g = SERIAL.lock().unwrap();
    let mut s = Session::new();
    let mut size = 0;
    for id in 1..=10000 {
        let c = s.baseline(id);
        let a = s.apply(&c);
        s.call(6, &ack(a)).unwrap();
        let cp = s.export();
        assert_eq!(cp.last_command, c);
        assert_eq!(cp.snapshot.revision, id);
        assert!(!cp.pending);
        let now = s.call(5, &[]).unwrap().len();
        if size == 0 {
            size = now
        };
        assert_eq!(size, now)
    }
    assert_eq!(s.status(&s.baseline(1)), (false, false));
    assert_eq!(s.call(3, &command(&s.baseline(1))), Err(-4));
}
