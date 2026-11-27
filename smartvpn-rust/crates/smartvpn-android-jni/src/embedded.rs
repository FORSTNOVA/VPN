use super::Bridge;
use anyhow::{anyhow, bail, ensure, Context, Result};
use clash_lib::{
    android_tun::{State, STATE},
    Config, Options,
};
use serde_json::{json, Value};
use smartvpn_runtime::{config::Profile, token, Controller, PlaneStatus, TunnelDataPlane};
use std::{
    os::fd::{AsRawFd, OwnedFd},
    sync::{Arc, Mutex},
    thread,
    time::{Duration, Instant},
};
use tokio_util::sync::CancellationToken;

// The final shared object's socket imports pass through this hook. Java's
// VpnService and its own libc calls live outside that link unit.
static PROTECT: Mutex<Option<Arc<Bridge>>> = Mutex::new(None);
extern "C" {
    fn __real_socket(domain: i32, kind: i32, protocol: i32) -> i32;
}
#[no_mangle]
unsafe extern "C" fn __wrap_socket(domain: i32, kind: i32, protocol: i32) -> i32 {
    let fd = __real_socket(domain, kind, protocol);
    if fd < 0 || !matches!(domain, libc::AF_INET | libc::AF_INET6) {
        return fd;
    }
    let bridge = PROTECT.lock().unwrap().clone();
    if let Some(bridge) = bridge {
        if let Err(e) = bridge.protect(fd) {
            eprintln!("socket protection failed: {e:#}");
            libc::close(fd);
            *libc::__errno() = libc::EACCES;
            return -1;
        }
    }
    fd
}
struct Instance {
    worker: thread::JoinHandle<()>,
    cancel: CancellationToken,
    controller: Controller,
    mixed: u16,
    members: Vec<Value>,
    policy: Value,
}
impl Instance {
    fn controller_for_probe(&self, nodes: &[Value]) -> Option<&Controller> {
        (!self.worker.is_finished() && nodes.iter().all(|node| self.members.contains(node)))
            .then_some(&self.controller)
    }
}
pub(super) struct Android {
    bridge: Arc<Bridge>,
    dir: String,
    pub(super) fd: Option<OwnedFd>,
    // Java may refresh the old numeric fd until stopSelf leads to its release report.
    pub(super) retired_fd: Option<i32>,
    pub(super) info: Option<Value>,
    instance: Option<Instance>,
}
impl Android {
    pub(super) fn new(bridge: Bridge, dir: String) -> Self {
        Self {
            bridge: Arc::new(bridge),
            dir,
            fd: None,
            retired_fd: None,
            info: None,
            instance: None,
        }
    }
    pub(super) fn release(&mut self) -> Result<()> {
        let joined = if let Some(instance) = self.instance.take() {
            instance.cancel.cancel();
            let joined = instance.worker.join();
            *PROTECT.lock().unwrap() = None;
            joined.map_err(|_| anyhow!("clash-lib thread panicked"))
        } else {
            Ok(())
        };
        self.fd = None;
        self.info = None;
        joined
    }
    pub(super) fn network_changed(&self) -> Result<()> {
        if let Some(instance) = &self.instance {
            clash_lib::network_changed();
            let (status, _) = instance.controller.request("DELETE", "/connections", &[])?;
            ensure!(status == 200, "close connections HTTP {status}");
            let (status, _) = instance
                .controller
                .request("POST", "/cache/dns/flush", &[])?;
            ensure!(status == 204, "flush resolver HTTP {status}");
        }
        Ok(())
    }
    fn check_task(&self) -> Result<bool> {
        let instance = self.instance.as_ref().context("no embedded instance")?;
        ensure!(!instance.worker.is_finished(), "clash-lib worker exited");
        match &*STATE.lock().unwrap() {
            State::Pending => Ok(false),
            State::Running => Ok(true),
            State::Failed(e) => bail!("{e}"),
        }
    }
}
impl TunnelDataPlane for Android {
    fn connect(&mut self, body: &[u8], stored: &[serde_json::Value]) -> Result<()> {
        ensure!(self.instance.is_none(), "embedded instance already started");
        let fd = self
            .fd
            .as_ref()
            .context("VpnService fd required")?
            .as_raw_fd();
        let profile: Profile = serde_json::from_slice(body).context("expected Android profile")?;
        let secret = token()?;
        profile.prepare_runtime(std::path::Path::new(&self.dir))?;
        let mut config = profile.generate(&secret)?;
        smartvpn_runtime::install_chain_members(&mut config, stored);
        config["tun"] = json!({"enable":true,"device-id":format!("fd://{fd}"),"route-all":false,"mtu":1500,"dns-hijack":true});
        let policy = json!({"dnsIPv6":config["dns"]["ipv6"],"dnsMode":config["dns"]["enhanced-mode"],"mtu":config["tun"]["mtu"],
            "dnsHijack":config["tun"]["dns-hijack"],
            "quicRules":config["rules"].as_array().unwrap().iter().filter_map(Value::as_str).filter(|r|r.starts_with("AND,((NETWORK,UDP),(DST-PORT,443)") && r.ends_with(",REJECT")).collect::<Vec<_>>()});
        // Parse before spawning, including the Android-only TUN fields.
        let mut members = config["proxies"].as_array().unwrap().clone();
        if let Some(groups) = config["proxy-groups"].as_array() {
            members.extend(groups.iter().cloned());
        }
        let config = Config::Str(config.to_string()).try_parse_strict()?;
        clash_lib::android_tun::OWNER_RESOLVER.get_or_init(|| {
            Box::new(|protocol, src, dst| {
                let bridge = PROTECT.lock().unwrap().clone()?;
                match bridge.owner_package(protocol, src, dst) {
                    Ok(package) => package,
                    Err(error) => {
                        eprintln!("resolveOwnerPackage JNI failed: {error:#}");
                        None
                    }
                }
            })
        });
        *STATE.lock().unwrap() = State::Pending;
        *PROTECT.lock().unwrap() = Some(self.bridge.clone());
        let result = clash_lib::start_scaffold_instance(Options {
            config: Config::Internal(config),
            cwd: Some(self.dir.clone()),
            rt: None,
            log_file: None,
            config_path: None,
        });
        let (worker, cancel) = match result {
            Ok(v) => v,
            Err(e) => {
                *PROTECT.lock().unwrap() = None;
                return Err(e.into());
            }
        };
        self.instance = Some(Instance {
            worker,
            cancel,
            controller: Controller {
                port: profile.controller_port.get(),
                secret,
            },
            mixed: profile.mixed_port.get(),
            policy,
            members,
        });
        let ready = (|| -> Result<()> {
            let deadline = Instant::now() + Duration::from_secs(10);
            loop {
                if self.check_task()? {
                    match self.controller()?.request("GET", "/version", &[]) {
                        Ok((200, _)) => return Ok(()),
                        Ok((code, _)) => bail!("controller readiness HTTP {code}"),
                        Err(e) if Instant::now() >= deadline => return Err(e),
                        Err(_) => {}
                    }
                }
                ensure!(
                    Instant::now() < deadline,
                    "TUN/controller startup timed out"
                );
                thread::sleep(Duration::from_millis(50));
            }
        })();
        if ready.is_err() {
            self.disconnect()?;
        }
        ready
    }
    fn disconnect(&mut self) -> Result<()> {
        let had_tunnel = self.fd.is_some();
        if let Some(fd) = &self.fd {
            self.retired_fd = Some(fd.as_raw_fd());
        }
        let result = self.release();
        if had_tunnel {
            self.bridge.drop_tunnel()?;
        }
        result
    }
    fn status(&mut self) -> Result<PlaneStatus> {
        let mut status = PlaneStatus {
            tunnel_authorized: self.fd.is_some(),
            tunnel_fd: self.fd.as_ref().map(AsRawFd::as_raw_fd),
            ..Default::default()
        };
        if let Some(i) = &self.instance {
            status.connected = self.check_task()?;
            status.controller_port = i.controller.port;
            status.mixed_port = i.mixed;
        }
        Ok(status)
    }
    fn connections(&self) -> Result<Value> {
        let mut doc = self.controller()?.json("/connections")?;
        let mut env = self.bridge.vm.attach_current_thread()?;
        for flow in doc["connections"].as_array_mut().unwrap() {
            let m = &mut flow["metadata"];
            if m["type"] == "Tun" {
                let owner = env.with_local_frame(8, |env| -> Result<Option<String>> {
                    use jni::objects::{JClass, JString, JValue};
                    let network = env.new_string(m["network"].as_str().unwrap())?;
                    let src = env.new_string(m["sourceIP"].as_str().context("missing TUN source IP")?)?;
                    let dst = env.new_string(m["tunDestinationIP"].as_str().context("missing original TUN destination")?)?;
                    let result = env.call_static_method(<&JClass>::from(self.bridge.class.as_obj()), "connectionOwner",
                        "(Ljava/lang/String;Ljava/lang/String;ILjava/lang/String;I)Ljava/lang/String;",
                        &[JValue::Object(&network), JValue::Object(&src), JValue::Int(m["sourcePort"].as_str().unwrap().parse()?),
                          JValue::Object(&dst), JValue::Int(m["destinationPort"].as_str().unwrap().parse()?)])?.l()?;
                    if result.is_null() {
                        return Ok(None); // Android no longer has an owner for the socket.
                    }
                    let name: String = env.get_string(&JString::from(result))?.into();
                    Ok(Some(name))
                })?;
                m["process"] = json!(owner);
            }
        }
        Ok(doc)
    }
    fn running_policy(&self) -> Option<&Value> {
        self.instance.as_ref().map(|i| &i.policy)
    }
    fn tunnel_interface(&self) -> Result<String> {
        let Some(fd) = self.fd.as_ref() else {
            return Ok(String::new());
        };
        // The VPN app is excluded from its own network. Ask the TUN descriptor,
        // whose interface is authoritative even before a LinkProperties callback.
        let mut iface: libc::ifreq = unsafe { std::mem::zeroed() };
        let rc = unsafe { libc::ioctl(fd.as_raw_fd(), libc::TUNGETIFF, &mut iface) };
        if rc < 0 {
            return Err(std::io::Error::last_os_error()).context("TUNGETIFF");
        }
        Ok(unsafe { std::ffi::CStr::from_ptr(iface.ifr_name.as_ptr()) }
            .to_str()?
            .to_owned())
    }
    fn probe_job(&self, nodes: Vec<Value>, url: String) -> Result<smartvpn_runtime::ProbeJob> {
        let measure = move |c: &Controller, name: &str| c.delay(name, &url);
        if let Some(c) = self
            .instance
            .as_ref()
            .and_then(|i| i.controller_for_probe(&nodes))
        {
            let c = c.clone();
            let name = nodes.last().context("empty probe chain")?["name"]
                .as_str()
                .context("missing probe target name")?
                .to_owned();
            return Ok(Box::new(move || measure(&c, &name)));
        }
        let dir = self.dir.clone();
        Ok(Box::new(move || {
            smartvpn_runtime::probe::with_probe(dir.as_ref(), nodes, |c, name, _| measure(c, name))
        }))
    }
    fn region(&self, nodes: Vec<Value>) -> Result<Value> {
        smartvpn_runtime::probe::with_probe(self.dir.as_ref(), nodes, |_, _, port| {
            smartvpn_runtime::region(port, std::path::Path::new(&self.dir))
        })
    }
    fn controller(&self) -> Result<&Controller> {
        Ok(&self
            .instance
            .as_ref()
            .context("embedded kernel disconnected")?
            .controller)
    }
}
