use super::{address, invalid, put_short_varint, read_field, Session};
use crate::dialer::{BoxFuture, TargetAddr, UdpPacketSocket};
use bytes::Bytes;
use quinn::Connection;
use std::{
    collections::HashMap,
    io,
    net::SocketAddr,
    sync::{
        atomic::{AtomicU16, AtomicU32, Ordering},
        Arc, Mutex,
    },
    time::{Duration, Instant},
};
use tokio::sync::{mpsc, Mutex as AsyncMutex};

const MAX_PAYLOAD: usize = 65507;
const MAX_PENDING: usize = 32;
const FRAGMENT_TTL: Duration = Duration::from_secs(5);

#[derive(Default)]
pub(super) struct Sessions {
    next: AtomicU32,
    queues: Mutex<HashMap<u32, mpsc::Sender<Bytes>>>,
}

impl Sessions {
    pub(super) fn start(conn: Connection) -> Arc<Self> {
        let sessions = Arc::new(Self::default());
        let routing = sessions.clone();
        tokio::spawn(async move {
            while let Ok(frame) = conn.read_datagram().await {
                if frame.len() < 4 {
                    conn.close(1u32.into(), b"missing Hysteria UDP session ID");
                    break;
                }
                let id = u32::from_be_bytes(frame[..4].try_into().unwrap());
                if let Some(queue) = routing.queues.lock().unwrap().get(&id) {
                    // UDP is unreliable. A full or closed session must never stall other sessions.
                    let _ = queue.try_send(frame.slice(4..));
                }
            }
            routing.queues.lock().unwrap().clear();
        });
        sessions
    }

    pub(super) fn open(&self, session: Arc<Session>) -> io::Result<PacketSocket> {
        if session.conn.max_datagram_size().is_none() {
            return Err(io::Error::new(
                io::ErrorKind::Unsupported,
                "QUIC peer did not negotiate datagrams",
            ));
        }
        let id = self
            .next
            .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |n| n.checked_add(1))
            .map_err(|_| io::Error::other("exhausted Hysteria UDP session IDs"))?;
        let (sender, queue) = mpsc::channel(256);
        self.queues.lock().unwrap().insert(id, sender);
        Ok(PacketSocket {
            session,
            id,
            next_packet: AtomicU16::new(0),
            receiver: AsyncMutex::new(Receiver {
                queue,
                partial: HashMap::new(),
            }),
        })
    }
}

pub(super) struct PacketSocket {
    session: Arc<Session>,
    id: u32,
    next_packet: AtomicU16,
    receiver: AsyncMutex<Receiver>,
}

impl Drop for PacketSocket {
    fn drop(&mut self) {
        self.session
            .sessions
            .as_ref()
            .unwrap()
            .queues
            .lock()
            .unwrap()
            .remove(&self.id);
    }
}

struct Partial {
    first: Instant,
    addr: TargetAddr,
    pieces: Vec<Option<Bytes>>,
    received: usize,
    size: usize,
}

struct Receiver {
    queue: mpsc::Receiver<Bytes>,
    partial: HashMap<u16, Partial>,
}

fn parse_address(bytes: Vec<u8>) -> io::Result<TargetAddr> {
    let text = std::str::from_utf8(&bytes).map_err(invalid)?;
    if let Ok(addr) = text.parse::<SocketAddr>() {
        return Ok(TargetAddr::Ip(addr));
    }
    let (host, port) = text
        .rsplit_once(':')
        .ok_or_else(|| invalid("missing UDP address port"))?;
    let target = TargetAddr::domain(host, port.parse().map_err(invalid)?);
    address(&target).map_err(invalid)?;
    Ok(target)
}

impl Receiver {
    async fn assemble(&mut self, frame: Bytes) -> io::Result<Option<(TargetAddr, Vec<u8>)>> {
        if frame.len() < 5 {
            return Err(invalid("truncated Hysteria UDP frame"));
        }
        let packet = u16::from_be_bytes([frame[0], frame[1]]);
        let index = frame[2] as usize;
        let count = frame[3] as usize;
        let mut rest = &frame[4..];
        let addr = parse_address(read_field(&mut rest, 1024).await?)?;
        if count == 1 {
            return Ok(Some((addr, rest.to_vec())));
        }
        if index >= count {
            return Err(invalid("invalid Hysteria UDP fragment index"));
        }
        let now = Instant::now();
        self.partial
            .retain(|_, p| now.duration_since(p.first) < FRAGMENT_TTL);
        if !self.partial.contains_key(&packet) && self.partial.len() == MAX_PENDING {
            let oldest = *self.partial.iter().min_by_key(|(_, p)| p.first).unwrap().0;
            self.partial.remove(&oldest);
        }
        let p = self.partial.entry(packet).or_insert_with(|| Partial {
            first: now,
            addr: addr.clone(),
            pieces: vec![None; count],
            received: 0,
            size: 0,
        });
        if p.addr != addr || p.pieces.len() != count {
            self.partial.remove(&packet);
            return Err(invalid("inconsistent Hysteria UDP fragments"));
        }
        if p.pieces[index].is_none() {
            p.size += rest.len();
            if p.size > MAX_PAYLOAD {
                self.partial.remove(&packet);
                return Err(invalid("reassembled UDP payload exceeds maximum size"));
            }
            p.pieces[index] = Some(frame.slice(frame.len() - rest.len()..));
            p.received += 1;
        }
        if p.received != count {
            return Ok(None);
        }
        let p = self.partial.remove(&packet).unwrap();
        let mut payload = Vec::with_capacity(p.size);
        for piece in p.pieces {
            payload.extend_from_slice(&piece.unwrap());
        }
        Ok(Some((p.addr, payload)))
    }
}

impl UdpPacketSocket for PacketSocket {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            if payload.len() > MAX_PAYLOAD {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "UDP payload too large",
                ));
            }
            let addr = address(target)?;
            let conn = &self.session.conn;
            let mtu = conn.max_datagram_size().ok_or_else(|| {
                io::Error::new(io::ErrorKind::Unsupported, "QUIC datagrams unavailable")
            })?;
            let mut header = self.id.to_be_bytes().to_vec();
            header.extend_from_slice(
                &self
                    .next_packet
                    .fetch_add(1, Ordering::Relaxed)
                    .to_be_bytes(),
            );
            header.extend_from_slice(&[0, 0]);
            put_short_varint(&mut header, addr.len() as u16);
            header.extend_from_slice(addr.as_bytes());
            let chunk = mtu
                .checked_sub(header.len())
                .filter(|n| *n > 0)
                .ok_or_else(|| invalid("UDP address exceeds QUIC datagram MTU"))?;
            let count = payload.len().div_ceil(chunk).max(1);
            header[7] = u8::try_from(count).map_err(invalid)?;
            for index in 0..count {
                header[6] = index as u8;
                let mut frame = header.clone();
                frame.extend_from_slice(
                    &payload[index * chunk..payload.len().min((index + 1) * chunk)],
                );
                conn.send_datagram_wait(frame.into())
                    .await
                    .map_err(io::Error::other)?;
            }
            Ok(payload.len())
        })
    }

    fn recv_from<'a>(
        &'a self,
        buf: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            let mut receiver = self.receiver.lock().await;
            loop {
                let frame = tokio::select! {
                    frame = receiver.queue.recv() => frame.ok_or_else(|| io::Error::new(io::ErrorKind::ConnectionAborted,"Hysteria datagram channel closed"))?,
                    reason = self.session.conn.closed() => return Err(reason.into()),
                };
                if let Some((addr, payload)) = receiver.assemble(frame).await? {
                    if payload.len() > buf.len() {
                        return Err(io::Error::new(
                            io::ErrorKind::InvalidInput,
                            "receive buffer too small for UDP packet",
                        ));
                    }
                    buf[..payload.len()].copy_from_slice(&payload);
                    return Ok((payload.len(), addr));
                }
            }
        })
    }
}
