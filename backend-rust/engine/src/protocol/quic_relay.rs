use crate::dialer::{OutboundDialer, TargetAddr};
use std::{io, net::SocketAddr};

pub(super) struct Relay(tokio::task::JoinHandle<()>);
impl Drop for Relay {
    fn drop(&mut self) {
        self.0.abort();
    }
}

pub(super) async fn open_relay(
    upstream: &dyn OutboundDialer,
    remote: TargetAddr,
    client: SocketAddr,
) -> io::Result<(SocketAddr, Relay)> {
    let packets = upstream
        .udp()
        .expect("validated QUIC underlay")
        .dial_udp()
        .await?;
    let relay = tokio::net::UdpSocket::bind((std::net::Ipv4Addr::LOCALHOST, 0)).await?;
    relay.connect(client).await?;
    let peer = relay.local_addr()?;
    let worker = tokio::spawn(async move {
        let mut outbound = vec![0; 65536];
        let mut inbound = vec![0; 65536];
        let forward = async {
            loop {
                let n = relay.recv(&mut outbound).await?;
                packets.send_to(&outbound[..n], &remote).await?;
            }
        };
        let reverse = async {
            loop {
                let (n, _) = packets.recv_from(&mut inbound).await?;
                relay.send(&inbound[..n]).await?;
            }
        };
        let result: io::Result<()> = tokio::select! { r = forward => r, r = reverse => r };
        if let Err(e) = result {
            eprintln!("QUIC chained UDP relay ended: {e}");
        }
    });
    Ok((peer, Relay(worker)))
}
