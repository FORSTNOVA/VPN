use smartvpn_engine::{
    config_bridge::build_router_from_clash_yaml, dns_server::DnsServer, inbound::MixedServer,
    router::ProcessResolver,
};
use std::{
    fs, io,
    net::{Ipv4Addr, SocketAddr},
    path::Path,
    sync::Arc,
};

pub fn run_embedded_engine(config_path: &Path, dns_listen: Option<SocketAddr>) -> io::Result<()> {
    run_with_ready(config_path, dns_listen, || Ok(()))
}

/// Signals managed-mode readiness only after every configured listener is bound.
pub fn run_with_ready(
    config_path: &Path,
    dns_listen: Option<SocketAddr>,
    ready: impl FnOnce() -> io::Result<()>,
) -> io::Result<()> {
    #[cfg(windows)]
    let resolver: Option<Arc<dyn ProcessResolver>> = Some(Arc::new(
        smartvpn_engine::process_win::WindowsProcessResolver,
    ));
    #[cfg(not(windows))]
    let resolver: Option<Arc<dyn ProcessResolver>> = None;
    let bundle = build_router_from_clash_yaml(&fs::read_to_string(config_path)?, resolver)?;
    tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()?
        .block_on(async {
            let mixed = MixedServer::bind(
                (Ipv4Addr::LOCALHOST, bundle.mixed_port).into(),
                bundle.router.clone(),
            )
            .await?;
            if let Some(addr) = dns_listen.or(bundle.dns_listen) {
                let dns = DnsServer::bind(
                    addr,
                    bundle.fake_ip_pool,
                    Arc::new(bundle.router.rules.clone()),
                    None,
                )
                .await?;
                ready()?;
                tokio::select! {
                    result = mixed.serve() => result,
                    result = dns.serve() => result,
                }
            } else {
                ready()?;
                mixed.serve().await
            }
        })
}
