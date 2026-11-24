//! SmartVPN integration for the single VpnService tunnel in this process.
//! Tracks the lifetime of the actual TUN task, independently of the controller.
use std::{
    net::SocketAddr,
    sync::{Mutex, OnceLock},
};

pub type OwnerResolver = dyn Fn(u8, SocketAddr, SocketAddr) -> Option<String> + Send + Sync;
pub static OWNER_RESOLVER: OnceLock<Box<OwnerResolver>> = OnceLock::new();

#[derive(Clone)]
pub enum State {
    Pending,
    Running,
    Failed(String),
}

pub static STATE: Mutex<State> = Mutex::new(State::Pending);

pub(crate) struct Running;
impl Running {
    pub(crate) fn entered() -> Self {
        *STATE.lock().unwrap() = State::Running;
        Self
    }
}
impl Drop for Running {
    fn drop(&mut self) {
        *STATE.lock().unwrap() = State::Failed("TUN task exited".into());
    }
}
