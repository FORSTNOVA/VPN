use smartvpn_core::service_state::Checkpoint;
use smartvpn_host::{Admission, Host, HostError, Phase, Request, Resources};

#[derive(Default)]
struct PanicResources {
    persist: usize,
    listener: usize,
    store: usize,
    panic_persist: bool,
    panic_listener: bool,
    panic_store: bool,
}
impl Resources for PanicResources {
    fn persist(&mut self, _: &Checkpoint) -> Result<(), String> {
        self.persist += 1;
        assert!(!self.panic_persist, "persist already performed");
        Ok(())
    }
    fn close_listener(&mut self) -> Result<(), String> {
        self.listener += 1;
        assert!(!self.panic_listener, "listener already closed");
        Ok(())
    }
    fn close_store(&mut self) -> Result<(), String> {
        self.store += 1;
        assert!(!self.panic_store, "store already closed");
        Ok(())
    }
}
#[test]
fn independent_listener_and_store_panics_still_close_authority_once() {
    for (listener, store) in [(true, false), (false, true), (true, true)] {
        let mut h = Host::new(
            1,
            vec![],
            true,
            PanicResources {
                panic_listener: listener,
                panic_store: store,
                ..Default::default()
            },
        )
        .unwrap();
        h.begin_shutdown();
        assert_eq!(h.phase(), Phase::Closed);
        assert!(h.checkpoint().is_err(), "closed must include authority");
        assert!(h.shutdown_error().is_some());
        h.begin_shutdown();
        assert_eq!((h.resources().listener, h.resources().store), (1, 1));
    }
}
#[test]
fn independent_persist_panic_records_failure_and_drains_last_ticket() {
    let mut h = Host::new(
        1,
        vec![],
        true,
        PanicResources {
            panic_persist: true,
            ..Default::default()
        },
    )
    .unwrap();
    let q = Request::SetSamplingError {
        expected_revision: 0,
        value: b"synthetic".to_vec(),
    };
    let ticket = match h.admit(1, q.clone()).unwrap() {
        Admission::Execute(t) => t,
        _ => panic!(),
    };
    h.begin_shutdown();
    assert_eq!(h.phase(), Phase::Closing);
    assert_eq!(h.execute(ticket), Err(HostError::StoreUncertain));
    assert_eq!(h.phase(), Phase::Closed);
    assert!(h.checkpoint().is_err());
    assert!(matches!(h.admit(1, q), Err(HostError::StoreUncertain)));
    h.begin_shutdown();
    assert_eq!(
        (
            h.resources().persist,
            h.resources().listener,
            h.resources().store
        ),
        (1, 1, 1)
    );
}
#[test]
fn independent_open_persist_panic_never_replays_or_accepts_next_mutation() {
    let mut h = Host::new(
        1,
        vec![],
        true,
        PanicResources {
            panic_persist: true,
            ..Default::default()
        },
    )
    .unwrap();
    let q = Request::SetSamplingError {
        expected_revision: 0,
        value: b"synthetic".to_vec(),
    };
    let ticket = match h.admit(1, q.clone()).unwrap() {
        Admission::Execute(t) => t,
        _ => panic!(),
    };
    assert_eq!(h.execute(ticket), Err(HostError::StoreUncertain));
    assert!(matches!(
        h.admit(1, q.clone()),
        Err(HostError::StoreUncertain)
    ));
    let next = match h.admit(2, q).unwrap() {
        Admission::Execute(t) => t,
        _ => panic!(),
    };
    assert_eq!(h.execute(next), Err(HostError::StoreUncertain));
    assert_eq!(h.resources().persist, 1);
    h.begin_shutdown();
    assert_eq!(h.phase(), Phase::Closed);
}
