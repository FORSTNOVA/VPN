#[cfg(windows)]
mod managed;
#[cfg(windows)]
mod proxy_restore;
fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let result = match args.as_slice() {
        [] => smartvpn_host::wire::run(&mut std::io::stdin().lock(), &mut std::io::stdout().lock()),
        [mode] if mode == "--offline-stdio" => {
            smartvpn_host::wire::run(&mut std::io::stdin().lock(), &mut std::io::stdout().lock())
        }
        [mode, path] if mode == "--offline-disk" => smartvpn_host::wire::run_disk(
            &mut std::io::stdin().lock(),
            &mut std::io::stdout().lock(),
            std::path::Path::new(path),
        ),
        #[cfg(windows)]
        [mode] if mode == "--proxy-restore-plan" => proxy_restore::run(),
        #[cfg(windows)]
        [mode, kernel, home, config, port] if mode == "--managed" => {
            managed::run(kernel, home, config, port)
        }
        [mode, port] if mode == "--reads" || mode == "--api" => (|| {
            let (session, bound) = smartvpn_host::monitor::embedded::start_api(
                port.parse().unwrap_or(0),
                &std::env::var("SMARTVPN_RESOURCE_TOKEN").unwrap_or_default(),
                &std::env::var("SMARTVPN_MONITOR_TOKEN").unwrap_or_default(),
                mode == "--api",
            )
            .ok_or_else(|| std::io::Error::other("read listener initialization failed"))?;
            println!("127.0.0.1:{bound}");
            let _ = std::io::copy(&mut std::io::stdin().lock(), &mut std::io::sink());
            unsafe {
                smartvpn_host::monitor::embedded::smartvpn_host_stop_v1(Box::into_raw(Box::new(
                    session,
                )))
            };
            Ok(())
        })(),
        [mode] if mode == "--http" => smartvpn_host::monitor::serve(None),
        [mode, port] if mode == "--http" => match port.parse::<u16>() {
            Ok(port) if port != 0 => smartvpn_host::monitor::serve(Some(port)),
            _ => Err(std::io::Error::other("invalid loopback controller port")),
        },
        _ => {
            eprintln!(
                "usage: --offline-stdio | --offline-disk <directory> | --http [controller-port]"
            );
            std::process::exit(2);
        }
    };
    if let Err(error) = result {
        eprintln!("SmartVPN host stopped: {error}");
        std::process::exit(1);
    }
}
