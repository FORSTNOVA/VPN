//! Versioned offline checkpoint journal. Checksums detect accidents, not tampering.
use crate::{Request, Response, MAX_FRAME};
use smartvpn_core::service_state::{
    validate_checkpoint, Ack, Checkpoint, Command, Snapshot, State,
};
use std::{
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    panic::{catch_unwind, AssertUnwindSafe},
    path::{Path, PathBuf},
};
pub const MAX_CHECKPOINT: usize = 128 << 20;
const MAGIC: &[u8; 8] = b"SVHOST01";
// V2 distinguishes receipts issued under the idle HTTP handlers. Keep the
// original bytes for checkpoints with legacy HTTP receipts or non-HTTP work.
const IDLE_HTTP_MAGIC: &[u8; 8] = b"SVHOST02";
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DurableReceipt {
    pub legacy_http: bool,
    pub id: u64,
    pub request: Request,
    pub response: Response,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DurableEnvelope {
    pub sequence: u64,
    pub checkpoint: Checkpoint,
    pub highwater: u64,
    pub intent: Option<(u64, Request)>,
    pub receipt: Option<DurableReceipt>,
}
#[derive(Debug)]
pub enum Error {
    Io(io::Error),
    Invalid,
    RecoveryRequired,
    ResourceIdentity,
    Uncertain,
    Stale,
    Conflict,
    Closed,
}
impl From<io::Error> for Error {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}
struct W(Vec<u8>);
impl W {
    fn n(&mut self, n: u64) {
        self.0.extend(n.to_le_bytes());
    }
    fn flag(&mut self, b: bool) {
        self.0.push(u8::from(b));
    }
    fn bytes(&mut self, b: &[u8]) {
        self.n(b.len() as u64);
        self.0.extend(b);
    }
    fn snapshot(&mut self, s: &Snapshot) {
        self.n(s.epoch);
        self.n(s.revision);
        self.flag(s.has_baseline);
        self.n(s.time_token);
        self.n(s.up as u64);
        self.n(s.down as u64);
        for b in [
            &s.sampling_error,
            &s.path_payload,
            &s.health_payload,
            &s.control_payload,
            &s.catalog_payload,
            &s.china_payload,
        ] {
            self.bytes(b);
        }
    }
    fn command(&mut self, c: &Command) {
        self.n(c.epoch);
        self.n(c.id);
        self.n(c.expected_revision);
        self.0.push(c.kind);
        self.flag(c.has_baseline);
        self.n(c.time_token);
        self.n(c.up as u64);
        self.n(c.down as u64);
        for b in [
            &c.sampling_error,
            &c.path_payload,
            &c.health_payload,
            &c.control_payload,
            &c.catalog_payload,
            &c.china_payload,
        ] {
            self.bytes(b);
        }
    }
    fn request(&mut self, q: &Request) {
        match q {
            Request::Snapshot => self.0.push(0),
            Request::SetSamplingError {
                expected_revision,
                value,
            } => {
                self.0.push(1);
                self.n(*expected_revision);
                self.bytes(value);
            }
            Request::Http {
                method,
                path,
                authorization,
            } => {
                self.0.push(2);
                for b in [method, path, authorization] {
                    self.bytes(b);
                }
            }
        }
    }
}
struct R<'a>(&'a [u8]);
impl<'a> R<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], Error> {
        let b = self.0.get(..n).ok_or(Error::Invalid)?;
        self.0 = &self.0[n..];
        Ok(b)
    }
    fn n(&mut self) -> Result<u64, Error> {
        Ok(u64::from_le_bytes(self.take(8)?.try_into().unwrap()))
    }
    fn byte(&mut self) -> Result<u8, Error> {
        Ok(self.take(1)?[0])
    }
    fn flag(&mut self) -> Result<bool, Error> {
        match self.byte()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(Error::Invalid),
        }
    }
    fn bytes(&mut self) -> Result<Vec<u8>, Error> {
        let n = usize::try_from(self.n()?).map_err(|_| Error::Invalid)?;
        if n > 16 << 20 {
            return Err(Error::Invalid);
        }
        Ok(self.take(n)?.to_vec())
    }
    fn snapshot(&mut self) -> Result<Snapshot, Error> {
        Ok(Snapshot {
            epoch: self.n()?,
            revision: self.n()?,
            has_baseline: self.flag()?,
            time_token: self.n()?,
            up: self.n()? as i64,
            down: self.n()? as i64,
            sampling_error: self.bytes()?,
            path_payload: self.bytes()?,
            health_payload: self.bytes()?,
            control_payload: self.bytes()?,
            catalog_payload: self.bytes()?,
            china_payload: self.bytes()?,
        })
    }
    fn command(&mut self) -> Result<Command, Error> {
        Ok(Command {
            epoch: self.n()?,
            id: self.n()?,
            expected_revision: self.n()?,
            kind: self.byte()?,
            has_baseline: self.flag()?,
            time_token: self.n()?,
            up: self.n()? as i64,
            down: self.n()? as i64,
            sampling_error: self.bytes()?,
            path_payload: self.bytes()?,
            health_payload: self.bytes()?,
            control_payload: self.bytes()?,
            catalog_payload: self.bytes()?,
            china_payload: self.bytes()?,
        })
    }
    fn request(&mut self) -> Result<Request, Error> {
        let q = match self.byte()? {
            0 => Request::Snapshot,
            1 => Request::SetSamplingError {
                expected_revision: self.n()?,
                value: self.bytes()?,
            },
            2 => Request::Http {
                method: self.bytes()?,
                path: self.bytes()?,
                authorization: self.bytes()?,
            },
            _ => return Err(Error::Invalid),
        };
        if crate::request_size(&q) > MAX_FRAME {
            return Err(Error::Invalid);
        }
        Ok(q)
    }
}
fn checksum(b: &[u8]) -> u64 {
    b.iter().fold(0xcbf29ce484222325u64, |h, x| {
        (h ^ u64::from(*x)).wrapping_mul(0x100000001b3)
    })
}
pub fn validate(e: &DurableEnvelope) -> Result<(), Error> {
    validate_checkpoint(&e.checkpoint).map_err(|_| Error::Invalid)?;
    if e.sequence == 0 {
        return Err(Error::Invalid);
    }
    if e.checkpoint.has_last && e.checkpoint.last_command.id > e.highwater {
        return Err(Error::Invalid);
    }
    if let Some((id, q)) = &e.intent {
        if *id == 0 || *id != e.highwater || crate::request_size(q) > MAX_FRAME {
            return Err(Error::Invalid);
        }
    }
    if let Some(r) = &e.receipt {
        if r.legacy_http && !matches!(r.request, Request::Http { .. }) {
            return Err(Error::Invalid);
        }
        if r.id == 0
            || r.id > e.highwater
            || r.response.revision > e.checkpoint.snapshot.revision
            || crate::request_size(&r.request) > MAX_FRAME
            || r.response.body.len() > MAX_FRAME
        {
            return Err(Error::Invalid);
        }
        if e.intent.is_none() && r.id != e.highwater {
            return Err(Error::Invalid);
        }
    }
    if e.highwater != 0 && e.intent.is_none() && e.receipt.is_none() {
        return Err(Error::Invalid);
    }
    if e.checkpoint.pending {
        let Some((
            id,
            Request::SetSamplingError {
                expected_revision,
                value,
            },
        )) = &e.intent
        else {
            return Err(Error::Invalid);
        };
        let c = &e.checkpoint.last_command;
        if c.kind != 2
            || c.id != *id
            || c.expected_revision != *expected_revision
            || c.sampling_error != *value
        {
            return Err(Error::Invalid);
        }
    }
    if e.intent.is_none() {
        if let Some(r) = &e.receipt {
            if r.response.revision != e.checkpoint.snapshot.revision {
                return Err(Error::Invalid);
            }
            if r.request == Request::Snapshot
                && (r.response.status != 200
                    || r.response.body
                        != format!(
                            "epoch={} revision={}\n",
                            e.checkpoint.snapshot.epoch, e.checkpoint.snapshot.revision
                        )
                        .as_bytes())
            {
                return Err(Error::Invalid);
            }

            if let Request::SetSamplingError {
                expected_revision,
                value,
            } = &r.request
            {
                let c = &e.checkpoint.last_command;
                if !e.checkpoint.has_last
                    || c.kind != 2
                    || c.id != r.id
                    || c.expected_revision != *expected_revision
                    || c.sampling_error != *value
                    || r.response.revision != e.checkpoint.last_ack.revision
                    || r.response.status != 200
                    || !r.response.body.is_empty()
                {
                    return Err(Error::Invalid);
                }
            }
        }
    }
    Ok(())
}
pub fn encode(e: &DurableEnvelope) -> Result<Vec<u8>, Error> {
    validate(e)?;
    let current_http = e
        .receipt
        .as_ref()
        .is_some_and(|r| !r.legacy_http && matches!(r.request, Request::Http { .. }));
    let mut w = W(if current_http { IDLE_HTTP_MAGIC } else { MAGIC }.to_vec());
    w.n(e.sequence);
    w.n(e.highwater);
    w.snapshot(&e.checkpoint.snapshot);
    w.flag(e.checkpoint.has_last);
    if e.checkpoint.has_last {
        w.command(&e.checkpoint.last_command);
        let a = e.checkpoint.last_ack;
        for n in [a.epoch, a.id, a.revision] {
            w.n(n);
        }
    }
    w.flag(e.checkpoint.pending);
    w.flag(e.intent.is_some());
    if let Some((id, q)) = &e.intent {
        w.n(*id);
        w.request(q);
    }
    w.flag(e.receipt.is_some());
    if let Some(r) = &e.receipt {
        w.n(r.id);
        w.request(&r.request);
        w.n(u64::from(r.response.status));
        w.n(r.response.revision);
        w.bytes(&r.response.body);
    }
    if w.0.len() > MAX_CHECKPOINT - 8 {
        return Err(Error::Invalid);
    }
    let hash = checksum(&w.0);
    w.n(hash);
    Ok(w.0)
}
pub fn decode(b: &[u8]) -> Result<DurableEnvelope, Error> {
    if b.len() < 16 || b.len() > MAX_CHECKPOINT {
        return Err(Error::Invalid);
    }
    let (body, hash) = b.split_at(b.len() - 8);
    if checksum(body) != u64::from_le_bytes(hash.try_into().unwrap()) {
        return Err(Error::Invalid);
    }
    let mut r = R(body);
    let magic = r.take(8)?;
    if magic != MAGIC && magic != IDLE_HTTP_MAGIC {
        return Err(Error::Invalid);
    }
    let sequence = r.n()?;
    let highwater = r.n()?;
    let snapshot = r.snapshot()?;
    let has_last = r.flag()?;
    let (last_command, last_ack) = if has_last {
        (
            r.command()?,
            Ack {
                epoch: r.n()?,
                id: r.n()?,
                revision: r.n()?,
            },
        )
    } else {
        (Command::default(), Ack::default())
    };
    let pending = r.flag()?;
    let intent = if r.flag()? {
        Some((r.n()?, r.request()?))
    } else {
        None
    };
    let receipt = if r.flag()? {
        let id = r.n()?;
        let request = r.request()?;
        Some(DurableReceipt {
            legacy_http: magic == MAGIC && matches!(request, Request::Http { .. }),
            id,
            request,
            response: Response {
                status: u16::try_from(r.n()?).map_err(|_| Error::Invalid)?,
                revision: r.n()?,
                body: r.bytes()?,
            },
        })
    } else {
        None
    };
    if !r.0.is_empty()
        || (magic == IDLE_HTTP_MAGIC
            && !receipt
                .as_ref()
                .is_some_and(|r| matches!(r.request, Request::Http { .. })))
    {
        return Err(Error::Invalid);
    }
    let e = DurableEnvelope {
        sequence,
        highwater,
        checkpoint: Checkpoint {
            snapshot,
            has_last,
            last_command,
            last_ack,
            pending,
        },
        intent,
        receipt,
    };
    validate(&e)?;
    Ok(e)
}
/// Publication errors (including panics) have unknown outcome. Never retry them.
pub trait Journal {
    fn load(&mut self) -> Result<Option<DurableEnvelope>, Error>;
    fn publish(&mut self, e: &DurableEnvelope) -> Result<(), Error>;
    fn close(&mut self) -> Result<(), Error> {
        Ok(())
    }
}
pub struct DiskJournal {
    directory: PathBuf,
    lock: Option<File>,
    last: u64,
}
impl DiskJournal {
    /// Directory must already exist and be dedicated to this offline host.
    pub fn open(directory: &Path) -> Result<Self, Error> {
        let m = fs::symlink_metadata(directory)?;
        if !m.is_dir() || m.file_type().is_symlink() {
            return Err(Error::Invalid);
        }
        let lock = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(directory.join("owner.lock"))?;
        Ok(Self {
            directory: directory.to_path_buf(),
            lock: Some(lock),
            last: 0,
        })
    }
    /// Explicit clean close. A killed process leaves owner.lock; no automatic steal.
    pub fn close(&mut self) -> Result<(), Error> {
        if self.lock.take().is_some() {
            fs::remove_file(self.directory.join("owner.lock"))?;
        }
        Ok(())
    }
}
impl Drop for DiskJournal {
    fn drop(&mut self) {
        let _ = self.close();
    }
}
impl Journal for DiskJournal {
    fn close(&mut self) -> Result<(), Error> {
        DiskJournal::close(self)
    }

    fn load(&mut self) -> Result<Option<DurableEnvelope>, Error> {
        let mut latest: Option<(u64, PathBuf)> = None;
        for item in fs::read_dir(&self.directory)? {
            let item = item?;
            let name = item.file_name();
            let name = name.to_str().ok_or(Error::Invalid)?;
            if let Some(n) = name.strip_suffix(".checkpoint") {
                if n.len() != 20 || !n.bytes().all(|b| b.is_ascii_digit()) {
                    return Err(Error::Invalid);
                }
                let n: u64 = n.parse().map_err(|_| Error::Invalid)?;
                if latest.as_ref().is_none_or(|(old, _)| n > *old) {
                    latest = Some((n, item.path()));
                }
            }
        }
        let Some((n, path)) = latest else {
            return Ok(None);
        };
        let metadata = fs::symlink_metadata(&path)?;
        if !metadata.is_file()
            || metadata.file_type().is_symlink()
            || metadata.len() > MAX_CHECKPOINT as u64
        {
            return Err(Error::Invalid);
        }
        let mut bytes = Vec::new();
        File::open(path)?
            .take((MAX_CHECKPOINT + 1) as u64)
            .read_to_end(&mut bytes)?;
        let e = decode(&bytes)?;
        if n != e.sequence {
            return Err(Error::Invalid);
        }
        self.last = n;
        Ok(Some(e))
    }
    fn publish(&mut self, e: &DurableEnvelope) -> Result<(), Error> {
        if self.lock.is_none() || e.sequence != self.last.checked_add(1).ok_or(Error::Invalid)? {
            return Err(Error::Invalid);
        }
        let bytes = encode(e)?;
        let tmp = self.directory.join(format!("{:020}.tmp", e.sequence));
        let final_path = self
            .directory
            .join(format!("{:020}.checkpoint", e.sequence));
        if final_path.try_exists()? {
            return Err(Error::Invalid);
        }
        let mut f = OpenOptions::new().write(true).create_new(true).open(&tmp)?;
        f.write_all(&bytes)?;
        f.sync_all()?;
        drop(f);
        fs::rename(&tmp, &final_path)?;
        // Directory fsync is portable on Unix, unavailable through std on Windows.
        // Windows guarantee here is process-restart publication, NOT power-loss durability.
        #[cfg(unix)]
        {
            File::open(&self.directory)?.sync_all()?;
        }
        self.last = e.sequence;
        Ok(())
    }
}

/// Offline disk mode: exactly one synchronous admission. No production resource
/// identities are reconstructed. Non-default resource domains are rejected.
pub struct DurableHost<J: Journal> {
    state: State,
    journal: J,
    envelope: DurableEnvelope,
    token: Vec<u8>,
    windows: bool,
    poisoned: bool,
    closed: bool,
    close_failed: bool,
}
fn resource_free(c: &Checkpoint) -> Result<(), Error> {
    use smartvpn_core::{catalog_state, china_state, control_state, health_state, path_state};
    let s = &c.snapshot;
    if s.has_baseline
        || s.time_token != 0
        || s.up != 0
        || s.down != 0
        || path_state::decode(&s.path_payload).map_err(|_| Error::Invalid)?
            != path_state::PathState::default()
        || health_state::decode(&s.health_payload).map_err(|_| Error::Invalid)?
            != health_state::HealthState::default()
        || control_state::decode(&s.control_payload).map_err(|_| Error::Invalid)?
            != control_state::ControlState::default()
        || catalog_state::decode(&s.catalog_payload).map_err(|_| Error::Invalid)?
            != catalog_state::CatalogState::default()
        || china_state::decode(&s.china_payload).map_err(|_| Error::Invalid)?
            != china_state::ChinaState::default()
    {
        return Err(Error::ResourceIdentity);
    }
    if c.has_last {
        let last = &c.last_command;
        let canonical = Command {
            epoch: last.epoch,
            id: last.id,
            expected_revision: last.expected_revision,
            kind: 2,
            sampling_error: last.sampling_error.clone(),
            ..Command::default()
        };
        if *last != canonical {
            return Err(Error::ResourceIdentity);
        }
    }
    Ok(())
}
impl<J: Journal> DurableHost<J> {
    pub fn open(mut journal: J, epoch: u64, token: Vec<u8>, windows: bool) -> Result<Self, Error> {
        if token.len() > MAX_FRAME {
            return Err(Error::Invalid);
        }
        let loaded =
            catch_unwind(AssertUnwindSafe(|| journal.load())).map_err(|_| Error::Uncertain)??;
        let envelope = if let Some(e) = loaded {
            validate(&e)?;
            e
        } else {
            let state = State::new(Snapshot {
                epoch,
                ..Snapshot::default()
            })
            .map_err(|_| Error::Invalid)?;
            let e = DurableEnvelope {
                sequence: 1,
                checkpoint: state.export().map_err(|_| Error::Invalid)?,
                highwater: 0,
                intent: None,
                receipt: None,
            };
            catch_unwind(AssertUnwindSafe(|| journal.publish(&e)))
                .map_err(|_| Error::Uncertain)??;
            e
        };
        if envelope.checkpoint.snapshot.epoch != epoch {
            return Err(Error::Invalid);
        }
        if envelope.intent.is_some() || envelope.checkpoint.pending {
            return Err(Error::RecoveryRequired);
        }
        resource_free(&envelope.checkpoint)?;
        if let Some(r) = &envelope.receipt {
            if let Request::Http {
                method,
                path,
                authorization,
            } = &r.request
            {
                let mut expected = Response {
                    status: 200,
                    revision: envelope.checkpoint.snapshot.revision,
                    body: vec![],
                };
                let plan = smartvpn_core::http_dispatch_flow::plan(
                    method,
                    path,
                    authorization,
                    &token,
                    windows,
                );
                smartvpn_core::http_dispatch_flow::execute(
                    plan,
                    &mut crate::OfflineHttp {
                        response: &mut expected,
                        head: method == b"HEAD",
                        legacy: r.legacy_http,
                    },
                )
                .map_err(|_| Error::Invalid)?;
                if expected != r.response {
                    return Err(Error::Invalid);
                }
            }
        }

        let state =
            State::from_checkpoint(envelope.checkpoint.clone()).map_err(|_| Error::Invalid)?;
        Ok(Self {
            state,
            journal,
            envelope,
            token,
            windows,
            poisoned: false,
            closed: false,
            close_failed: false,
        })
    }
    pub fn journal(&self) -> &J {
        &self.journal
    }
    pub fn checkpoint(&self) -> Result<Checkpoint, Error> {
        self.state.export().map_err(|_| Error::Invalid)
    }
    fn publish(&mut self) -> Result<(), Error> {
        self.envelope.sequence = self
            .envelope
            .sequence
            .checked_add(1)
            .ok_or(Error::Invalid)?;
        if !matches!(
            catch_unwind(AssertUnwindSafe(|| self.journal.publish(&self.envelope))),
            Ok(Ok(()))
        ) {
            return Err(Error::Uncertain);
        }
        Ok(())
    }
    pub fn request(&mut self, id: u64, q: Request) -> Result<Response, Error> {
        if self.closed {
            return Err(Error::Closed);
        }
        if self.poisoned {
            return Err(Error::Uncertain);
        }
        if let Some(r) = &self.envelope.receipt {
            if id == r.id {
                return if q == r.request {
                    Ok(r.response.clone())
                } else {
                    Err(Error::Conflict)
                };
            }
        }
        if id == 0 || id <= self.envelope.highwater {
            return Err(Error::Stale);
        }
        if crate::request_size(&q) > MAX_FRAME {
            return Err(Error::Invalid);
        }
        let revision = self.envelope.checkpoint.snapshot.revision;
        if let Request::SetSamplingError {
            expected_revision, ..
        } = &q
        {
            if *expected_revision != revision || revision == u64::MAX {
                return Err(Error::Invalid);
            }
        }
        // Poison before the first durable effect. Cleared only after final receipt is published.
        self.poisoned = true;
        self.envelope.highwater = id;
        self.envelope.intent = Some((id, q.clone()));
        self.publish()?;
        let mut response = Response {
            status: 200,
            revision,
            body: vec![],
        };
        match &q {
            Request::SetSamplingError {
                expected_revision,
                value,
            } => {
                let ack = self
                    .state
                    .apply(Command {
                        epoch: self.envelope.checkpoint.snapshot.epoch,
                        id,
                        expected_revision: *expected_revision,
                        kind: 2,
                        sampling_error: value.clone(),
                        ..Command::default()
                    })
                    .map_err(|_| Error::Uncertain)?;
                self.envelope.checkpoint = self.state.export().map_err(|_| Error::Uncertain)?;
                self.publish()?;
                self.state.acknowledge(ack).map_err(|_| Error::Uncertain)?;
                self.envelope.checkpoint = self.state.export().map_err(|_| Error::Uncertain)?;
                response.revision = ack.revision;
            }
            Request::Snapshot => {
                response.body = format!(
                    "epoch={} revision={}\n",
                    self.envelope.checkpoint.snapshot.epoch, revision
                )
                .into_bytes()
            }
            Request::Http {
                method,
                path,
                authorization,
            } => {
                let p = smartvpn_core::http_dispatch_flow::plan(
                    method,
                    path,
                    authorization,
                    &self.token,
                    self.windows,
                );
                let mut adapter = crate::OfflineHttp {
                    response: &mut response,
                    head: method == b"HEAD",
                    legacy: false,
                };
                smartvpn_core::http_dispatch_flow::execute(p, &mut adapter)
                    .map_err(|_| Error::Uncertain)?;
            }
        }
        self.envelope.intent = None;
        self.envelope.receipt = Some(DurableReceipt {
            legacy_http: false,
            id,
            request: q,
            response: response.clone(),
        });
        self.publish()?;
        self.poisoned = false;
        Ok(response)
    }
    pub fn close(&mut self) -> Result<(), Error> {
        if self.closed {
            return if self.close_failed {
                Err(Error::Uncertain)
            } else {
                Ok(())
            };
        }
        self.closed = true;
        let result =
            catch_unwind(AssertUnwindSafe(|| self.journal.close())).map_err(|_| Error::Uncertain);
        let _ = self.state.close();
        self.close_failed = !matches!(result, Ok(Ok(())));
        result?
    }
}
