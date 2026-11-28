use crate::dialer::{BoxFuture, BoxedStream, OutboundDialer, OutboundUdpDialer, TargetAddr};
use smartvpn_sing_mux::{SessionPool, HOST, PORT};
use std::{io, sync::Arc};

pub struct Mux {
    outbound: Arc<dyn OutboundDialer>,
    pool: SessionPool,
}
impl Mux {
    pub fn new(outbound: Arc<dyn OutboundDialer>) -> Self {
        Self {
            outbound,
            pool: SessionPool::default(),
        }
    }
}
impl OutboundDialer for Mux {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        self.outbound.udp()
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let mut header = vec![0, 0];
            target.write_socks_addr(&mut header)?;
            let server = TargetAddr::Domain(HOST.into(), PORT);
            let stream = self
                .pool
                .open(header, self.outbound.dial_tcp(&server))
                .await?;
            Ok(Box::new(stream) as BoxedStream)
        })
    }
}
