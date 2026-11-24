use crate::dialer::{TargetAddr, UdpPacketSocket};
use blake2::{digest::consts::U32, Blake2b, Digest};
use quinn::{
    udp::{RecvMeta, Transmit},
    AsyncUdpSocket, UdpPoller,
};
use std::{
    collections::VecDeque,
    fmt,
    future::Future,
    io::{self, IoSliceMut},
    net::SocketAddr,
    pin::Pin,
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc, Mutex,
    },
    task::{ready, Context, Poll, Waker},
    time::Duration,
};
use tokio::time::{Instant, Sleep};

#[derive(Debug)]
struct TunnelPoller;

impl UdpPoller for TunnelPoller {
    fn poll_writable(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
}

pub(super) struct TunnelUdpSocket {
    tx: tokio::sync::mpsc::Sender<Vec<u8>>,
    rx_queue: Arc<Mutex<VecDeque<Vec<u8>>>>,
    rx_waker: Arc<Mutex<Option<Waker>>>,
    closed: Arc<AtomicBool>,
    synthetic_peer: SocketAddr,
    local: SocketAddr,
}

impl fmt::Debug for TunnelUdpSocket {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("TunnelUdpSocket")
            .field("synthetic_peer", &self.synthetic_peer)
            .finish_non_exhaustive()
    }
}

impl TunnelUdpSocket {
    pub(super) fn new(
        sock: Box<dyn UdpPacketSocket>,
        remote: TargetAddr,
        synthetic_peer: SocketAddr,
    ) -> Arc<Self> {
        let sock: Arc<dyn UdpPacketSocket> = Arc::from(sock);
        let (tx, mut rx) = tokio::sync::mpsc::channel::<Vec<u8>>(256);
        let rx_queue = Arc::new(Mutex::new(VecDeque::new()));
        let rx_waker: Arc<Mutex<Option<Waker>>> = Arc::new(Mutex::new(None));
        let closed = Arc::new(AtomicBool::new(false));

        let sock_tx = Arc::clone(&sock);
        let remote_tx = remote.clone();
        let closed_tx = Arc::clone(&closed);
        tokio::spawn(async move {
            while let Some(pkt) = rx.recv().await {
                if sock_tx.send_to(&pkt, &remote_tx).await.is_err() {
                    closed_tx.store(true, Ordering::Relaxed);
                    break;
                }
            }
        });

        let sock_rx = Arc::clone(&sock);
        let q_rx = Arc::clone(&rx_queue);
        let w_rx = Arc::clone(&rx_waker);
        let closed_rx = Arc::clone(&closed);
        tokio::spawn(async move {
            let mut buf = vec![0u8; 65535];
            loop {
                match sock_rx.recv_from(&mut buf).await {
                    Ok((n, _)) => {
                        {
                            let mut q = q_rx.lock().unwrap();
                            if q.len() < 256 {
                                q.push_back(buf[..n].to_vec());
                            }
                        }
                        if let Some(w) = w_rx.lock().unwrap().take() {
                            w.wake();
                        }
                    }
                    Err(_) => {
                        closed_rx.store(true, Ordering::Relaxed);
                        if let Some(w) = w_rx.lock().unwrap().take() {
                            w.wake();
                        }
                        break;
                    }
                }
            }
        });

        let local: SocketAddr = if synthetic_peer.is_ipv6() {
            ([0u16; 8], 0).into()
        } else {
            ([127, 0, 0, 1], 0).into()
        };
        Arc::new(Self {
            tx,
            rx_queue,
            rx_waker,
            closed,
            synthetic_peer,
            local,
        })
    }
}

impl AsyncUdpSocket for TunnelUdpSocket {
    fn create_io_poller(self: Arc<Self>) -> Pin<Box<dyn UdpPoller>> {
        Box::pin(TunnelPoller)
    }

    fn try_send(&self, transmit: &Transmit<'_>) -> io::Result<()> {
        if self.closed.load(Ordering::Relaxed) {
            return Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "underlay UDP tunnel closed",
            ));
        }
        self.tx
            .try_send(transmit.contents.to_vec())
            .map_err(|e| match e {
                tokio::sync::mpsc::error::TrySendError::Full(_) => {
                    io::ErrorKind::WouldBlock.into()
                }
                tokio::sync::mpsc::error::TrySendError::Closed(_) => {
                    io::Error::new(io::ErrorKind::BrokenPipe, "underlay UDP tunnel closed")
                }
            })
    }

    fn poll_recv(
        &self,
        cx: &mut Context<'_>,
        bufs: &mut [IoSliceMut<'_>],
        meta: &mut [RecvMeta],
    ) -> Poll<io::Result<usize>> {
        if let Some(pkt) = self.rx_queue.lock().unwrap().pop_front() {
            let n = pkt.len().min(bufs[0].len());
            bufs[0][..n].copy_from_slice(&pkt[..n]);
            meta[0] = RecvMeta {
                addr: self.synthetic_peer,
                len: n,
                stride: n.max(1),
                ecn: None,
                dst_ip: None,
            };
            return Poll::Ready(Ok(1));
        }
        if self.closed.load(Ordering::Relaxed) {
            return Poll::Ready(Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "underlay UDP tunnel closed",
            )));
        }
        *self.rx_waker.lock().unwrap() = Some(cx.waker().clone());
        Poll::Pending
    }

    fn local_addr(&self) -> io::Result<SocketAddr> {
        Ok(self.local)
    }
}

pub(super) fn bind_protected(addr: SocketAddr) -> io::Result<std::net::UdpSocket> {
    let socket = std::net::UdpSocket::bind(addr)?;
    #[cfg(unix)]
    crate::dialer::protect_socket_fd(std::os::fd::AsRawFd::as_raw_fd(&socket))?;
    Ok(socket)
}

fn scramble(password: &[u8], salt: &[u8], payload: &mut [u8]) {
    let mask = Blake2b::<U32>::new()
        .chain_update(password)
        .chain_update(salt)
        .finalize();
    for (i, byte) in payload.iter_mut().enumerate() {
        *byte ^= mask[i % 32];
    }
}

// One endpoint per authenticated connection. Quinn sees a stable peer address
// even when the on-wire destination rotates through forwarded server ports.
pub(super) struct PacketTransport {
    inner: Arc<dyn AsyncUdpSocket>,
    peer: SocketAddr,
    ports: Vec<u16>,
    started: Instant,
    password: Option<String>,
    pub(super) rate: AtomicU64,
    next_send: Mutex<Instant>,
    scratch: Mutex<Vec<u8>>,
}

impl fmt::Debug for PacketTransport {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("PacketTransport")
            .field("peer", &self.peer)
            .finish_non_exhaustive()
    }
}

impl PacketTransport {
    fn normalize_peer(&self, packet: &mut RecvMeta) {
        if packet.addr.ip() == self.peer.ip() && self.ports.contains(&packet.addr.port()) {
            packet.addr = self.peer;
        }
    }
    pub(super) fn new(
        inner: Arc<dyn AsyncUdpSocket>,
        peer: SocketAddr,
        ports: Vec<u16>,
        password: Option<String>,
    ) -> Self {
        Self {
            inner,
            peer,
            ports,
            password,
            started: Instant::now(),
            rate: AtomicU64::new(0),
            next_send: Mutex::new(Instant::now()),
            scratch: Mutex::new(vec![0; 65535]),
        }
    }
}

#[derive(Debug)]
struct Poller {
    socket: Arc<PacketTransport>,
    inner: Pin<Box<dyn UdpPoller>>,
    timer: Pin<Box<Sleep>>,
}

impl UdpPoller for Poller {
    fn poll_writable(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let deadline = *self.socket.next_send.lock().unwrap();
        if deadline > Instant::now() {
            self.timer.as_mut().reset(deadline);
            ready!(self.timer.as_mut().poll(cx));
        }
        self.inner.as_mut().poll_writable(cx)
    }
}

impl AsyncUdpSocket for PacketTransport {
    fn create_io_poller(self: Arc<Self>) -> Pin<Box<dyn UdpPoller>> {
        Box::pin(Poller {
            inner: self.inner.clone().create_io_poller(),
            socket: self,
            timer: Box::pin(tokio::time::sleep(Duration::ZERO)),
        })
    }

    fn try_send(&self, tx: &Transmit<'_>) -> io::Result<()> {
        let rate = self.rate.load(Ordering::Relaxed);
        let mut deadline = self.next_send.lock().unwrap();
        if rate > 0 && *deadline > Instant::now() {
            return Err(io::ErrorKind::WouldBlock.into());
        }
        let mut wire = tx.clone();
        if !self.ports.is_empty() {
            let hop = self.started.elapsed().as_secs() / 30;
            wire.destination
                .set_port(self.ports[(hop % self.ports.len() as u64) as usize]);
        }
        let mut encoded;
        if let Some(password) = &self.password {
            let salt = rand::random::<[u8; 8]>();
            encoded = Vec::with_capacity(8 + tx.contents.len());
            encoded.extend_from_slice(&salt);
            encoded.extend_from_slice(tx.contents);
            scramble(password.as_bytes(), &salt, &mut encoded[8..]);
            wire.contents = &encoded;
        }
        self.inner.try_send(&wire)?;
        if rate > 0 {
            *deadline =
                Instant::now() + Duration::from_secs_f64(wire.contents.len() as f64 / rate as f64);
        }
        Ok(())
    }

    fn poll_recv(
        &self,
        cx: &mut Context<'_>,
        bufs: &mut [IoSliceMut<'_>],
        meta: &mut [RecvMeta],
    ) -> Poll<io::Result<usize>> {
        let Some(password) = &self.password else {
            let count = ready!(self.inner.poll_recv(cx, bufs, meta))?;
            for packet in &mut meta[..count] {
                self.normalize_peer(packet);
            }
            return Poll::Ready(Ok(count));
        };
        let mut scratch = self.scratch.lock().unwrap();
        let mut incoming = [IoSliceMut::new(&mut scratch)];
        let count = ready!(self.inner.poll_recv(cx, &mut incoming, &mut meta[..1]))?;
        if count == 0 {
            return Poll::Ready(Ok(0));
        }
        let m = &mut meta[0];
        self.normalize_peer(m);
        let mut written = 0;
        // GRO can coalesce multiple wire datagrams, each with its own salt.
        if m.stride > 8 {
            for packet in scratch[..m.len].chunks_mut(m.stride) {
                if packet.len() <= 8 {
                    break;
                }
                let (salt, payload) = packet.split_at_mut(8);
                if written + payload.len() > bufs[0].len() {
                    break;
                }
                scramble(password.as_bytes(), salt, payload);
                bufs[0][written..written + payload.len()].copy_from_slice(payload);
                written += payload.len();
            }
        }
        m.len = written;
        m.stride = m.stride.saturating_sub(8).max(1);
        Poll::Ready(Ok(usize::from(written != 0)))
    }
    fn max_receive_segments(&self) -> usize {
        self.inner.max_receive_segments()
    }
    fn may_fragment(&self) -> bool {
        self.inner.may_fragment()
    }
    fn local_addr(&self) -> io::Result<SocketAddr> {
        self.inner.local_addr()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use quinn::Runtime;
    use std::future::poll_fn;
    use tokio::net::UdpSocket;

    #[tokio::test]
    async fn port_rotation_and_wire_rate_limit() {
        tokio::time::timeout(Duration::from_secs(5), async {
            let first = UdpSocket::bind("127.0.0.1:0").await.unwrap();
            let second = UdpSocket::bind("127.0.0.1:0").await.unwrap();
            let peer = first.local_addr().unwrap();
            let raw = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            let socket = PacketTransport::new(
                quinn::TokioRuntime.wrap_udp_socket(raw).unwrap(),
                peer,
                vec![peer.port(), second.local_addr().unwrap().port()],
                None,
            );
            let contents = [42; 100];
            let tx = Transmit {
                destination: peer,
                contents: &contents,
                ecn: None,
                segment_size: None,
                src_ip: None,
            };
            socket.rate.store(1000, Ordering::Relaxed);
            // Prime OS write readiness independently of the rate limiter.
            let mut socket = Arc::new(socket);
            let mut poller = socket.clone().create_io_poller();
            poll_fn(|cx| poller.as_mut().poll_writable(cx))
                .await
                .unwrap();
            socket.try_send(&tx).unwrap();
            assert_eq!(
                socket.try_send(&tx).unwrap_err().kind(),
                io::ErrorKind::WouldBlock
            );
            let mut buf = [0; 100];
            first.recv(&mut buf).await.unwrap();
            assert_eq!(buf, contents);
            let deadline = *socket.next_send.lock().unwrap();
            poll_fn(|cx| poller.as_mut().poll_writable(cx))
                .await
                .unwrap();
            assert!(Instant::now() >= deadline);
            drop(poller);
            // Move the hop epoch across the interval on the same bound socket.
            Arc::get_mut(&mut socket).unwrap().started -= Duration::from_secs(30);
            socket.try_send(&tx).unwrap();
            let (n, local) = second.recv_from(&mut buf).await.unwrap();
            assert_eq!(&buf[..n], contents);
            second.send_to(&contents, local).await.unwrap();
            let mut meta = [RecvMeta::default()];
            poll_fn(|cx| socket.poll_recv(cx, &mut [IoSliceMut::new(&mut buf)], &mut meta))
                .await
                .unwrap();
            assert_eq!(meta[0].addr, peer);
            assert_eq!(meta[0].len, contents.len());
        })
        .await
        .unwrap();
    }
}
