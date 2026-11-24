//! Trojan outbound dialer over TLS (TCP + UDP Associate).

use std::{io, sync::Arc};

use sha2::{Digest, Sha224};
use tokio::io::{AsyncReadExt, AsyncWriteExt, ReadHalf, WriteHalf};

use crate::{
    dialer::{
        BoxFuture, BoxedStream, DirectDialer, OutboundDialer, OutboundUdpDialer, TargetAddr,
        UdpPacketSocket,
    },
    transport::tls::{connect_tls, TlsConfig},
};

#[derive(Clone, Debug)]
pub struct TrojanConfig {
    pub server: TargetAddr,
    pub password: String,
    pub tls: TlsConfig,
}

pub struct TrojanDialer {
    cfg: TrojanConfig,
    password_hex: String,
    underlay: Arc<dyn OutboundDialer>,
}

impl TrojanDialer {
    pub fn new(cfg: TrojanConfig) -> Self {
        Self::with_underlay(cfg, Arc::new(DirectDialer))
    }

    pub fn with_underlay(cfg: TrojanConfig, underlay: Arc<dyn OutboundDialer>) -> Self {
        let digest = Sha224::digest(cfg.password.as_bytes());
        let password_hex = digest.iter().map(|b| format!("{b:02x}")).collect();
        Self {
            cfg,
            password_hex,
            underlay,
        }
    }

    async fn open_cmd(&self, cmd: u8, target: &TargetAddr) -> io::Result<BoxedStream> {
        let raw = self.underlay.dial_tcp(&self.cfg.server).await?;
        let mut tls = connect_tls(raw, &self.cfg.server.host(), &self.cfg.tls).await?;

        let mut header = Vec::with_capacity(56 + 2 + 1 + 64 + 2);
        header.extend_from_slice(self.password_hex.as_bytes());
        header.extend_from_slice(b"\r\n");
        header.push(cmd);
        target.write_socks_addr(&mut header)?;
        header.extend_from_slice(b"\r\n");

        tls.write_all(&header).await?;
        tls.flush().await?;
        Ok(tls)
    }
}

impl OutboundDialer for TrojanDialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }

    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move { self.open_cmd(0x01, target).await })
    }
}

impl OutboundUdpDialer for TrojanDialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async move {
            let dummy = TargetAddr::Ip(([0, 0, 0, 0], 0).into());
            let stream = self.open_cmd(0x03, &dummy).await?;
            let (reader, writer) = tokio::io::split(stream);
            Ok(Box::new(TrojanUdpSocket {
                reader: tokio::sync::Mutex::new(reader),
                writer: tokio::sync::Mutex::new(writer),
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}

struct TrojanUdpSocket {
    reader: tokio::sync::Mutex<ReadHalf<BoxedStream>>,
    writer: tokio::sync::Mutex<WriteHalf<BoxedStream>>,
}

impl UdpPacketSocket for TrojanUdpSocket {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            let len = u16::try_from(payload.len())
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "UDP payload too large"))?;
            let mut pkt = Vec::with_capacity(64 + 4 + payload.len());
            target.write_socks_addr(&mut pkt)?;
            pkt.extend_from_slice(&len.to_be_bytes());
            pkt.extend_from_slice(b"\r\n");
            pkt.extend_from_slice(payload);

            let mut w = self.writer.lock().await;
            w.write_all(&pkt).await?;
            w.flush().await?;
            Ok(payload.len())
        })
    }

    fn recv_from<'a>(
        &'a self,
        buf: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            let mut r = self.reader.lock().await;
            let addr = TargetAddr::read_socks_addr(&mut *r).await?;
            let mut hdr = [0u8; 4];
            r.read_exact(&mut hdr).await?;
            if &hdr[2..4] != b"\r\n" {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "invalid Trojan UDP framing",
                ));
            }
            let len = u16::from_be_bytes([hdr[0], hdr[1]]) as usize;
            if len > buf.len() {
                let mut discard = vec![0u8; len];
                r.read_exact(&mut discard).await?;
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "receive buffer too small for Trojan UDP packet",
                ));
            }
            r.read_exact(&mut buf[..len]).await?;
            Ok((len, addr))
        })
    }
}
