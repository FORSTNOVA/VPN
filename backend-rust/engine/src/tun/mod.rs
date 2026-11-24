use crate::dialer::BoxFuture;
use smoltcp::{
    phy::{self, Device, DeviceCapabilities, Medium},
    time::Instant,
};
use std::{collections::VecDeque, io};
use tokio::sync::mpsc;

#[cfg(unix)]
pub mod android_fd;
mod stack;
mod udp;
#[cfg(windows)]
pub mod wintun_win;
pub use stack::TunStack;

/// Receive must be cancellation safe. Each operation transfers one raw IP packet.
pub trait TunDevice: Send + Sync {
    fn recv_packet(&mut self) -> BoxFuture<'_, io::Result<Vec<u8>>>;
    fn send_packet<'a>(&'a self, pkt: &'a [u8]) -> BoxFuture<'a, io::Result<()>>;
}
pub struct MemoryTun {
    rx: mpsc::Receiver<Vec<u8>>,
    tx: mpsc::Sender<Vec<u8>>,
}
impl MemoryTun {
    pub fn pair(capacity: usize) -> (Self, Self) {
        let (a, b) = mpsc::channel(capacity);
        let (c, d) = mpsc::channel(capacity);
        (Self { rx: b, tx: c }, Self { rx: d, tx: a })
    }
}
impl TunDevice for MemoryTun {
    fn recv_packet(&mut self) -> BoxFuture<'_, io::Result<Vec<u8>>> {
        Box::pin(async {
            self.rx
                .recv()
                .await
                .ok_or_else(|| io::Error::new(io::ErrorKind::UnexpectedEof, "TUN peer closed"))
        })
    }
    fn send_packet<'a>(&'a self, pkt: &'a [u8]) -> BoxFuture<'a, io::Result<()>> {
        Box::pin(async { self.tx.send(pkt.to_vec()).await.map_err(io::Error::other) })
    }
}

/// Synchronous IP device for one smoltcp poll. Drain transmitted packets before the next poll.
#[derive(Default)]
pub struct IpDevice {
    pub received: Option<Vec<u8>>,
    pub transmitted: VecDeque<Vec<u8>>,
}
pub struct Rx(Vec<u8>);
pub struct Tx<'a>(&'a mut VecDeque<Vec<u8>>);
impl phy::RxToken for Rx {
    fn consume<R, F: FnOnce(&[u8]) -> R>(self, f: F) -> R {
        f(&self.0)
    }
}
impl phy::TxToken for Tx<'_> {
    fn consume<R, F: FnOnce(&mut [u8]) -> R>(self, len: usize, f: F) -> R {
        let mut p = vec![0; len];
        let result = f(&mut p);
        self.0.push_back(p);
        result
    }
}
impl Device for IpDevice {
    type RxToken<'a> = Rx;
    type TxToken<'a> = Tx<'a>;
    fn receive(&mut self, _: Instant) -> Option<(Rx, Tx<'_>)> {
        self.received
            .take()
            .map(|p| (Rx(p), Tx(&mut self.transmitted)))
    }
    fn transmit(&mut self, _: Instant) -> Option<Tx<'_>> {
        Some(Tx(&mut self.transmitted))
    }
    fn capabilities(&self) -> DeviceCapabilities {
        let mut caps = DeviceCapabilities::default();
        caps.medium = Medium::Ip;
        caps.max_transmission_unit = 1500;
        caps
    }
}
