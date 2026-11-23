//! Authoritative per-instance traffic, path, health and control state. No resources, clock or I/O.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub epoch: u64,
    pub revision: u64,
    pub has_baseline: bool,
    pub time_token: u64,
    pub up: i64,
    pub down: i64,
    pub sampling_error: Vec<u8>,
    pub path_payload: Vec<u8>,
    pub health_payload: Vec<u8>,
    pub control_payload: Vec<u8>,
    pub catalog_payload: Vec<u8>,
    pub china_payload: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    pub epoch: u64,
    pub id: u64,
    pub expected_revision: u64,
    pub kind: u8,
    pub has_baseline: bool,
    pub time_token: u64,
    pub up: i64,
    pub down: i64,
    pub sampling_error: Vec<u8>,
    pub path_payload: Vec<u8>,
    pub health_payload: Vec<u8>,
    pub control_payload: Vec<u8>,
    pub catalog_payload: Vec<u8>,
    pub china_payload: Vec<u8>,
}
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Ack {
    pub epoch: u64,
    pub id: u64,
    pub revision: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Checkpoint {
    pub snapshot: Snapshot,
    pub has_last: bool,
    pub last_command: Command,
    pub last_ack: Ack,
    pub pending: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Epoch,
    Revision,
    Conflict,
    Stale,
    Pending,
    Overflow,
    Closed,
}
pub const MAX_ERROR_BYTES: usize = 1 << 20;
pub struct State {
    checkpoint: Checkpoint,
    health: crate::health_state::HealthState,
    control: crate::control_state::ControlState,
    catalog: crate::catalog_state::CatalogState,
    china: crate::china_state::ChinaState,
    closed: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Status {
    pub found: bool,
    pub command: Command,
    pub ack: Ack,
    pub pending: bool,
}
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Rate {
    pub up: i64,
    pub down: i64,
    pub supported: bool,
}
fn validate_snapshot(s: &Snapshot) -> Result<(), Error> {
    crate::path_state::decode(&s.path_payload).map_err(|_| Error::Invalid)?;
    crate::health_state::decode(&s.health_payload).map_err(|_| Error::Invalid)?;
    crate::control_state::decode(&s.control_payload).map_err(|_| Error::Invalid)?;
    crate::catalog_state::decode(&s.catalog_payload).map_err(|_| Error::Invalid)?;
    crate::china_state::decode(&s.china_payload).map_err(|_| Error::Invalid)?;
    if s.epoch == 0
        || s.sampling_error.len() > MAX_ERROR_BYTES
        || s.has_baseline != (s.time_token != 0)
    {
        Err(Error::Invalid)
    } else {
        Ok(())
    }
}
fn validate_command(c: &Command) -> Result<(), Error> {
    if c.epoch == 0
        || c.id == 0
        || !matches!(c.kind, 1..=7)
        || c.sampling_error.len() > MAX_ERROR_BYTES
        || c.path_payload.len() > crate::path_state::MAX_PATH_BYTES
        || c.health_payload.len() > crate::health_state::MAX_HEALTH_BYTES
        || c.control_payload.len() > crate::control_state::MAX_CONTROL_BYTES
        || c.catalog_payload.len() > crate::catalog_state::MAX_CATALOG_BYTES
        || c.china_payload.len() > crate::china_state::MAX_CHINA_BYTES
        || (c.kind == 1 && c.has_baseline != (c.time_token != 0))
    {
        Err(Error::Invalid)
    } else {
        if c.kind == 3 {
            crate::path_state::validate_payload(&c.path_payload).map_err(|_| Error::Invalid)?;
        }
        if c.kind == 4 {
            crate::health_state::validate_payload(&c.health_payload).map_err(|_| Error::Invalid)?;
        }
        if c.kind == 5 {
            crate::control_state::validate_payload(&c.control_payload)
                .map_err(|_| Error::Invalid)?;
        }
        if c.kind == 6 {
            crate::catalog_state::validate_payload(&c.catalog_payload)
                .map_err(|_| Error::Invalid)?;
        }
        if c.kind == 7 {
            crate::china_state::validate_payload(&c.china_payload).map_err(|_| Error::Invalid)?;
        }
        Ok(())
    }
}
pub fn validate_checkpoint(c: &Checkpoint) -> Result<(), Error> {
    validate_snapshot(&c.snapshot)?;
    if !c.has_last {
        if c.pending
            || c.snapshot.revision != 0
            || c.last_command != Command::default()
            || c.last_ack != Ack::default()
        {
            return Err(Error::Invalid);
        };
        return Ok(());
    }
    validate_command(&c.last_command)?;
    let cmd = &c.last_command;
    let s = &c.snapshot;
    let revision = cmd
        .expected_revision
        .checked_add(1)
        .ok_or(Error::Overflow)?;
    if cmd.epoch != s.epoch
        || c.last_ack
            != (Ack {
                epoch: s.epoch,
                id: cmd.id,
                revision,
            })
        || s.revision != revision
    {
        return Err(Error::Invalid);
    }
    if cmd.kind == 1
        && (s.has_baseline != cmd.has_baseline
            || s.time_token != cmd.time_token
            || s.up != cmd.up
            || s.down != cmd.down)
    {
        return Err(Error::Invalid);
    }
    if cmd.kind == 2 && s.sampling_error != cmd.sampling_error {
        return Err(Error::Invalid);
    }
    if cmd.kind == 3 {
        let path = crate::path_state::decode(&s.path_payload).map_err(|_| Error::Invalid)?;
        crate::path_state::validate_last(&path, &cmd.path_payload).map_err(|_| Error::Invalid)?;
    }
    if cmd.kind == 4 {
        let health = crate::health_state::decode(&s.health_payload).map_err(|_| Error::Invalid)?;
        crate::health_state::validate_last(&health, &cmd.health_payload)
            .map_err(|_| Error::Invalid)?;
    }
    if cmd.kind == 5 {
        let control =
            crate::control_state::decode(&s.control_payload).map_err(|_| Error::Invalid)?;
        crate::control_state::validate_last(&control, &cmd.control_payload)
            .map_err(|_| Error::Invalid)?;
    }
    if cmd.kind == 6 {
        let catalog =
            crate::catalog_state::decode(&s.catalog_payload).map_err(|_| Error::Invalid)?;
        crate::catalog_state::validate_last(&catalog, &cmd.catalog_payload)
            .map_err(|_| Error::Invalid)?;
    }
    if cmd.kind == 7 {
        let china = crate::china_state::decode(&s.china_payload).map_err(|_| Error::Invalid)?;
        crate::china_state::validate_last(&china, &cmd.china_payload)
            .map_err(|_| Error::Invalid)?;
    }
    Ok(())
}
impl State {
    /// Restore validated authority without discarding command identity or pending.
    /// Resource owners must separately reconcile opaque handles and clocks.
    pub fn from_checkpoint(mut checkpoint: Checkpoint) -> Result<Self, Error> {
        validate_checkpoint(&checkpoint)?;
        let health = crate::health_state::decode(&checkpoint.snapshot.health_payload)
            .map_err(|_| Error::Invalid)?;
        let control = crate::control_state::decode(&checkpoint.snapshot.control_payload)
            .map_err(|_| Error::Invalid)?;
        let catalog = crate::catalog_state::decode(&checkpoint.snapshot.catalog_payload)
            .map_err(|_| Error::Invalid)?;
        let china = crate::china_state::decode(&checkpoint.snapshot.china_payload)
            .map_err(|_| Error::Invalid)?;
        checkpoint.snapshot.health_payload.clear();
        checkpoint.snapshot.control_payload.clear();
        checkpoint.snapshot.catalog_payload.clear();
        Ok(Self {
            checkpoint,
            health,
            control,
            catalog,
            china,
            closed: false,
        })
    }
    pub fn new(mut snapshot: Snapshot) -> Result<Self, Error> {
        validate_snapshot(&snapshot)?;
        if snapshot.revision != 0 {
            return Err(Error::Revision);
        };
        if snapshot.path_payload.is_empty() {
            snapshot.path_payload = crate::path_state::encode(
                &crate::path_state::decode(&[]).map_err(|_| Error::Invalid)?,
            );
        }
        let health =
            crate::health_state::decode(&snapshot.health_payload).map_err(|_| Error::Invalid)?;
        let control =
            crate::control_state::decode(&snapshot.control_payload).map_err(|_| Error::Invalid)?;
        let catalog =
            crate::catalog_state::decode(&snapshot.catalog_payload).map_err(|_| Error::Invalid)?;
        let china =
            crate::china_state::decode(&snapshot.china_payload).map_err(|_| Error::Invalid)?;
        snapshot.china_payload = crate::china_state::encode(&china).map_err(|_| Error::Invalid)?;
        snapshot.catalog_payload.clear();
        snapshot.control_payload.clear();
        snapshot.health_payload.clear();
        Ok(Self {
            health,
            control,
            catalog,
            china,
            checkpoint: Checkpoint {
                snapshot,
                ..Checkpoint::default()
            },
            closed: false,
        })
    }
    fn live(&self) -> Result<(), Error> {
        if self.closed {
            Err(Error::Closed)
        } else {
            Ok(())
        }
    }
    pub fn read(&self) -> Result<Snapshot, Error> {
        self.live()?;
        let mut snapshot = self.checkpoint.snapshot.clone();
        snapshot.health_payload =
            crate::health_state::encode(&self.health).map_err(|_| Error::Invalid)?;
        snapshot.control_payload =
            crate::control_state::encode(&self.control).map_err(|_| Error::Invalid)?;
        snapshot.catalog_payload =
            crate::catalog_state::encode(&self.catalog).map_err(|_| Error::Invalid)?;
        Ok(snapshot)
    }
    /// Authority metadata without health or control tables; path and traffic retain their existing payloads.
    pub fn meta(&self) -> Result<Snapshot, Error> {
        self.live()?;
        Ok(self.checkpoint.snapshot.clone())
    }
    pub fn health_query(&self, query: &[u8]) -> Result<Vec<u8>, Error> {
        self.live()?;
        crate::health_state::query(&self.health, query).map_err(|_| Error::Invalid)
    }
    pub fn control_query(&self) -> Result<Vec<u8>, Error> {
        self.live()?;
        crate::control_state::encode(&self.control).map_err(|_| Error::Invalid)
    }
    pub fn control_meta(&self) -> Result<Vec<u8>, Error> {
        self.live()?;
        crate::control_state::meta(&self.control).map_err(|_| Error::Invalid)
    }
    pub fn catalog_query(&self, input: &[u8]) -> Result<Vec<u8>, Error> {
        self.live()?;
        crate::catalog_state::query(&self.catalog, input).map_err(|_| Error::Invalid)
    }
    pub fn china_query(&self) -> Result<Vec<u8>, Error> {
        self.live()?;
        crate::china_state::encode(&self.china).map_err(|_| Error::Invalid)
    }
    pub fn export(&self) -> Result<Checkpoint, Error> {
        self.live()?;
        let mut checkpoint = self.checkpoint.clone();
        checkpoint.snapshot = self.read()?;
        validate_checkpoint(&checkpoint)?;
        Ok(checkpoint)
    }
    pub fn status(&self, c: &Command) -> Result<Status, Error> {
        self.live()?;
        validate_command(c)?;
        if c.epoch != self.checkpoint.snapshot.epoch {
            return Err(Error::Epoch);
        }
        if self.checkpoint.has_last && c.id == self.checkpoint.last_command.id {
            if c != &self.checkpoint.last_command {
                return Err(Error::Conflict);
            }
            Ok(Status {
                found: true,
                command: c.clone(),
                ack: self.checkpoint.last_ack,
                pending: self.checkpoint.pending,
            })
        } else {
            Ok(Status::default())
        }
    }
    pub fn apply(&mut self, c: Command) -> Result<Ack, Error> {
        let status = self.status(&c)?;
        if status.found {
            return Ok(status.ack);
        }
        if self.checkpoint.pending {
            return Err(Error::Pending);
        }
        if self.checkpoint.has_last && c.id <= self.checkpoint.last_command.id {
            return Err(Error::Stale);
        }
        if c.expected_revision != self.checkpoint.snapshot.revision {
            return Err(Error::Revision);
        }
        let revision = self
            .checkpoint
            .snapshot
            .revision
            .checked_add(1)
            .ok_or(Error::Overflow)?;
        match c.kind {
            1 => {
                let s = &mut self.checkpoint.snapshot;
                s.has_baseline = c.has_baseline;
                s.time_token = c.time_token;
                s.up = c.up;
                s.down = c.down
            }
            2 => self.checkpoint.snapshot.sampling_error = c.sampling_error.clone(),
            3 => {
                let mut path = crate::path_state::decode(&self.checkpoint.snapshot.path_payload)
                    .map_err(|_| Error::Invalid)?;
                crate::path_state::apply_payload(&mut path, &c.path_payload)
                    .map_err(|_| Error::Invalid)?;
                let encoded = crate::path_state::encode(&path);
                if encoded.len() > crate::path_state::MAX_PATH_BYTES {
                    return Err(Error::Invalid);
                }
                self.checkpoint.snapshot.path_payload = encoded;
            }
            4 => crate::health_state::apply_payload(&mut self.health, &c.health_payload)
                .map_err(|_| Error::Invalid)?,
            5 => crate::control_state::apply_payload(&mut self.control, &c.control_payload)
                .map_err(|_| Error::Invalid)?,
            6 => crate::catalog_state::apply_payload(&mut self.catalog, &c.catalog_payload)
                .map_err(|_| Error::Invalid)?,
            7 => {
                crate::china_state::apply_payload(&mut self.china, &c.china_payload)
                    .map_err(|_| Error::Invalid)?;
                self.checkpoint.snapshot.china_payload =
                    crate::china_state::encode(&self.china).map_err(|_| Error::Invalid)?;
            }
            _ => unreachable!(),
        }
        self.checkpoint.snapshot.revision = revision;
        let ack = Ack {
            epoch: c.epoch,
            id: c.id,
            revision,
        };
        self.checkpoint.last_command = c;
        self.checkpoint.last_ack = ack;
        self.checkpoint.has_last = true;
        self.checkpoint.pending = true;
        Ok(ack)
    }
    pub fn acknowledge(&mut self, a: Ack) -> Result<(), Error> {
        self.live()?;
        if a.epoch != self.checkpoint.snapshot.epoch {
            return Err(Error::Epoch);
        };
        if !self.checkpoint.has_last || a != self.checkpoint.last_ack {
            return Err(Error::Conflict);
        };
        self.checkpoint.pending = false;
        Ok(())
    }
    pub fn rate(&self, elapsed: f64, current_up: i64, current_down: i64) -> Result<Rate, Error> {
        self.live()?;
        let s = &self.checkpoint.snapshot;
        if !s.has_baseline || elapsed <= 0.0 || current_up < s.up || current_down < s.down {
            return Ok(Rate {
                supported: true,
                ..Rate::default()
            });
        }
        let convert = |delta: i64| {
            let n = delta as f64 / elapsed;
            if n.is_finite() && n >= i64::MIN as f64 && n < 9223372036854775808.0 {
                Some(n as i64)
            } else {
                None
            }
        };
        match (
            convert(current_up.wrapping_sub(s.up)),
            convert(current_down.wrapping_sub(s.down)),
        ) {
            (Some(up), Some(down)) => Ok(Rate {
                up,
                down,
                supported: true,
            }),
            _ => Ok(Rate::default()),
        }
    }
    pub fn close(&mut self) -> Result<(), Error> {
        self.checkpoint = Checkpoint::default();
        self.health = crate::health_state::HealthState::default();
        self.control = crate::control_state::ControlState::default();
        self.catalog = crate::catalog_state::CatalogState::default();
        self.china = crate::china_state::ChinaState::default();
        self.closed = true;
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn state() -> State {
        State::new(Snapshot {
            epoch: 7,
            ..Snapshot::default()
        })
        .unwrap()
    }
    fn baseline(id: u64, rev: u64) -> Command {
        Command {
            epoch: 7,
            id,
            expected_revision: rev,
            kind: 1,
            has_baseline: true,
            time_token: id,
            up: 100,
            down: 200,
            ..Command::default()
        }
    }
    #[test]
    fn apply_retry_ack_is_once_and_new_write_requires_ack() {
        let mut s = state();
        let c = baseline(1, 0);
        let a = s.apply(c.clone()).unwrap();
        assert_eq!(a.revision, 1);
        for _ in 0..5 {
            assert_eq!(s.apply(c.clone()), Ok(a))
        }
        assert_eq!(s.read().unwrap().revision, 1);
        assert!(s.status(&c).unwrap().pending);
        assert_eq!(s.apply(baseline(2, 1)), Err(Error::Pending));
        s.acknowledge(a).unwrap();
        s.acknowledge(a).unwrap();
        assert!(!s.status(&c).unwrap().pending);
        assert_eq!(s.apply(c.clone()), Ok(a));
        assert!(!s.status(&c).unwrap().pending);
        s.apply(baseline(2, 1)).unwrap();
    }
    #[test]
    fn all_command_payload_fields_bound_to_identity() {
        let mut s = state();
        let c = baseline(1, 0);
        s.apply(c.clone()).unwrap();
        let mut variants = vec![];
        let mut x = c.clone();
        x.expected_revision = 1;
        variants.push(x);
        let mut x = c.clone();
        x.kind = 2;
        variants.push(x);
        let mut x = c.clone();
        x.time_token = 99;
        variants.push(x);
        let mut x = c.clone();
        x.up = -1;
        variants.push(x);
        let mut x = c.clone();
        x.down = -1;
        variants.push(x);
        let mut x = c.clone();
        x.sampling_error = b"ignored but bound".to_vec();
        variants.push(x);
        let mut x = c.clone();
        x.path_payload = b"ignored path still bound".to_vec();
        variants.push(x);
        for x in variants {
            assert_eq!(s.apply(x.clone()), Err(Error::Conflict));
            assert_eq!(s.status(&x), Err(Error::Conflict))
        }
    }
    #[test]
    fn invalid_epoch_revision_zero_id_and_stale_leave_state_unchanged() {
        let mut s = state();
        let original = s.export().unwrap();
        for (id, epoch, rev, expected) in [
            (0, 7, 0, Error::Invalid),
            (1, 8, 0, Error::Epoch),
            (1, 7, 1, Error::Revision),
        ] {
            let mut c = baseline(id, rev);
            c.epoch = epoch;
            assert_eq!(s.apply(c), Err(expected));
            assert_eq!(s.export().unwrap(), original)
        }
        let a = s.apply(baseline(8, 0)).unwrap();
        s.acknowledge(a).unwrap();
        assert_eq!(s.apply(baseline(7, 1)), Err(Error::Stale));
    }
    #[test]
    fn ack_wrong_epoch_id_revision_rejected_and_pending_retained() {
        let mut s = state();
        let c = baseline(1, 0);
        let a = s.apply(c.clone()).unwrap();
        for bad in [
            Ack { epoch: 8, ..a },
            Ack { id: 2, ..a },
            Ack { revision: 2, ..a },
        ] {
            assert!(s.acknowledge(bad).is_err());
            assert!(s.status(&c).unwrap().pending)
        }
        s.acknowledge(a).unwrap();
        assert!(!s.status(&c).unwrap().pending)
    }
    #[test]
    fn baseline_error_and_reset_have_independent_state() {
        let mut s = state();
        let a = s.apply(baseline(1, 0)).unwrap();
        s.acknowledge(a).unwrap();
        let c = Command {
            epoch: 7,
            id: 2,
            expected_revision: 1,
            kind: 2,
            sampling_error: vec![0, 255, b'a'],
            ..Command::default()
        };
        let a = s.apply(c.clone()).unwrap();
        s.acknowledge(a).unwrap();
        assert_eq!(s.read().unwrap().up, 100);
        assert_eq!(s.read().unwrap().sampling_error, c.sampling_error);
        let reset = Command {
            epoch: 7,
            id: 3,
            expected_revision: 2,
            kind: 1,
            ..Command::default()
        };
        s.apply(reset).unwrap();
        let snap = s.read().unwrap();
        assert!(!snap.has_baseline);
        assert_eq!(snap.time_token, 0);
        assert_eq!(snap.sampling_error, c.sampling_error);
    }
    #[test]
    fn checkpoint_pending_committed_identity_validation() {
        let mut s = state();
        validate_checkpoint(&s.export().unwrap()).unwrap();
        let a = s.apply(baseline(1, 0)).unwrap();
        let pending = s.export().unwrap();
        assert!(pending.pending);
        validate_checkpoint(&pending).unwrap();
        s.acknowledge(a).unwrap();
        let mut cp = s.export().unwrap();
        assert!(!cp.pending);
        cp.last_ack.id += 1;
        assert!(validate_checkpoint(&cp).is_err());
        let mut cp = pending.clone();
        cp.snapshot.up += 1;
        assert!(validate_checkpoint(&cp).is_err());
        let mut cp = pending;
        cp.has_last = false;
        assert!(validate_checkpoint(&cp).is_err());
    }
    #[test]
    fn separate_instances_and_terminal_close() {
        let mut a = state();
        let b = state();
        a.apply(baseline(1, 0)).unwrap();
        assert_eq!(b.read().unwrap().revision, 0);
        a.close().unwrap();
        a.close().unwrap();
        assert_eq!(a.read(), Err(Error::Closed));
        assert_eq!(a.apply(baseline(2, 1)), Err(Error::Closed));
        assert_eq!(a.export(), Err(Error::Closed));
        assert_eq!(a.rate(1.0, 0, 0), Err(Error::Closed));
        assert_eq!(b.read().unwrap().epoch, 7);
    }
    #[test]
    fn reads_and_rates_never_advance_revision_or_ack() {
        let mut s = state();
        let c = baseline(1, 0);
        s.apply(c.clone()).unwrap();
        let cp = s.export().unwrap();
        for _ in 0..100 {
            assert_eq!(
                s.rate(2.0, 110, 230),
                Ok(Rate {
                    up: 5,
                    down: 15,
                    supported: true
                })
            );
            s.read().unwrap();
            s.status(&c).unwrap();
        }
        assert_eq!(s.export().unwrap(), cp);
    }
    #[test]
    fn rate_reset_counter_decline_zero_negative_and_unsupported_extremes() {
        let mut s = state();
        assert_eq!(s.rate(1.0, 100, 200).unwrap().up, 0);
        let a = s.apply(baseline(1, 0)).unwrap();
        s.acknowledge(a).unwrap();
        for (elapsed, up, down) in [
            (0.0, 110, 230),
            (-1.0, 110, 230),
            (1.0, 99, 230),
            (1.0, 110, 199),
        ] {
            assert_eq!(
                s.rate(elapsed, up, down),
                Ok(Rate {
                    supported: true,
                    ..Rate::default()
                })
            )
        }
        assert!(!s.rate(1e-9, i64::MAX, i64::MAX).unwrap().supported);
        assert!(!s.rate(f64::NAN, 100, 200).unwrap().supported);
        let mut c = baseline(2, 1);
        c.up = i64::MIN;
        c.down = i64::MIN;
        s.apply(c).unwrap();
        assert_eq!(
            s.rate(1.0, i64::MAX, i64::MAX),
            Ok(Rate {
                up: -1,
                down: -1,
                supported: true
            })
        );
    }
    #[test]
    fn maximum_error_size_and_oversize_zero_mutation() {
        let mut s = state();
        let c = Command {
            epoch: 7,
            id: 1,
            kind: 2,
            sampling_error: vec![255; MAX_ERROR_BYTES],
            ..Command::default()
        };
        let a = s.apply(c).unwrap();
        s.acknowledge(a).unwrap();
        let before = s.export().unwrap();
        let c = Command {
            epoch: 7,
            id: 2,
            expected_revision: 1,
            kind: 2,
            sampling_error: vec![0; MAX_ERROR_BYTES + 1],
            ..Command::default()
        };
        assert_eq!(s.apply(c), Err(Error::Invalid));
        assert_eq!(s.export().unwrap(), before);
    }
    #[test]
    fn token_presence_validation_and_nonzero_initial_revision() {
        for (has, token) in [(true, 0), (false, 1)] {
            assert!(State::new(Snapshot {
                epoch: 1,
                has_baseline: has,
                time_token: token,
                ..Snapshot::default()
            })
            .is_err())
        }
        assert!(State::new(Snapshot {
            epoch: 1,
            revision: 1,
            ..Snapshot::default()
        })
        .is_err());
    }
    #[test]
    fn revision_overflow_rejected_without_mutation() {
        let mut s = state();
        s.checkpoint.snapshot.revision = u64::MAX;
        let c = baseline(1, u64::MAX);
        assert_eq!(s.apply(c), Err(Error::Overflow));
        assert_eq!(s.checkpoint.snapshot.revision, u64::MAX);
        assert!(!s.checkpoint.has_last);
    }
    #[test]
    fn bounded_last_command_over_many_commits() {
        let mut s = state();
        for id in 1..=100000 {
            let c = baseline(id, id - 1);
            let ack = s.apply(c.clone()).unwrap();
            s.acknowledge(ack).unwrap();
            assert_eq!(s.checkpoint.last_command, c);
            assert_eq!(s.checkpoint.snapshot.revision, id)
        }
        assert_eq!(s.checkpoint.last_ack.id, 100000);
    }
}
#[cfg(test)]
mod checkpoint_restore_tests {
    use super::*;
    #[test]
    fn restore_pending_preserves_identity_and_requires_ack() {
        let mut state = State::new(Snapshot {
            epoch: 51,
            ..Snapshot::default()
        })
        .unwrap();
        let command = Command {
            epoch: 51,
            id: 7,
            kind: 2,
            sampling_error: vec![0xff, 0],
            ..Command::default()
        };
        let ack = state.apply(command.clone()).unwrap();
        let checkpoint = state.export().unwrap();
        let mut restored = State::from_checkpoint(checkpoint.clone()).unwrap();
        assert_eq!(restored.export().unwrap(), checkpoint);
        assert_eq!(restored.apply(command.clone()).unwrap(), ack);
        assert_eq!(
            restored.apply(Command {
                id: 8,
                expected_revision: 1,
                ..command.clone()
            }),
            Err(Error::Pending)
        );
        restored.acknowledge(ack).unwrap();
        assert!(!restored.status(&command).unwrap().pending);
        assert!(restored
            .apply(Command {
                id: 8,
                expected_revision: 1,
                ..command
            })
            .is_ok());
    }
    #[test]
    fn restore_rejects_corrupt_ack_and_keeps_closed_original_closed() {
        let mut state = State::new(Snapshot {
            epoch: 52,
            ..Snapshot::default()
        })
        .unwrap();
        let ack = state
            .apply(Command {
                epoch: 52,
                id: 1,
                kind: 2,
                ..Command::default()
            })
            .unwrap();
        state.acknowledge(ack).unwrap();
        let cp = state.export().unwrap();
        state.close().unwrap();
        assert_eq!(state.meta(), Err(Error::Closed));
        assert_eq!(
            State::from_checkpoint(cp.clone())
                .unwrap()
                .export()
                .unwrap(),
            cp
        );
        let mut bad = cp;
        bad.last_ack.revision += 1;
        assert!(State::from_checkpoint(bad).is_err());
    }
}
