use crate::dialer::{
    BoxFuture, BoxedStream, OutboundDialer, OutboundUdpDialer, TargetAddr, UdpPacketSocket,
};
use std::io::{self, Read, Write};
use std::net::IpAddr;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

pub struct SocksOutbound {
    pub server: TargetAddr,
    pub first_hop: bool,
    pub credentials: Option<(String, String)>,
    pub underlay: Arc<dyn OutboundDialer>,
}

impl SocksOutbound {
    async fn open(
        &self,
        command: u8,
        target: &TargetAddr,
    ) -> io::Result<(BoxedStream, TargetAddr)> {
        let mut stream = self.underlay.dial_tcp(&self.server).await?;
        let method = if self.credentials.is_some() { 2 } else { 0 };
        async_exchange(&mut stream, &[5, 1, method], [5, method]).await?;
        if let Some((user, password)) = &self.credentials {
            let mut auth = vec![1];
            append_field(&mut auth, user.as_bytes())?;
            append_field(&mut auth, password.as_bytes())?;
            async_exchange(&mut stream, &auth, [1, 0]).await?;
        }
        let mut request = vec![5, command, 0];
        target.write_socks_addr(&mut request)?;
        stream.write_all(&request).await?;
        stream.flush().await?;
        let mut header = [0; 3];
        stream.read_exact(&mut header).await?;
        if header[0] != 5 || header[2] != 0 {
            return Err(invalid_reply());
        }
        if header[1] != 0 {
            return Err(io::Error::other(format!(
                "SOCKS5 command {command} rejected with {}",
                header[1]
            )));
        }
        let bound = TargetAddr::read_socks_addr(&mut stream).await?;
        Ok((stream, bound))
    }
}

impl OutboundDialer for SocksOutbound {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        self.first_hop.then_some(self)
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move { Ok(self.open(1, target).await?.0) })
    }
}

struct Association {
    _control: tokio::sync::Mutex<BoxedStream>,
    relay: tokio::net::UdpSocket,
}

impl OutboundUdpDialer for SocksOutbound {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async move {
            let (control, bound) = self.open(3, &TargetAddr::Ip(([0; 4], 0).into())).await?;
            let host = match &bound {
                TargetAddr::Ip(ip) if ip.ip().is_unspecified() => self.server.host(),
                _ => bound.host(),
            };
            let peer = tokio::net::lookup_host((host, bound.port()))
                .await?
                .next()
                .ok_or_else(|| io::Error::other("SOCKS5 UDP relay address unresolved"))?;
            let bind: std::net::SocketAddr = if peer.is_ipv4() {
                ([0; 4], 0).into()
            } else {
                ([0; 16], 0).into()
            };
            let relay = tokio::net::UdpSocket::bind(bind).await?;
            #[cfg(unix)]
            crate::dialer::protect_socket_fd(std::os::fd::AsRawFd::as_raw_fd(&relay))?;
            relay.connect(peer).await?;
            Ok(Box::new(Association {
                _control: tokio::sync::Mutex::new(control),
                relay,
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}

impl UdpPacketSocket for Association {
    fn send_to<'a>(
        &'a self,
        data: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            let mut frame = vec![0; 3];
            target.write_socks_addr(&mut frame)?;
            frame.extend_from_slice(data);
            self.relay.send(&frame).await?;
            Ok(data.len())
        })
    }
    fn recv_from<'a>(
        &'a self,
        out: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            let mut frame = vec![0; 65536];
            let count = self.relay.recv(&mut frame).await?;
            if count < 4 || frame[..3] != [0, 0, 0] {
                return Err(invalid_reply());
            }
            let mut payload = &frame[3..count];
            let source = TargetAddr::read_socks_addr(&mut payload).await?;
            if payload.len() > out.len() {
                return Err(io::Error::other(
                    "SOCKS5 UDP response exceeds receive buffer",
                ));
            }
            out[..payload.len()].copy_from_slice(payload);
            Ok((payload.len(), source))
        })
    }
}

async fn async_exchange(
    stream: &mut BoxedStream,
    request: &[u8],
    expected: [u8; 2],
) -> io::Result<()> {
    stream.write_all(request).await?;
    stream.flush().await?;
    let mut reply = [0; 2];
    stream.read_exact(&mut reply).await?;
    if reply != expected {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "SOCKS5 authentication rejected",
        ));
    }
    Ok(())
}

/// Negotiate CONNECT on an already connected upstream stream. The caller owns
/// the socket, its timeouts, and its lifecycle. Leave all tunnel data unread.
pub fn connect(
    stream: &mut (impl Read + Write),
    host: &str,
    port: u16,
    credentials: Option<(&[u8], &[u8])>,
) -> io::Result<()> {
    let mut request = vec![5, 1, 0];
    match host.parse::<IpAddr>() {
        Ok(IpAddr::V4(ip)) => {
            request.push(1);
            request.extend(ip.octets());
        }
        Ok(IpAddr::V6(ip)) => {
            request.push(4);
            request.extend(ip.octets());
        }
        Err(_) => {
            request.push(3);
            append_field(&mut request, host.as_bytes())?;
        }
    }
    request.extend(port.to_be_bytes());
    let mut auth = vec![1];
    let method = if let Some((user, password)) = credentials {
        append_field(&mut auth, user)?;
        append_field(&mut auth, password)?;
        2
    } else {
        0
    };
    exchange(stream, &[5, 1, method], [5, method])?;
    if credentials.is_some() {
        exchange(stream, &auth, [1, 0])?;
    }
    stream.write_all(&request)?;
    stream.flush()?;
    let mut header = [0; 4];
    stream.read_exact(&mut header)?;
    if header[0] != 5 || header[2] != 0 {
        return Err(invalid_reply());
    }
    if header[1] != 0 {
        return Err(io::Error::other(format!(
            "SOCKS5 CONNECT failed with reply {}",
            header[1]
        )));
    }
    let addr_len = match header[3] {
        1 => 4,
        4 => 16,
        3 => {
            let mut len = [0];
            stream.read_exact(&mut len)?;
            if len[0] == 0 {
                return Err(invalid_reply());
            }
            usize::from(len[0])
        }
        _ => return Err(invalid_reply()),
    };
    let mut bound = [0; 257];
    stream.read_exact(&mut bound[..addr_len + 2])
}

fn append_field(out: &mut Vec<u8>, value: &[u8]) -> io::Result<()> {
    if value.is_empty() || value.len() > 255 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "SOCKS5 field must contain 1 to 255 bytes",
        ));
    }
    out.push(value.len() as u8);
    out.extend_from_slice(value);
    Ok(())
}

fn invalid_reply() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "invalid SOCKS5 reply")
}

fn exchange(stream: &mut (impl Read + Write), request: &[u8], expected: [u8; 2]) -> io::Result<()> {
    stream.write_all(request)?;
    stream.flush()?;
    let mut reply = [0; 2];
    stream.read_exact(&mut reply)?;
    if reply != expected {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "SOCKS5 authentication rejected",
        ));
    }
    Ok(())
}
