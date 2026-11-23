use smartvpn_host::{durable::*, Request};
use std::sync::{Arc, Mutex};
#[derive(Clone, Default)]
struct Memory(Arc<Mutex<Option<DurableEnvelope>>>);
impl Journal for Memory {
    fn load(&mut self) -> Result<Option<DurableEnvelope>, Error> {
        Ok(self.0.lock().unwrap().clone())
    }
    fn publish(&mut self, e: &DurableEnvelope) -> Result<(), Error> {
        *self.0.lock().unwrap() = Some(e.clone());
        Ok(())
    }
}
#[test]
fn completed_snapshot_receipt_is_exactly_bound() {
    let j = Memory::default();
    let mut h = DurableHost::open(j.clone(), 7, vec![], true).unwrap();
    h.request(1, Request::Snapshot).unwrap();
    let original = j.0.lock().unwrap().clone().unwrap();
    for field in 0..3 {
        let mut e = original.clone();
        let r = &mut e.receipt.as_mut().unwrap().response;
        match field {
            0 => r.body.push(b'x'),
            1 => r.status = 201,
            _ => r.revision = 1,
        }
        assert!(encode(&e).is_err());
    }
}
#[test]
fn completed_http_receipt_is_checked_before_replay() {
    let j = Memory::default();
    let mut h = DurableHost::open(j.clone(), 7, vec![], true).unwrap();
    let q = Request::Http {
        method: b"GET".to_vec(),
        path: b"/identity".to_vec(),
        authorization: vec![],
    };
    h.request(1, q).unwrap();
    let original = j.0.lock().unwrap().clone().unwrap();
    for field in 0..2 {
        let mut e = original.clone();
        let r = &mut e.receipt.as_mut().unwrap().response;
        if field == 0 {
            r.body.push(b'x');
        } else {
            r.status = 599;
        }
        assert!(encode(&e).is_ok()); // Structural codec is not the HTTP policy owner.
        *j.0.lock().unwrap() = Some(e);
        assert!(matches!(
            DurableHost::open(j.clone(), 7, vec![], true),
            Err(Error::Invalid)
        ));
    }
}
#[test]
fn receipt_replay_after_close_reopen_does_not_publish() {
    let j = Memory::default();
    let mut h = DurableHost::open(j.clone(), 7, vec![], true).unwrap();
    let q = Request::SetSamplingError {
        expected_revision: 0,
        value: vec![0xff, 0],
    };
    let response = h.request(1, q.clone()).unwrap();
    h.close().unwrap();
    let committed = j.0.lock().unwrap().clone().unwrap();
    let mut reopened = DurableHost::open(j.clone(), 7, vec![], true).unwrap();
    assert_eq!(reopened.request(1, q).unwrap(), response);
    assert_eq!(j.0.lock().unwrap().as_ref().unwrap(), &committed);
}
