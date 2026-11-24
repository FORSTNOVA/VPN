//! Framed offline state host and opt-in loopback HTTP monitor. The offline
//! host uniquely owns its state and resource adapter. Only monitor mode binds
//! a socket and it attaches to a separately managed controller.
use smartvpn_core::{
    http_core,
    service_state::{Checkpoint, Command, Snapshot, State},
};
use std::{
    collections::BTreeMap,
    panic::{catch_unwind, AssertUnwindSafe},
};
pub const MAX_FRAME: usize = 1 << 20;
pub const MAX_ADMITTED: usize = 64;
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Request {
    Http {
        method: Vec<u8>,
        path: Vec<u8>,
        authorization: Vec<u8>,
    },
    SetSamplingError {
        expected_revision: u64,
        value: Vec<u8>,
    },
    Snapshot,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Response {
    pub status: u16,
    pub revision: u64,
    pub body: Vec<u8>,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum HostError {
    Closing,
    Closed,
    Capacity,
    Stale,
    Conflict,
    UnknownTicket,
    State,
    StoreUncertain,
    Frame,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Phase {
    Open,
    Closing,
    Closed,
}
/// Returning an error may mean the effect happened. Host never automatically
/// retries these calls. Adapters must not launch work outside this ownership.
pub trait Resources {
    fn persist(&mut self, checkpoint: &Checkpoint) -> Result<(), String>;
    fn close_listener(&mut self) -> Result<(), String>;
    fn close_store(&mut self) -> Result<(), String>;
}
#[derive(Default)]
pub struct MemoryResources {
    pub checkpoint: Option<Checkpoint>,
    pub persists: usize,
    pub listener_closes: usize,
    pub store_closes: usize,
}
impl Resources for MemoryResources {
    fn persist(&mut self, c: &Checkpoint) -> Result<(), String> {
        self.persists += 1;
        self.checkpoint = Some(c.clone());
        Ok(())
    }
    fn close_listener(&mut self) -> Result<(), String> {
        self.listener_closes += 1;
        Ok(())
    }
    fn close_store(&mut self) -> Result<(), String> {
        self.store_closes += 1;
        Ok(())
    }
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Ticket(u64);
#[derive(Clone, Debug)]
struct Receipt {
    id: u64,
    request: Request,
    response: Result<Response, HostError>,
}
pub enum Admission {
    Execute(Ticket),
    Replay(Response),
}
pub struct Host<R: Resources> {
    state: State,
    resources: R,
    token: Vec<u8>,
    windows: bool,
    phase: Phase,
    highwater: u64,
    admitted: BTreeMap<u64, Request>,
    last: Option<Receipt>,
    uncertain: bool,
    close_error: Option<String>,
}
impl<R: Resources> Host<R> {
    pub fn new(epoch: u64, token: Vec<u8>, windows: bool, resources: R) -> Result<Self, HostError> {
        if token.len() > MAX_FRAME {
            return Err(HostError::Frame);
        }
        let state = State::new(Snapshot {
            epoch,
            ..Snapshot::default()
        })
        .map_err(|_| HostError::State)?;
        Ok(Self {
            state,
            resources,
            token,
            windows,
            phase: Phase::Open,
            highwater: 0,
            admitted: BTreeMap::new(),
            last: None,
            uncertain: false,
            close_error: None,
        })
    }
    pub fn phase(&self) -> Phase {
        self.phase
    }
    pub fn resources(&self) -> &R {
        &self.resources
    }
    pub fn checkpoint(&self) -> Result<Checkpoint, HostError> {
        self.state.export().map_err(|_| HostError::State)
    }
    pub fn admit(&mut self, id: u64, request: Request) -> Result<Admission, HostError> {
        if let Some(last) = &self.last {
            if id == last.id {
                if request != last.request {
                    return Err(HostError::Conflict);
                }
                return last.response.clone().map(Admission::Replay);
            }
        }
        if self.phase != Phase::Open {
            return Err(if self.phase == Phase::Closed {
                HostError::Closed
            } else {
                HostError::Closing
            });
        }
        if id == 0 || id <= self.highwater {
            return Err(HostError::Stale);
        }
        if self.admitted.len() >= MAX_ADMITTED {
            return Err(HostError::Capacity);
        }
        // One mutation/receipt owner at a time. Concurrent transport admission may
        // queue read requests; execution is serialized by &mut Host, never copied.
        if request_size(&request) > MAX_FRAME {
            return Err(HostError::Frame);
        }
        self.highwater = id;
        self.admitted.insert(id, request);
        Ok(Admission::Execute(Ticket(id)))
    }
    pub fn execute(&mut self, ticket: Ticket) -> Result<Response, HostError> {
        let request = self
            .admitted
            .remove(&ticket.0)
            .ok_or(HostError::UnknownTicket)?;
        let response = self.execute_request(ticket.0, &request);
        self.last = Some(Receipt {
            id: ticket.0,
            request,
            response: response.clone(),
        });
        if self.phase == Phase::Closing && self.admitted.is_empty() {
            self.finish_close();
        }
        response
    }
    fn execute_request(&mut self, id: u64, q: &Request) -> Result<Response, HostError> {
        if self.uncertain {
            return Err(HostError::StoreUncertain);
        }
        let meta = self.state.meta().map_err(|_| HostError::State)?;
        let mut response = Response {
            status: 200,
            revision: meta.revision,
            body: Vec::new(),
        };
        match q {
            Request::Snapshot => {
                response.body =
                    format!("epoch={} revision={}\n", meta.epoch, meta.revision).into_bytes();
            }
            Request::SetSamplingError {
                expected_revision,
                value,
            } => {
                let ack = self
                    .state
                    .apply(Command {
                        epoch: meta.epoch,
                        id,
                        expected_revision: *expected_revision,
                        kind: 2,
                        sampling_error: value.clone(),
                        ..Command::default()
                    })
                    .map_err(|_| HostError::State)?;
                // State is already committed. Any persistence error is uncertain: no replay,
                // rollback or stale fallback. Preserve pending authority for investigation.
                let checkpoint = self.state.export().map_err(|_| HostError::State)?;
                self.uncertain = true;
                if !matches!(
                    catch_unwind(AssertUnwindSafe(|| self.resources.persist(&checkpoint))),
                    Ok(Ok(()))
                ) {
                    return Err(HostError::StoreUncertain);
                }
                self.state.acknowledge(ack).map_err(|_| HostError::State)?;
                self.uncertain = false;
                response.revision = ack.revision;
            }
            Request::Http {
                method,
                path,
                authorization,
            } => {
                let plan = smartvpn_core::http_dispatch_flow::plan(
                    method,
                    path,
                    authorization,
                    &self.token,
                    self.windows,
                );
                let mut adapter = OfflineHttp {
                    response: &mut response,
                    head: method == b"HEAD",
                    legacy: false,
                };
                smartvpn_core::http_dispatch_flow::execute(plan, &mut adapter)?;
            }
        }
        Ok(response)
    }
    pub fn begin_shutdown(&mut self) {
        if self.phase == Phase::Closed {
            return;
        }
        if self.phase == Phase::Open {
            // Consume ownership before invoking a potentially panicking adapter.
            self.phase = Phase::Closing;
            let result = catch_unwind(AssertUnwindSafe(|| self.resources.close_listener()));
            match result {
                Ok(Ok(())) => {}
                Ok(Err(e)) => self.close_error = Some(e),
                Err(_) => {
                    self.close_error = Some("listener close panicked; outcome uncertain".into())
                }
            }
        }
        if self.admitted.is_empty() {
            self.finish_close();
        }
    }
    fn finish_close(&mut self) {
        if self.phase == Phase::Closed {
            return;
        }
        // No retry even if the resource adapter acts and then panics.
        self.phase = Phase::Closed;
        let result = catch_unwind(AssertUnwindSafe(|| self.resources.close_store()));
        let error = match result {
            Ok(Ok(())) => None,
            Ok(Err(e)) => Some(e),
            Err(_) => Some("store close panicked; outcome uncertain".into()),
        };
        if self.close_error.is_none() {
            self.close_error = error;
        }
        // A store failure cannot leave the authoritative state usable after close.
        let _ = self.state.close();
    }
    pub fn shutdown_error(&self) -> Option<&str> {
        self.close_error.as_deref()
    }
}
fn request_size(q: &Request) -> usize {
    match q {
        Request::Http {
            method,
            path,
            authorization,
        } => method
            .len()
            .saturating_add(path.len())
            .saturating_add(authorization.len()),
        Request::SetSamplingError { value, .. } => value.len(),
        Request::Snapshot => 0,
    }
}
pub mod wire;

// Composition is reused unchanged. This binary replay transport has no HTTP
// headers, sockets, redirect normalization or production handler adapter.
struct OfflineHttp<'a> {
    response: &'a mut Response,
    head: bool,
    legacy: bool,
}
fn idle_monitor_response(route: u32) -> Option<(u16, Vec<u8>)> {
    let (status, body): (u16, &[u8]) = match route {
        9 => (409, b"connect before measuring node latency\n"),
        16 => (409, b"connect before measuring latency\n"),
        23 => (
            200,
            "{\"available\":false,\"reason\":\"内核未运行：连接与流量监控只在连接期间有意义\"}\n"
                .as_bytes(),
        ),
        _ => return None,
    };
    Some((status, body.to_vec()))
}
impl smartvpn_core::http_dispatch_flow::Effects for OfflineHttp<'_> {
    type Error = HostError;
    fn reject(&mut self, status: u16) -> Result<(), HostError> {
        self.response.status = status;
        Ok(())
    }
    fn json_header(&mut self) -> Result<(), HostError> {
        Ok(())
    }
    fn invoke(&mut self, route: u32) -> Result<(), HostError> {
        // This host has no kernel. Only dispatch branches whose outcome is
        // completely determined by that fact, without controller or store I/O.
        let (status, body) = match route {
            1 => (200, http_core::identity_json(false).to_vec()),
            9 | 16 | 23 if !self.legacy => idle_monitor_response(route).unwrap(),
            _ => {
                self.response.status = 501;
                self.response.body =
                    format!("unsupported host business adapter: route {route}\n").into_bytes();
                return Ok(());
            }
        };
        self.response.status = status;
        if !self.head {
            self.response.body = body;
        }
        Ok(())
    }
}
pub mod durable;
pub mod monitor;
pub mod engine_runtime;
pub mod control_plane;
#[cfg(target_os = "android")]
pub mod android_jni;
