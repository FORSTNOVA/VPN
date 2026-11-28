use super::{datagram::UdpPacket, utils::RemoteConnector};
use crate::{app::dns::ThreadSafeDNSResolver, session::{Session, SocksAddr}};
use futures::{SinkExt, StreamExt};
use std::{io, net::{Ipv4Addr, SocketAddr, UdpSocket}};
use tokio::task::JoinHandle;

/// Adapt the QUIC libraries' native UDP sockets to the preceding outbound's datagram channel.
pub(crate) struct QuicRelay {
    pub peer: SocketAddr,
    pub socket: UdpSocket,
    task: JoinHandle<()>,
}

impl Drop for QuicRelay {
    fn drop(&mut self) {
        self.task.abort();
    }
}

impl QuicRelay {
    pub fn clone_socket(&self) -> io::Result<UdpSocket> { self.socket.try_clone() }

    pub async fn open(connector: &dyn RemoteConnector, resolver: ThreadSafeDNSResolver,
                      sess: &Session, remote: SocketAddr) -> io::Result<Self> {
        let client = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0))?;
        client.set_nonblocking(true)?;
        let source = client.local_addr()?;
        let relay = tokio::net::UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).await?;
        let peer = relay.local_addr()?;
        relay.connect(source).await?;
        let upstream = connector.connect_datagram(resolver, Some(source), SocksAddr::Ip(remote),
            sess.iface.as_ref(), #[cfg(target_os = "linux")] sess.so_mark).await?;
        let (mut send, mut recv) = upstream.split();
        let task = tokio::spawn(async move {
            let forward = async {
                let mut buf = vec![0; 65536];
                loop {
                    let count = relay.recv(&mut buf).await?;
                    send.send(UdpPacket { data: buf[..count].to_vec(), src_addr: SocksAddr::Ip(source),
                        dst_addr: SocksAddr::Ip(remote), inbound_user: None }).await?;
                }
                #[allow(unreachable_code)]
                Ok::<(), io::Error>(())
            };
            let reverse = async {
                while let Some(packet) = recv.next().await {
                    if packet.src_addr == SocksAddr::Ip(remote) {
                        relay.send(&packet.data).await?;
                    }
                }
                Err::<(), _>(io::Error::new(io::ErrorKind::UnexpectedEof, "upstream UDP association closed"))
            };
            if let Err(error) = tokio::try_join!(forward, reverse) {
                tracing::warn!(%remote, %error, "QUIC upstream relay stopped");
            }
        });
        Ok(Self { peer, socket: client, task })
    }
}
