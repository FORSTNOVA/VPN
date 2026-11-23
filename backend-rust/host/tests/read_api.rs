use smartvpn_host::monitor::embedded::{smartvpn_host_stop_v1, start_reads};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::time::Duration;

#[test]
fn consecutive_reads_reuse_the_same_adapter_connection() {
    let adapter = TcpListener::bind("127.0.0.1:0").unwrap();
    let (session, port) =
        start_reads(adapter.local_addr().unwrap().port(), "private", "client").unwrap();
    let worker = std::thread::spawn(move || {
        let (sock, _) = adapter.accept().unwrap();
        sock.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        let mut upstream = BufReader::new(sock);
        for expected in ["GET /1 HTTP/1.1", "GET /2 HTTP/1.1"] {
            let mut line = String::new();
            upstream.read_line(&mut line).unwrap();
            assert_eq!(line.trim_end(), expected);
            loop {
                line.clear();
                assert!(upstream.read_line(&mut line).unwrap() > 0);
                if line == "\r\n" {
                    break;
                }
            }
            upstream
                .get_mut()
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
                .unwrap();
        }
    });
    let sock = TcpStream::connect(("127.0.0.1", port)).unwrap();
    sock.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
    let mut downstream = BufReader::new(sock);
    for path in ["identify", "state"] {
        write!(
            downstream.get_mut(),
            "GET /api/{path} HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client\r\n\r\n"
        )
        .unwrap();
        let mut status = String::new();
        downstream.read_line(&mut status).unwrap();
        assert!(status.starts_with("HTTP/1.1 200"));
        loop {
            let mut header = String::new();
            assert!(downstream.read_line(&mut header).unwrap() > 0);
            if header == "\r\n" {
                break;
            }
        }
        let mut body = [0; 2];
        downstream.read_exact(&mut body).unwrap();
        assert_eq!(&body, b"{}");
    }
    worker.join().unwrap();
    unsafe { smartvpn_host_stop_v1(Box::into_raw(Box::new(session))) };
}

#[test]
fn resource_calls_are_authenticated_read_only_and_preserve_query() {
    let adapter = TcpListener::bind("127.0.0.1:0").unwrap();
    let (session, port) = start_reads(
        adapter.local_addr().unwrap().port(),
        "private-effect-key",
        "client-key",
    )
    .unwrap();
    let worker = std::thread::spawn(move || {
        let (mut sock, _) = adapter.accept().unwrap();
        let mut reader = BufReader::new(&sock);
        let mut headers = Vec::new();
        loop {
            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            if line == "\r\n" || line.is_empty() {
                break;
            }
            headers.push(line.trim_end().to_owned());
        }
        assert_eq!(headers[0], "GET /24?app=a%26b&limit=3 HTTP/1.1");
        assert!(headers
            .iter()
            .any(|s| s.eq_ignore_ascii_case("authorization: Bearer private-effect-key")));
        assert!(headers.iter().all(|s| !s.contains("client-key")));
        sock.write_all(
            b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nContent-Type: application/json\r\n\r\n{}",
        )
        .unwrap();
    });
    let call = |verb: &str, path: &str, token: &str| {
        let mut socket = TcpStream::connect(("127.0.0.1", port)).unwrap();
        socket
            .set_read_timeout(Some(Duration::from_secs(5)))
            .unwrap();
        write!(socket,"{verb} {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nAuthorization: Bearer {token}\r\n\r\n").unwrap();
        let mut body = String::new();
        socket.read_to_string(&mut body).unwrap();
        body
    };
    assert!(call("GET", "/api/state", "bad").starts_with("HTTP/1.1 401"));
    assert!(call("POST", "/api/disconnect", "client-key").starts_with("HTTP/1.1 405"));
    assert!(call("GET", "/api/missing", "client-key").starts_with("HTTP/1.1 404"));
    let ok = call(
        "GET",
        "/api/traffic/history?app=a%26b&limit=3",
        "client-key",
    );
    assert!(ok.starts_with("HTTP/1.1 200") && ok.ends_with("{}"));
    worker.join().unwrap();
    unsafe { smartvpn_host_stop_v1(Box::into_raw(Box::new(session))) };
    assert!(TcpStream::connect(("127.0.0.1", port)).is_err());
}

#[test]
fn writes_wait_for_the_complete_body_and_never_replay_after_dispatch() {
    use std::net::Shutdown;
    let adapter = TcpListener::bind("127.0.0.1:0").unwrap();
    let (session, port) = smartvpn_host::monitor::embedded::start_api(
        adapter.local_addr().unwrap().port(),
        "effect-key",
        "client-key",
        true,
    )
    .unwrap();
    let write = |payload: &[u8]| {
        let mut sock = TcpStream::connect(("127.0.0.1", port)).unwrap();
        sock.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
        sock.write_all(payload).unwrap();
        sock
    };
    let partial=write(b"PUT /api/settings HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client-key\r\nContent-Length: 200\r\n\r\n{}");
    partial.shutdown(Shutdown::Both).unwrap();
    let worker = std::thread::spawn(move || {
        let (sock, _) = adapter.accept().unwrap();
        let mut reader = BufReader::new(sock);
        let mut first = String::new();
        reader.read_line(&mut first).unwrap();
        assert_eq!(first.trim(), "POST /30?name=a%26b HTTP/1.1");
        let mut line = String::new();
        loop {
            line.clear();
            reader.read_line(&mut line).unwrap();
            if line == "\r\n" {
                break;
            }
        }
        let mut body = [0; 7];
        reader.read_exact(&mut body).unwrap();
        assert_eq!(&body, br#"{"x":1}"#);
        // Command reached the effect owner; lose its acknowledgement.
        drop(reader);
        adapter
    });
    let mut sock=write(b"POST /api/nodes/manual?name=a%26b HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client-key\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n3\r\n{\"x\r\n4\r\n\":1}\r\n0\r\n\r\n");
    let mut reply = String::new();
    sock.read_to_string(&mut reply).unwrap();
    assert!(reply.starts_with("HTTP/1.1 502"));
    let adapter = worker.join().unwrap();
    unsafe { smartvpn_host_stop_v1(Box::into_raw(Box::new(session))) };
    adapter.set_nonblocking(true).unwrap();
    assert_eq!(
        adapter.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
}

#[test]
fn clash_rs_controller_member_delays_and_connection_metadata() {
    use smartvpn_host::monitor::embedded::smartvpn_host_start_v1;
    use std::ffi::CString;
    let controller = TcpListener::bind("127.0.0.1:0").unwrap();
    let controller_port = controller.local_addr().unwrap().port();
    let worker = std::thread::spawn(move || {
        let mut paths = Vec::new();
        for _ in 0..5 {
            let (mut c, _) = controller.accept().unwrap();
            let mut line = String::new();
            let mut reader = BufReader::new(&c);
            reader.read_line(&mut line).unwrap();
            let path = line.split_whitespace().nth(1).unwrap().to_owned();
            let mut headers = String::new();
            loop {
                line.clear();
                assert!(reader.read_line(&mut line).unwrap() > 0);
                if line == "\r\n" {
                    break;
                }
                headers.push_str(&line);
            }
            assert!(headers
                .to_ascii_lowercase()
                .contains("authorization: bearer key"));
            let route = path.split('?').next().unwrap();
            let body = match route {
                "/group/SmartVPN/delay" => r#"{"SmartVPN":63}"#,
                "/proxies/SmartVPN" => {
                    r#"{"all":["REJECT","DIRECT","SmartVPNAuto","SmartVPNFallback","A","B /"]}"#
                }
                "/proxies/A/delay" => r#"{"delay":17}"#,
                "/proxies/B%20%2F/delay" => r#"{"delay":23}"#,
                "/connections" => {
                    r#"{"connections":[{"metadata":{"destinationIP":"127.0.0.2","destinationPort":8042,"network":"Tcp"}},{"metadata":{"destinationPort":9000,"network":"Udp"}}]}"#
                }
                p => panic!("unexpected request {p}"),
            };
            if route.ends_with("/delay") {
                assert!(path
                    .ends_with("?timeout=6000&url=https%3A%2F%2Fwww.youtube.com%2Fgenerate_204"))
            }
            paths.push(path);
            write!(
                c,
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            )
            .unwrap();
        }
        paths
    });
    let key = CString::new("key").unwrap();
    let token = CString::new("client").unwrap();
    let mut port = 0;
    let session =
        unsafe { smartvpn_host_start_v1(controller_port, key.as_ptr(), token.as_ptr(), &mut port) };
    assert!(!session.is_null());
    let call = |route: &str| {
        let mut socket = TcpStream::connect(("127.0.0.1", port)).unwrap();
        socket
            .set_read_timeout(Some(Duration::from_secs(5)))
            .unwrap();
        write!(socket,"GET /api/{route} HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer client\r\nConnection: close\r\n\r\n").unwrap();
        let mut reply = String::new();
        socket.read_to_string(&mut reply).unwrap();
        assert!(reply.starts_with("HTTP/1.1 200"), "{reply}");
        serde_json::from_str::<serde_json::Value>(reply.split_once("\r\n\r\n").unwrap().1).unwrap()
    };
    assert_eq!(
        call("nodes/latency")["latency"],
        serde_json::json!({"A":17,"B /":23})
    );
    let traffic = call("traffic");
    let rows = traffic["connections"].as_array().unwrap();
    assert!(rows
        .iter()
        .any(|c| c["network"] == "tcp" && c["target"] == "127.0.0.2:8042"));
    assert!(rows.iter().any(|c| c["network"] == "udp"));
    unsafe { smartvpn_host_stop_v1(session) };
    worker.join().unwrap();
}
