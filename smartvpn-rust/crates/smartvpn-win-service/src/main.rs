mod dns_guard;
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
    members: Vec<Value>,
    mixed: u16,
    _wintun: Option<WintunModule>,
    dns_guard: Option<dns_guard::DnsGuard>,
}
impl Kernel {
    fn controller_for_probe(&self, nodes: &[Value]) -> Option<&Controller> {
        (!self.worker.is_finished() && nodes.iter().all(|node| self.members.contains(node)))
            .then_some(&self.controller)
    }
}

struct Windows {
    dir: PathBuf,
    kernel: Option<Kernel>,
}
// The controller and the TUN runner start independently. Check a DNS A query
// on the SmartVPN interface before reporting a successful TUN connection.
fn check_tun_dns() -> Result<u32> {
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
    Ok(interface.index)
}

impl TunnelDataPlane for Windows {
    fn connect(&mut self, body: &[u8], stored: &[serde_json::Value]) -> Result<()> {
        ensure!(!self.status()?.connected, "kernel already running");
        let req: Connect = serde_json::from_slice(body).context("expected directory and config")?;
        let dir = req.directory.canonicalize()?;
        let secret = token()?;
        req.config.prepare_runtime(&dir)?;
        let mut config = req.config.generate(&secret)?;
        smartvpn_runtime::install_chain_members(&mut config, stored);
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
            members: config["proxies"]
                .as_array()
                .unwrap()
                .iter()
                .chain(config["proxy-groups"].as_array().unwrap())
                .cloned()
                .collect(),
            _wintun: wintun,
            dns_guard: None,
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
                        Ok(index) => {
                            if req.config.prevent_dns_leaks {
                                kernel.dns_guard = Some(dns_guard::DnsGuard::install(index)?);
                            }
                            return Ok(());
                        }
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
            // runtime.
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
        if let Some(controller) = self
            .kernel
            .as_ref()
            .and_then(|k| k.controller_for_probe(&nodes))
        {
            let controller = controller.clone();
            let exit = nodes.last().unwrap()["name"].as_str().unwrap().to_owned();
            return Ok(Box::new(move || controller.delay(&exit, &url)));
        }
        let dir = self.dir.clone();
        // Temporary instances share clash-lib's pre-TUN outbound interface.
        // WFP blocks physical DNS on ports 53 and 853 while the guard is installed.
        let protected_dns = self.kernel.as_ref().is_some_and(|k| k.dns_guard.is_some());
        Ok(Box::new(move || {
            smartvpn_runtime::probe::with_probe_dns(&dir, nodes, protected_dns, |c, name, _| {
                c.delay(name, &url)
            })
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn running_controller_requires_every_compiled_member_to_match() {
        let entry = json!({"name":"entry","type":"socks5","server":"host","port":1080});
        let exit = json!({"name":"exit","type":"socks5","server":"other","port":1080,"dialer-proxy":"entry"});
        let backup = json!({"name":"backup","type":"socks5","server":"third","port":1080,"dialer-proxy":"entry"});
        let group = json!({"name":"policy","type":"fallback","proxies":["exit","backup"]});
        let listener = std::net::TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        let (stop, wait) = std::sync::mpsc::channel();
        let worker = thread::spawn(move || {
            use std::io::{BufRead, BufReader};
            for name in ["entry", "exit", "policy"] {
                let (mut socket, _) = listener.accept().unwrap();
                let mut headers = BufReader::new(&socket).lines().map(io::Result::unwrap);
                assert!(headers
                    .next()
                    .unwrap()
                    .starts_with(&format!("GET /proxies/{name}/delay?")));
                assert!(headers.any(|line| line.is_empty()));
                let body = r#"{"overall":23,"delay":17}"#;
                write!(
                    socket,
                    "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                )
                .unwrap();
            }
            wait.recv().unwrap();
            Ok(())
        });
        let plane = Windows {
            dir: PathBuf::new(),
            kernel: Some(Kernel {
                worker,
                controller: Controller {
                    port,
                    secret: "fixture".into(),
                },
                members: vec![entry.clone(), exit.clone(), backup.clone(), group.clone()],
                mixed: 12346,
                _wintun: None,
                dns_guard: None,
            }),
        };
        for nodes in [
            vec![entry.clone()],
            vec![entry.clone(), exit.clone()],
            vec![entry.clone(), exit.clone(), backup.clone(), group.clone()],
        ] {
            let job = plane
                .probe_job(nodes, "https://example.test/".into())
                .unwrap();
            assert_eq!(job().unwrap(), (23, 17));
        }
        let kernel = plane.kernel.unwrap();
        let mut standalone = exit.clone();
        standalone.as_object_mut().unwrap().remove("dialer-proxy");
        assert!(kernel.controller_for_probe(&[standalone]).is_none());
        let mut edited = entry.clone();
        edited["port"] = json!(1081);
        assert!(kernel
            .controller_for_probe(&[edited, exit.clone()])
            .is_none());
        let mut changed_policy = group;
        changed_policy["proxies"] = json!(["backup", "exit"]);
        assert!(kernel
            .controller_for_probe(&[entry.clone(), exit, backup, changed_policy])
            .is_none());

        stop.send(()).unwrap();
        while !kernel.worker.is_finished() {
            thread::yield_now();
        }
        assert!(kernel.controller_for_probe(&[entry]).is_none());
        kernel.worker.join().unwrap().unwrap();
    }
}
