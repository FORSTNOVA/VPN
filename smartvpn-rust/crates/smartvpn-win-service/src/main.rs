use anyhow::{anyhow, bail, ensure, Context, Result};
use clash_lib::{Config, Options};
use serde_json::Value;
use smartvpn_runtime::{config::Connect, token, Controller, PlaneStatus, Runtime, TunnelDataPlane};
use std::{
    io::{self, Write},
    path::PathBuf,
    sync::{Arc, Mutex},
    thread,
    time::{Duration, Instant},
};
struct Kernel {
    worker: thread::JoinHandle<()>,
    cancel: tokio_util::sync::CancellationToken,
    controller: Controller,
    mixed: u16,
}
struct Windows {
    dir: PathBuf,
    kernel: Option<Kernel>,
}
impl TunnelDataPlane for Windows {
    fn connect(&mut self, body: &[u8], stored: &[serde_json::Value]) -> Result<()> {
        ensure!(!self.status()?.connected, "kernel already running");
        let req: Connect = serde_json::from_slice(body).context("expected directory and config")?;
        let dir = req.directory.canonicalize()?;
        let secret = token()?;
        req.config.prepare_runtime(&dir)?;
        let mut config = req.config.generate(&secret)?;
        config["proxies"]
            .as_array_mut()
            .unwrap()
            .extend_from_slice(stored);
        let parsed = Config::Str(config.to_string())
            .try_parse_strict()
            .map_err(|_| anyhow!("invalid kernel profile"))?;
        let (worker, cancel) = clash_lib::start_scaffold_instance(Options {
            config: Config::Internal(parsed),
            cwd: Some(dir.to_string_lossy().into_owned()),
            rt: None,
            log_file: None,
            config_path: None,
        })?;
        self.kernel = Some(Kernel {
            worker,
            cancel,
            controller: Controller {
                port: req.config.controller_port.get(),
                secret,
            },
            mixed: req.config.mixed_port.get(),
        });
        let ready = (|| -> Result<()> {
            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                let kernel = self.kernel.as_mut().unwrap();
                if kernel.worker.is_finished() {
                    bail!("clash-rs exited during startup");
                }
                match kernel.controller.request("GET", "/version", &[]) {
                    Ok((200, _)) => return Ok(()),
                    Ok((code, _)) => bail!("controller /version returned {code}"),
                    Err(e) if Instant::now() >= deadline => {
                        return Err(e.context("controller readiness deadline"))
                    }
                    Err(_) => thread::sleep(Duration::from_millis(50)),
                }
            }
        })();
        if ready.is_err() {
            self.disconnect()?;
        }
        ready
    }
    fn status(&mut self) -> Result<PlaneStatus> {
        if let Some(k) = self.kernel.as_mut() {
            if k.worker.is_finished() {
                self.disconnect()?;
                bail!("clash-rs exited unexpectedly");
            }
            return Ok(PlaneStatus {
                connected: true,
                mixed_port: k.mixed,
                controller_port: k.controller.port,
                ..Default::default()
            });
        }
        Ok(PlaneStatus::default())
    }
    fn disconnect(&mut self) -> Result<()> {
        if let Some(k) = self.kernel.take() {
            k.cancel.cancel();
            k.worker
                .join()
                .map_err(|_| anyhow!("embedded kernel panicked"))?;
        }
        Ok(())
    }
    fn probe_job(&self, nodes: Vec<Value>, url: String) -> Result<smartvpn_runtime::ProbeJob> {
        let dir = self.dir.clone();
        Ok(Box::new(move || {
            smartvpn_runtime::probe::with_probe(&dir, nodes, |c, name, _| c.delay(name, &url))
        }))
    }
    fn region(&self, nodes: Vec<Value>) -> Result<Value> {
        smartvpn_runtime::probe::with_probe(&self.dir, nodes, |_, _, port| {
            smartvpn_runtime::region(port, &self.dir)
        })
    }
    fn controller(&self) -> Result<&Controller> {
        Ok(&self
            .kernel
            .as_ref()
            .context("kernel disconnected")?
            .controller)
    }
}
fn main() -> Result<()> {
    let dir = match std::env::var_os("SMARTVPN_DATA_DIR") {
        Some(dir) => PathBuf::from(dir),
        None => std::env::current_exe()?.with_file_name("SmartVPN-data"),
    };
    let runtime = Runtime::start(
        Arc::new(Mutex::new(Windows {
            dir: dir.clone(),
            kernel: None,
        })),
        &dir,
    )?;
    println!("{}", serde_json::to_string(&runtime.bootstrap)?);
    io::stdout().flush()?;
    runtime.wait()
}
