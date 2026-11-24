//! Trojan outbound dialer over TLS.

use std::{io, sync::Arc};

use sha2::{Digest, Sha224};
use tokio::io::AsyncWriteExt;

use crate::{
    dialer::{BoxFuture, BoxedStream, DirectDialer, OutboundDialer, TargetAddr},
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
}

impl OutboundDialer for TrojanDialer {
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let raw = self.underlay.dial_tcp(&self.cfg.server).await?;
            let mut tls = connect_tls(raw, &self.cfg.server.host(), &self.cfg.tls).await?;

            let mut header = Vec::with_capacity(56 + 2 + 1 + 64 + 2);
            header.extend_from_slice(self.password_hex.as_bytes());
            header.extend_from_slice(b"\r\n");
            header.push(0x01); // CMD_CONNECT
            target.write_socks_addr(&mut header)?;
            header.extend_from_slice(b"\r\n");

            tls.write_all(&header).await?;
            tls.flush().await?;
            Ok(tls)
        })
    }
}
