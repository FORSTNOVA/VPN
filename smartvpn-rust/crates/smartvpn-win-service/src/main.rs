use anyhow::{anyhow, bail, ensure, Context, Result};
use clash_lib::{Config, Options};
use serde_json::{json, Value};
use smartvpn_runtime::{config::Connect, token, Controller, PlaneStatus, Runtime, TunnelDataPlane};
use std::{
    io::{self, Write},
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
    thread,
    time::{Duration, Instant},
};
// tun-rs opens wintun.dll by basename. Keep the selected module loaded until
// the kernel has stopped so Windows resolves that basename to the same image.
struct WintunModule(isize);
#[link(name = "kernel32")]
extern "system" {
    fn LoadLibraryExW(path: *const u16, file: isize, flags: u32) -> isize;
    fn GetModuleHandleW(name: *const u16) -> isize;
    fn FreeLibrary(module: isize) -> i32;
}
impl WintunModule {
    fn load(path: &Path) -> Result<Self> {
        use std::os::windows::ffi::OsStrExt;
        let path = path.canonicalize()?;
        let wide: Vec<_> = path.as_os_str().encode_wide().chain(Some(0)).collect();
        // LOAD_LIBRARY_SEARCH_SYSTEM32 limits the DLL's dependency search.
        let handle = unsafe { LoadLibraryExW(wide.as_ptr(), 0, 0x800) };
        if handle == 0 {
            return Err(io::Error::last_os_error())
                .with_context(|| format!("加载 {}", path.display()));
        }
        let module = Self(handle);
        let name: Vec<_> = "wintun.dll".encode_utf16().chain(Some(0)).collect();
        ensure!(
            unsafe { GetModuleHandleW(name.as_ptr()) } == handle,
            "已加载其他 wintun.dll，切换路径后请重启 SmartVPN"
        );
        Ok(module)
    }
}
impl Drop for WintunModule {
    fn drop(&mut self) {
        unsafe {
            FreeLibrary(self.0);
        }
    }
}

struct Kernel {
    worker: thread::JoinHandle<clash_lib::Result<()>>,
    controller: Controller,
    mixed: u16,
    _wintun: Option<WintunModule>,
}
struct Windows {
    dir: PathBuf,
    kernel: Option<Kernel>,
}
// The controller and the TUN runner start independently. Check a DNS A query
// on the SmartVPN interface before reporting a successful TUN connection.
fn check_tun_dns() -> Result<()> {
    use std::net::UdpSocket;
    let interface =
        clash_lib::app::net::get_interface_by_name("SmartVPN").context("等待 SmartVPN 网卡")?;
    let addr = interface.addr_v4.context("等待 SmartVPN 网卡 IPv4 地址")?;
    let socket = UdpSocket::bind((addr, 0))?;
    socket.set_read_timeout(Some(Duration::from_millis(250)))?;
    // Inside the TUN's directly connected subnet, intercepted locally on port 53.
    socket.connect("198.18.0.2:53")?;
    let query =
        b"SV\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x08smartvpn\x04test\x00\x00\x01\x00\x01";
    socket.send(query)?;
    let mut buf = [0; 512];
    let size = socket
        .recv(&mut buf)
        .context("SmartVPN 网卡未返回 DNS 应答")?;
    // A single-question/single-answer A response with a synthetic address.
    ensure!(
        size >= 16
            && buf[..2] == query[..2]
            && buf[2] & 0x80 != 0
            && buf[3] & 15 == 0
            && buf[6..8] == [0, 1]
            && buf[size - 6..size - 2] == [0, 4, 198, 18],
        "SmartVPN DNS 未返回 fake-ip 地址"
    );
    Ok(())
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
        let wintun = if req.config.tun {
            let dll =
                WintunModule::load(req.wintun_path.as_deref().context("缺少 wintun.dll 路径")?)?;
            config["tun"] = json!({"enable":true, "device-id":"dev://SmartVPN",
                "route-all":true, "dns-hijack":true, "mtu":65535});
            Some(dll)
        } else {
            None
        };
        let parsed = Config::Str(config.to_string())
            .try_parse_strict()
            .context("invalid kernel profile")?;
        let worker = thread::Builder::new()
            .name("clash-rs".into())
            .spawn(move || {
                clash_lib::start_scaffold(Options {
                    config: Config::Internal(parsed),
                    cwd: Some(dir.to_string_lossy().into_owned()),
                    rt: None,
                    log_file: None,
                    config_path: None,
                })
            })?;
        self.kernel = Some(Kernel {
            worker,
            controller: Controller {
                port: req.config.controller_port.get(),
                secret,
            },
            mixed: req.config.mixed_port.get(),
            _wintun: wintun,
        });
        let ready = (|| -> Result<()> {
            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                let kernel = self.kernel.as_mut().unwrap();
                if kernel.worker.is_finished() {
                    bail!("clash-rs exited during startup");
                }
                match kernel.controller.request("GET", "/version", &[]) {
                    Ok((200, _)) if req.config.tun => match check_tun_dns() {
                        Ok(()) => return Ok(()),
                        Err(err) if Instant::now() >= deadline => {
                            return Err(err.context("Windows TUN 启动失败"))
                        }
                        Err(_) => thread::sleep(Duration::from_millis(50)),
                    },
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
            // The main scaffold registers its shutdown token after building its
            // runtime. Latency probes use independent scaffold instances.
            while !clash_lib::shutdown() && !k.worker.is_finished() {
                thread::sleep(Duration::from_millis(10));
            }
            k.worker
                .join()
                .map_err(|panic| {
                    let detail = panic
                        .downcast_ref::<String>()
                        .map(String::as_str)
                        .or_else(|| panic.downcast_ref::<&str>().copied());
                    anyhow!(
                        "embedded kernel panicked: {}",
                        detail.unwrap_or("non-string panic")
                    )
                })?
                .context("clash-rs kernel failed")?;
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
