use crate::{token, Controller};
use anyhow::{anyhow, bail, ensure, Result};
use clash_lib::{Config, Options};
use serde_json::Value;
use std::{
    sync::Mutex,
    thread,
    time::{Duration, Instant},
};
pub fn is_udp_serialized_node(node: &Value) -> bool {
    let kind = node
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or("")
        .trim()
        .to_ascii_lowercase();
    if matches!(kind.as_str(), "hysteria2" | "hysteria" | "tuic" | "wireguard") {
        return true;
    }
    node.get("network")
        .and_then(Value::as_str)
        .unwrap_or("")
        .trim()
        .eq_ignore_ascii_case("udp")
}

pub fn with_probe<T>(
    dir: &std::path::Path,
    nodes: Vec<Value>,
    test: impl FnOnce(&Controller, &str, u16) -> Result<T>,
) -> Result<T> {
    with_probe_dns(dir, nodes, false, test)
}

pub fn with_probe_dns<T>(
    dir: &std::path::Path,
    nodes: Vec<Value>,
    protected_dns: bool,
    test: impl FnOnce(&Controller, &str, u16) -> Result<T>,
) -> Result<T> {
    let name = nodes.last().unwrap()["name"].as_str().unwrap();
    // Serialize UDP/QUIC probes (hysteria2/tuic/wireguard) so concurrent
    // handshakes do not contend on mobile/NAT UDP sockets.
    static UDP_PROBE: Mutex<()> = Mutex::new(());
    let _udp_guard = if nodes.iter().any(is_udp_serialized_node) {
        Some(UDP_PROBE.lock().unwrap_or_else(|e| e.into_inner()))
    } else {
        None
    };
    // The kernel rebinds our reserved ports during startup. Serialize that
    // hand-off between temporary instances, then measure concurrently.
    static STARTUP: Mutex<()> = Mutex::new(());
    let startup = STARTUP.lock().unwrap_or_else(|e| e.into_inner());
    let listener = std::net::TcpListener::bind(("127.0.0.1", 0))?;
    let controller = Controller {
        port: listener.local_addr()?.port(),
        secret: token()?,
    };
    let mixed = std::net::TcpListener::bind(("127.0.0.1", 0))?;
    let port = mixed.local_addr()?.port();
    let cfg = crate::config::probe_config_with_dns(
        &nodes,
        port,
        listener.local_addr()?,
        &controller.secret,
        protected_dns,
    );
    let config = Config::Str(cfg.to_string()).try_parse_strict()?;
    let work = tempfile::tempdir_in(dir)?;
    drop((listener, mixed));
    let (worker, cancel) = clash_lib::start_scaffold_instance(Options {
        config: Config::Internal(config),
        cwd: Some(work.path().to_string_lossy().into_owned()),
        rt: Some(clash_lib::TokioRuntime::SingleThread),
        log_file: None,
        config_path: None,
    })?;
    let result = (|| {
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            ensure!(!worker.is_finished(), "probe kernel exited");
            match controller.request("GET", "/version", &[]) {
                Ok((200, _)) => break,
                Ok((code, _)) => bail!("probe controller HTTP {code}"),
                Err(e) if Instant::now() >= deadline => return Err(e.context("probe startup")),
                Err(_) => thread::sleep(Duration::from_millis(30)),
            }
        }
        drop(startup);
        test(&controller, name, port)
    })();
    cancel.cancel();
    worker
        .join()
        .map_err(|_| anyhow!("probe kernel panicked"))?;
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use anyhow::Context;
    use hickory_proto::{
        op::{Message, MessageType},
        rr::{rdata::A, RData, Record},
    };
    use serde_json::json;
    use std::net::{Ipv4Addr, TcpListener, UdpSocket};

    #[test]
    fn hysteria2_server_uses_real_dns_in_fake_ip_mode() -> Result<()> {
        let dns = UdpSocket::bind("127.0.0.1:0")?;
        let server = UdpSocket::bind("127.0.0.1:0")?;
        let mixed = TcpListener::bind("127.0.0.1:0")?;
        let controller = TcpListener::bind("127.0.0.1:0")?;
        let addr = controller.local_addr()?;
        let node = json!({"name":"test","type":"hysteria2","server":"peer.example.test",
            "port":server.local_addr()?.port(),"password":"fixture","skip-cert-verify":true,
            "obfs":"salamander","obfs-password":"fixture"});
        let mut cfg =
            crate::config::probe_config(&[node], mixed.local_addr()?.port(), addr, "fixture");
        let upstream = format!("udp://{}", dns.local_addr()?);
        cfg["dns"] = json!({"enable":true,"ipv6":false,"enhanced-mode":"fake-ip",
            "fake-ip-range":"127.18.0.1/16","nameserver":[upstream],
            "default-nameserver":[upstream],"proxy-server-nameserver":[upstream]});
        let work = tempfile::tempdir()?;
        let opts = Options {
            config: Config::Str(cfg.to_string()),
            cwd: Some(work.path().to_str().unwrap().to_owned()),
            rt: Some(clash_lib::TokioRuntime::SingleThread),
            log_file: None,
            config_path: None,
        };
        drop((mixed, controller));
        let (worker, cancel) = clash_lib::start_scaffold_instance(opts)?;
        let control = Controller {
            port: addr.port(),
            secret: "fixture".into(),
        };
        let result = (|| -> Result<()> {
            let start = Instant::now();
            while control.request("GET", "/version", &[]).is_err() {
                ensure!(
                    !worker.is_finished() && start.elapsed() < Duration::from_secs(5),
                    "kernel startup failed"
                );
                thread::sleep(Duration::from_millis(20));
            }
            dns.set_read_timeout(Some(Duration::from_secs(3)))?;
            server.set_read_timeout(Some(Duration::from_secs(3)))?;
            thread::scope(|scope| -> Result<()> {
                // No QUIC responder is needed. The initial datagram proves where the client dialed.
                let request = scope.spawn(|| {
                    control.request(
                        "GET",
                        "/proxies/test/delay?url=http%3A%2F%2F127.0.0.1%2F&timeout=2000",
                        &[],
                    )
                });
                let mut buf = [0; 4096];
                let (len, peer) = dns
                    .recv_from(&mut buf)
                    .context("no real DNS query for Hysteria2 server")?;
                let query = Message::from_vec(&buf[..len])?;
                let name = query.queries[0].name().clone();
                ensure!(
                    name.to_ascii() == "peer.example.test.",
                    "unexpected DNS question"
                );
                let mut response = query;
                response.metadata.message_type = MessageType::Response;
                response.answers.push(Record::from_rdata(
                    name,
                    30,
                    RData::A(A(Ipv4Addr::LOCALHOST)),
                ));
                dns.send_to(&response.to_vec()?, peer)?;
                ensure!(server.recv(&mut buf)? > 0, "empty client datagram");
                let (status, _) = request.join().unwrap()?;
                ensure!(status == 400, "expected timeout from test endpoint");
                Ok(())
            })
        })();
        cancel.cancel();
        worker.join().unwrap();
        result
    }

    #[test]
    fn udp_serialized_node_matches_quic_and_udp_protocols() {
        assert!(is_udp_serialized_node(&json!({"type": "hysteria2"})));
        assert!(is_udp_serialized_node(&json!({"type": "Hysteria2"})));
        assert!(is_udp_serialized_node(&json!({"type": "tuic"})));
        assert!(is_udp_serialized_node(&json!({"type": "wireguard"})));
        assert!(is_udp_serialized_node(&json!({"type": "vless", "network": "udp"})));
        assert!(!is_udp_serialized_node(&json!({"type": "anytls"})));
        assert!(!is_udp_serialized_node(&json!({"type": "vless", "network": "ws"})));
    }
}
