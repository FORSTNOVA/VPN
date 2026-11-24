use crate::{
    app::{
        dispatcher::Dispatcher,
        dns::{ThreadSafeDNSResolver, exchange_with_resolver},
        net::DEFAULT_OUTBOUND_INTERFACE,
    },
    common::errors::new_io_error,
    proxy::datagram::UdpPacket,
    session::{Network, Session, Type},
};
use futures::{Sink, Stream, ready};
use std::{sync::Arc, task::Poll};
use tracing::{debug, trace, warn};

fn spawn_dns_query(
    queries: &mut tokio::task::JoinSet<()>,
    query: impl Future<Output = ()> + Send + 'static,
) {
    while let Some(result) = queries.try_join_next() {
        result.expect("TUN DNS task panicked");
    }
    if queries.len() == 128 {
        warn!("TUN DNS concurrency limit reached");
    } else {
        queries.spawn(query);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn smartvpn_dns_slow_query_does_not_hold_next_reply() {
        let mut queries = tokio::task::JoinSet::new();
        let (release, wait) = tokio::sync::oneshot::channel::<()>();
        spawn_dns_query(&mut queries, async move { wait.await.unwrap() });
        spawn_dns_query(&mut queries, async {});
        tokio::time::timeout(
            std::time::Duration::from_secs(1),
            queries.join_next(),
        )
        .await
        .unwrap()
        .unwrap()
        .unwrap();
        release.send(()).unwrap();
        queries.join_next().await.unwrap().unwrap();
    }
}

pub(crate) async fn handle_inbound_datagram(
    socket: watfaq_netstack::UdpSocket,
    dispatcher: Arc<Dispatcher>,
    resolver: ThreadSafeDNSResolver,
    so_mark: Option<u32>,
    dns_hijack: bool,
) {
    // tun i/o
    // lr: app packets went into tun will be accessed from lr
    // ls: packet written into ls will go back to app from tun
    let (mut lr, mut ls) = socket.split();
    let ls_dns = ls.clone(); // for dns hijack
    let resolver_dns = resolver.clone(); // for dns hijack

    // dispatcher <-> tun communications
    // l_tx: dispatcher write packet responded from remote proxy
    // l_rx: in fut1 items are forwarded to ls
    let (l_tx, mut l_rx) = tokio::sync::mpsc::channel::<UdpPacket>(32);

    // forward packets from tun to dispatcher
    let (d_tx, d_rx) = tokio::sync::mpsc::channel::<UdpPacket>(32);

    // for dispatcher - the dispatcher would receive packets from this channel,
    // which is from the stack and send back packets to this channel, which
    // is to the tun
    let udp_stream = TunDatagram::new(l_tx, d_rx);

    let default_outbound = DEFAULT_OUTBOUND_INTERFACE.read().await;
    let sess = Session {
        network: Network::Udp,
        typ: Type::Tun,
        iface: default_outbound.clone().inspect(|x| {
            debug!("selecting outbound interface: {:?} for tun UDP traffic", x);
        }),
        so_mark,
        ..Default::default()
    };

    let closer = dispatcher
        .dispatch_datagram(sess, Box::new(udp_stream))
        .await;

    // dispatcher -> tun
    let fut1 = tokio::spawn(async move {
        while let Some(pkt) = l_rx.recv().await {
            trace!("tun <- dispatcher: {:?}", pkt);
            if let Err(e) = ls
                .send(
                    (
                        pkt.data,
                        pkt.src_addr.must_into_socket_addr(),
                        pkt.dst_addr.must_into_socket_addr(),
                    )
                        .into(),
                )
                .await
            {
                warn!("failed to send udp packet to netstack: {}", e);
            }
        }
    });

    // tun -> dispatcher
    let fut2 = tokio::spawn(async move {
        let mut queries = tokio::task::JoinSet::new();
        'read_packet: while let Some(watfaq_netstack::UdpPacket {
            data,
            local_addr,
            remote_addr,
        }) = lr.recv().await
        {
            if remote_addr.ip().is_multicast() {
                continue;
            }
            let pkt = UdpPacket {
                data: data.data().into(),
                src_addr: local_addr.into(),
                dst_addr: remote_addr.into(),
                inbound_user: None,
            };

            trace!("tun -> dispatcher: {:?}", pkt);

            if dns_hijack && pkt.dst_addr.port() == 53 {
                trace!(
                    "got dns packet: {:?}, returning from Clash DNS server",
                    pkt
                );

                let mut reply = ls_dns.clone();
                let resolver = resolver_dns.clone();
                spawn_dns_query(&mut queries, async move {
                    let result: anyhow::Result<()> = async {
                        let msg = hickory_proto::op::Message::from_vec(&pkt.data)?;
                        let mut response =
                            exchange_with_resolver(&resolver, &msg, true).await?;
                        response.metadata.id = msg.metadata.id;
                        reply
                            .send(
                                (response.to_vec()?, remote_addr, local_addr)
                                    .into(),
                            )
                            .await?;
                        Ok(())
                    }
                    .await;
                    if let Err(error) = result {
                        warn!("TUN DNS exchange failed: {error}");
                    }
                });

                // don't forward dns packet to dispatcher
                continue 'read_packet;
            }

            match d_tx.send(pkt).await {
                Ok(_) => {}
                Err(e) => {
                    warn!("failed to send udp packet to proxy: {}", e);
                }
            }
        }

        closer.send(0).ok();
    });

    debug!("tun UDP ready");

    let _ = futures::future::join(fut1, fut2).await;
}

#[derive(Debug)]
pub struct TunDatagram {
    rx: tokio::sync::mpsc::Receiver<UdpPacket>,
    tx: tokio::sync::mpsc::Sender<UdpPacket>,

    pkt: Option<UdpPacket>,
    flushed: bool,
}

impl TunDatagram {
    pub fn new(
        // send to tun
        tx: tokio::sync::mpsc::Sender<UdpPacket>,
        // receive from tun
        rx: tokio::sync::mpsc::Receiver<UdpPacket>,
    ) -> Self {
        Self {
            rx,
            tx,
            pkt: None,
            flushed: true,
        }
    }
}

impl Stream for TunDatagram {
    type Item = UdpPacket;

    fn poll_next(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Self::Item>> {
        self.rx.poll_recv(cx)
    }
}

impl Sink<UdpPacket> for TunDatagram {
    type Error = std::io::Error;

    fn poll_ready(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Result<(), Self::Error>> {
        if !self.flushed {
            match self.poll_flush(cx)? {
                Poll::Ready(()) => {}
                Poll::Pending => return Poll::Pending,
            }
        }

        Poll::Ready(Ok(()))
    }

    fn start_send(
        self: std::pin::Pin<&mut Self>,
        item: UdpPacket,
    ) -> Result<(), Self::Error> {
        let pin = self.get_mut();
        pin.pkt = Some(item);
        pin.flushed = false;
        Ok(())
    }

    fn poll_flush(
        mut self: std::pin::Pin<&mut Self>,
        _: &mut std::task::Context<'_>,
    ) -> Poll<Result<(), Self::Error>> {
        if self.flushed {
            return Poll::Ready(Ok(()));
        }

        let Self {
            ref mut tx,
            ref mut pkt,
            ref mut flushed,
            ..
        } = *self;

        let pkt = pkt
            .take()
            .ok_or(new_io_error("no packet to send, call start_send first"))?;

        match tx.try_send(pkt) {
            Ok(_) => {
                *flushed = true;
                Poll::Ready(Ok(()))
            }
            Err(err) => {
                self.pkt = Some(err.into_inner());
                Poll::Ready(Err(new_io_error(
                    "could not send packet, queue full or disconnected",
                )))
            }
        }
    }

    fn poll_close(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> Poll<Result<(), Self::Error>> {
        ready!(self.poll_flush(cx))?;
        Poll::Ready(Ok(()))
    }
}
