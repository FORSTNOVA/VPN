"""Run on Windows after build-windows.ps1. Only uses loopback and fixture PIDs."""
import argparse
import base64
import select
import http.client
import json
import os
import sqlite3
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlencode
import shutil
import socket
import socketserver
import struct
import subprocess
import tempfile
import time
from contextlib import closing, ExitStack
from pathlib import Path
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parents[1]
VERSION = (ROOT / "VERSION").read_text().strip().split("+")[0]
SCRATCH = Path(r"C:\Users\21654\.gemini\antigravity\brain\6a453053-a315-41e2-b122-010410e5b9fd\scratch")


def pids(path):
    script = ("@(Get-Process -Name '" + path.stem + "' -ErrorAction SilentlyContinue | "
              "Where-Object Path -eq '" + str(path) + "' | ForEach-Object Id) -join ','")
    result = subprocess.check_output(["powershell", "-NoProfile", "-Command", script], text=True).strip()
    return {int(pid) for pid in result.split(",")} if result else set()


def listener():
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    sock.listen()
    sock.settimeout(6)
    return sock


def main(archive):

    with tempfile.TemporaryDirectory(dir=os.environ.get("SMARTVPN_TEST_TMP", SCRATCH)) as temp, ExitStack() as stack:
        bundle = Path(temp, "bundle")
        bundle.mkdir()
        service = bundle / "smartvpn-service-current.exe"
        kernel = bundle / "clash-rs.exe"
        if archive:
            with ZipFile(archive) as zip_file:
                assert "clash-rs.exe" not in zip_file.namelist()
                zip_file.extract(service.name, bundle)
        else:
            shutil.copyfile(ROOT / "dist/windows-x64/smartvpn-rust-service.exe", service)
        class ProbeHandler(BaseHTTPRequestHandler):
            def do_GET(self):
                time.sleep(0.025)
                payload = {"/trace":b"loc=US\n","/country":b"US"}.get(self.path, b"")
                self.send_response(200 if payload else 204)
                self.send_header("Content-Length",str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
            do_HEAD = do_GET
            def log_message(self, fmt, *args):
                pass
        probe = ThreadingHTTPServer(("127.0.0.1", 0), ProbeHandler)
        threading.Thread(target=probe.serve_forever, daemon=True).start()
        stack.callback(probe.server_close)
        stack.callback(probe.shutdown)
        Path(temp, "latency-target.txt").write_text(f"http://127.0.0.1:{probe.server_port}/check")
        Path(temp,"region-trace.txt").write_text(f"http://127.0.0.1:{probe.server_port}/trace")
        Path(temp,"region-country.txt").write_text(f"http://127.0.0.1:{probe.server_port}/country")
        origin = stack.enter_context(listener())
        origin_port = origin.getsockname()[1]
        log = stack.enter_context(open(Path(temp, "kernel.log"), "w+b"))
        child_env = {k:v for k,v in os.environ.items() if not k.lower().endswith("_proxy")}
        child_env["SMARTVPN_DATA_DIR"] = temp
        def launch():
            return subprocess.Popen([service], cwd=temp, stdout=subprocess.PIPE, stderr=log,
                                    env=child_env)
        proc = launch()
        def cleanup():
            proc.kill()
            proc.wait(timeout=8)
            proc.stdout.close()
        stack.callback(cleanup)
        bootstrap = json.loads(proc.stdout.readline())
        port = bootstrap["port"]
        assert bootstrap["address"] == f"127.0.0.1:{port}"

        def api(method, route, expected, data=None, auth=True):
            conn = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
            try:
                headers = {"Authorization": "Bearer " + bootstrap["token"]} if auth else {}
                conn.request(method, "/api/" + route, json.dumps(data) if data is not None else None, headers)
                response = conn.getresponse()
                raw = response.read()
                assert response.status == expected, (route.split("?")[0], response.status, expected)
                return json.loads(raw) if raw else None
            finally:
                conn.close()

        assert api("GET", "identify", 200, auth=False) == {
            "app":"SmartVPN","edition":"rust-pure","version":VERSION,"kernel":"clash-rs","running":False}
        api("GET", "status", 401, auth=False)
        assert not api("GET", "status", 200)["connected"]
        state = api("GET","state",200)
        assert state["dataDir"] == temp and state["settings"] == api("GET","settings",200)
        for route in ("settings","state","nodes","subscriptions","nodes/latency","traffic"):
            api("GET",route,401,auth=False)
        assert api("POST","settings",200,{"socksPort":18481})["socksPort"] == 18481
        class Proxy(socketserver.StreamRequestHandler):
            def handle(self):
                version, methods = self.rfile.read(2)
                assert version == 5 and 0 in self.rfile.read(methods)
                self.wfile.write(bytes([5, 0]))
                version, command, _, address_type = self.rfile.read(4)
                assert (version, command) == (5, 1)
                if address_type == 1:
                    host = socket.inet_ntoa(self.rfile.read(4))
                else:
                    assert address_type == 3
                    host = self.rfile.read(self.rfile.read(1)[0]).decode()
                dest, = struct.unpack("!H", self.rfile.read(2))
                assert (host, dest) == ("127.0.0.1", probe.server_port)
                with socket.create_connection((host, dest), timeout=5) as remote:
                    self.wfile.write(bytes([5, 0, 0, 1, 127, 0, 0, 1, 0, 0]))
                    while True:
                        line = self.rfile.readline()
                        assert line
                        remote.sendall(line)
                        if line == b"\r\n":
                            break
                    while chunk := remote.recv(4096):
                        self.wfile.write(chunk)
        proxy = stack.enter_context(socketserver.ThreadingTCPServer(("127.0.0.1", 0), Proxy))
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        stack.callback(proxy.shutdown)
        name = "Loop + socks"
        add = {"uri":f"socks5://127.0.0.1:{proxy.server_address[1]}", "name":name}
        api("POST","nodes/manual",200,{**add,"preview":True})
        assert not api("GET","nodes",200)["nodes"]
        api("POST","nodes/manual",200,add)
        api("PUT","nodes/select",200,{"name":name})
        verified=api("POST","regions/verify",200,{"refresh":True})
        assert verified["job"]["verified"] == 1 and verified["nodes"][0]["country"] == "US"
        locked=api("POST","region/lock",200,{"country":"US"})
        assert locked["lockedRegion"]=="US" and locked["pool"]==[name]
        api("POST","region/lock",200,{"country":""})
        secret = "subscription-test-credential"
        sub = api("POST","subscriptions/add",200,{"name":"fixture","url":"https://localhost/"+secret,"enabled":False})
        api("POST","shutdown",200)
        assert proc.wait(timeout=6) == 0
        proc.stdout.close()
        proc = launch()
        bootstrap = json.loads(proc.stdout.readline())
        port = bootstrap["port"]
        state = api("GET","state",200)
        assert state["socksPort"] == 18481 and state["selectedNode"] == name
        assert api("GET","subscriptions",200)["subscriptions"][0]["enabled"] is False
        api("DELETE","subscriptions?id="+sub["id"],200)
        api("PUT","settings",200,{"socksPort":0})
        # Flutter's ordinary empty-body connect resolves the packaged kernel and saved node.
        api("POST","connect",200)
        assert api("GET","proxies",200)["proxies"]["SmartVPN"]["now"] == name
        query = urlencode({"url":f"http://127.0.0.1:{probe.server_port}/check","timeout":3000})
        timing = api("GET","latency?"+query,200)
        print(f"SOCKS fixture HTTP {timing['delay']} ms, overall {timing['overall']} ms")
        assert timing["overall"] >= timing["delay"] >= 25
        [member] = api("GET","nodes/latency?"+query,200)["results"]
        assert member["name"] == name and member["latencyMs"] >= 0, member
        api("POST","disconnect",200)
        api("DELETE","nodes/manual?"+urlencode({"name":name}),200)
        assert api("GET","nodes",200)["nodes"] == []
        # Each relay only accepts CONNECT for its immediate, expected loopback peer.
        observed = []
        def relay(expected_port):
            class Hop(socketserver.StreamRequestHandler):
                def handle(self):
                    version, methods = self.rfile.read(2)
                    assert version == 5 and 0 in self.rfile.read(methods)
                    self.wfile.write(bytes([5, 0]))
                    version, command, _, kind = self.rfile.read(4)
                    assert (version, command) == (5, 1)
                    if kind == 1:
                        host = socket.inet_ntoa(self.rfile.read(4))
                    else:
                        assert kind == 3
                        host = self.rfile.read(self.rfile.read(1)[0]).decode()
                    port, = struct.unpack("!H", self.rfile.read(2))
                    if expected_port == origin_port and (host,port) == ("223.5.5.5",18081):
                        self.wfile.write(bytes([5,0,0,1,127,0,0,1,0,0]))
                        assert self.rfile.readline() == b"geo fixture\n"
                        self.wfile.write(b"CN rule selected\n")
                        return
                    is_probe = expected_port == origin_port and (host,port) == ("127.0.0.1",probe.server_port)
                    assert is_probe or (host, port) == (("origin.example" if expected_port == origin_port else "127.0.0.1"), expected_port)
                    observed.append((self.server.server_address[1],port))
                    with socket.create_connection(("127.0.0.1", port),timeout=5) as peer:
                        self.wfile.write(bytes([5,0,0,1,127,0,0,1,0,0]))
                        while True:
                            ready,_,_ = select.select([self.connection,peer],[],[],10)
                            assert ready, "idle fixture relay"
                            for stream in ready:
                                chunk = stream.recv(8192)
                                if not chunk:
                                    return
                                (peer if stream is self.connection else self.connection).sendall(chunk)
            server = stack.enter_context(socketserver.ThreadingTCPServer(("127.0.0.1",0),Hop))
            threading.Thread(target=server.serve_forever,daemon=True).start()
            stack.callback(server.shutdown)
            return server.server_address[1]
        exit_port = relay(origin_port)
        middle_port = relay(exit_port)
        entry_port = relay(middle_port)
        mode = "valid"
        refreshing, release_download = threading.Event(), threading.Event()
        class Subscription(BaseHTTPRequestHandler):
            def do_GET(self):
                path,query = self.path.split("?")
                fields = parse_qs(query, strict_parsing=True)
                assert fields.pop("token") == [secret]
                assert fields in ({}, {"cla":["0"]}, {"cla":["1"]}), fields
                if path == "/entry" and mode == "empty":
                    self.send_response(200)
                    self.end_headers()
                    return
                if path == "/entry" and mode == "error":
                    self.send_response(503)
                    self.end_headers()
                    return
                self.send_response(200)
                quota = "upload=1; download=2; total=300; expire=1900000000"
                if mode == "scheduled":
                    if path == "/entry":
                        refreshing.set()
                        assert release_download.wait(5)
                    quota = "upload=5; download=6; total=600"
                if mode == "invalid quota":
                    quota = "upload=1; download=2; total=invalid"
                self.send_header("Subscription-Userinfo",quota)
                self.end_headers()
                if path == "/entry":
                    content = "proxies:\n  - {name: 剩余流量, type: socks5, server: localhost, port: 9}\n"
                    if mode in ("valid","scheduled"):
                        content += f"  - {{name: relay, type: socks5, server: 127.0.0.1, port: {entry_port}}}\n"
                    content += ("rules:\n  - DOMAIN,example.test,GROUP\n"
                                "  - DOMAIN-SUFFIX,blocked.test,REJECT\n"
                                "  - DOMAIN-KEYWORD,chat,GROUP\n  - GEOIP,CN,GROUP,no-resolve\n  - IP-CIDR,223.5.5.5/32,REJECT,no-resolve\n"
                                "  - IP-CIDR6,2001:db8::/32,DIRECT\n"
                                "  - IP-CIDR,192.0.2.0/24,DIRECT,no-resolve\n")
                    self.wfile.write(content.encode())
                else:
                    assert path == "/exit"
                    label = "new" if mode == "rename exit" else "relay"
                    self.wfile.write(base64.b64encode((f"socks5://127.0.0.1:{exit_port}#{label}\n"*2).encode()))
            def log_message(self,*args):
                pass
        subsrv = stack.enter_context(ThreadingHTTPServer(("127.0.0.1",0),Subscription))
        threading.Thread(target=subsrv.serve_forever,daemon=True).start()
        stack.callback(subsrv.shutdown)
        def add_sub(label):
            url = f"http://127.0.0.1:{subsrv.server_port}/{label}?token={secret}"
            return api("POST","subscriptions/add",200,{"label":label,"url":url,"prefix":label+"-"})["id"]
        entry,exit_id = add_sub("entry"),add_sub("exit")
        mode = "error"
        assert api("POST","subscriptions/fetch",400,{"id":entry}) == {"error":"upstream HTTP 503"}
        mode = "valid"
        for route in ["subscriptions/fetch","nodes/fetch","subscriptions/refresh","subscriptions/activate"]:
            response = api("POST",route,200,{"id":entry})
            assert (len(response["nodes"]) if route=="nodes/fetch" else response["nodes"]) == 1 and not response["fromCache"]
        provider = Path(temp,"providers/subscription.yaml")
        cached = provider.read_bytes()
        for mode in ["announcement","empty"]:
            retained=api("POST","subscriptions/fetch",200,{"id":entry})
            assert retained["fromCache"] and retained["warning"]
            assert provider.read_bytes() == cached
        mode = "error"
        result = api("POST","subscriptions/merge",200)
        assert result["nodes"] == 2 and result["warnings"] == [{"id":entry,"error":"upstream HTTP 503"}],result
        assert api("GET","nodes",200)["nodes"][0]["manual"] is False
        hops = ["entry-relay","exit-relay"]
        assert [n["name"] for n in json.loads(provider.read_bytes())["proxies"]] == hops
        for invalid in [hops[:1],hops[:1]*2,[hops[0],"absent"],hops*4+hops[:1]]:
            api("POST","nodes/chain",400,{"hops":invalid})
        extra = ["fixture-"+str(i) for i in range(6)]
        for label in extra:
            api("POST","nodes/manual",200,{"name":label,"uri":f"socks5://127.0.0.1:{entry_port}"})
        assert api("POST","nodes/chain",200,{"hops":hops+extra})["hops"] == hops+extra
        api("POST","connect",200)
        assert api("GET","state",200)["selectedNode"] == extra[-1]
        api("PUT","nodes/select",409,{"name":name})
        api("PUT","proxies/SmartVPN",409,{"name":name})
        assert api("GET","proxies",200)["proxies"]["SmartVPN"]["now"] == extra[-1]
        api("POST","disconnect",200)
        api("POST","nodes/chain",200,{"enabled":False,"hops":[]})
        for label in extra:
            api("DELETE","nodes/manual?"+urlencode({"name":label}),200)
        api("PUT","nodes/chain/profile",200,{"name":"loop","original":"","hops":hops})
        assert api("GET","state",200)["chains"] == {"loop":hops}
        api("POST","nodes/chain",200,{"name":"loop","hops":hops})
        assert api("GET","nodes/chain",200)["hops"] == hops
        mode = "valid"
        mode = "rename exit"
        api("POST","subscriptions/refresh",200,{"id":exit_id})
        assert [n["name"] for n in json.loads(provider.read_bytes())["proxies"]] == ["entry-relay","exit-new"]
        mode = "valid"
        refresh=api("POST","subscriptions/refresh",200,{"id":exit_id})
        assert refresh["subscription"]["nodeCount"] == 1
        assert [n["name"] for n in json.loads(provider.read_bytes())["proxies"]] == hops
        api("POST","nodes/manual",200,{"name":"middle","uri":f"socks5://127.0.0.1:{middle_port}"})
        hops.insert(1,"middle")
        api("POST","nodes/chain",200,{"hops":hops})
        report = api("POST","nodes/chain/probe",200,{"hops":hops})
        assert report["delay"] >= 0 and report["warmDelay"] >= 0
        assert len(observed) == 3
        observed.clear()
        assert api("GET","regions",200)["job"]["total"] == 3
        assert api("POST","tun/check",200)["mode"] == "external"
        # Keep domestic refresh local throughout the policy matrix.
        Path(temp,"providers/china-ip.txt").write_text("192.0.2.0/24\n")
        for domestic,dns,rtc in [(False,False,False),(True,False,False),(True,True,True),(False,True,False)]:
            api("PUT","settings",200,{"importPolicy":True,"domesticWeb":domestic,
                "dnsProtection":dns,"webRTCProtection":rtc})
            api("POST","connect",200)
            # Connect strictly parses the actual policy and constructs the GEOIP router.
            assert api("GET","status",200)["connected"]
            if domestic and dns:
                # An IP-specific REJECT immediately follows GEOIP,CN in the imported policy.
                # Only the CN match can deliver this payload to our loopback exit fixture.
                mixed = api("GET","status",200)["mixedPort"]
                with socket.create_connection(("127.0.0.1",mixed),timeout=5) as client, client.makefile("rb") as read:
                    client.sendall(b"CONNECT 223.5.5.5:18081 HTTP/1.1\r\nHost: 223.5.5.5:18081\r\n\r\n")
                    assert b" 200 " in read.readline()
                    while (header := read.readline()) != b"\r\n":
                        assert header
                    client.sendall(b"geo fixture\n")
                    assert read.readline()==b"CN rule selected\n"
                observed.clear()
            assert Path(temp,"Country.mmdb").read_bytes() == (ROOT/"vendor/Country.mmdb").read_bytes()
            api("POST","disconnect",200)
        Path(temp,"providers/china-ip.txt").unlink()
        api("PUT","settings",200,{"importPolicy":False,"domesticWeb":False,"dnsProtection":False,"webRTCProtection":False})
        api("POST","connect",200)
        assert api("GET","proxies",200)["proxies"]["SmartVPN"]["all"] == [hops[-1]]
        quota_rows = api("GET","subscriptions",200)["subscriptions"]
        assert all(row["userInfo"] == dict(upload=1,download=2,total=300,expire=1900000000) for row in quota_rows)
        active = provider.read_bytes()
        info = api("POST","subscriptions/quota",200,{"id":exit_id})["userInfo"]
        assert info == dict(upload=1,download=2,total=300,expire=1900000000)
        mode = "invalid quota"
        failure = api("POST","subscriptions/quota",400,{"id":exit_id})
        assert secret not in failure["error"]
        mode = "valid"
        listed = api("GET","subscriptions",200)["subscriptions"]
        assert listed[0]["userInfo"] == info and listed[1]["userInfo"] == info
        assert active == provider.read_bytes()
        mode = "scheduled"
        controller = api("GET","status",200)["controllerPort"]
        api("POST","subscriptions/auto-merge",200,{"hours":1})
        with closing(sqlite3.connect(Path(temp,"smartvpn.db"))) as db:
            db.execute("UPDATE preferences SET doc='0' WHERE key='mergeAt'")
            db.commit()
        try:
            assert refreshing.wait(5)
            start=time.monotonic()
            status=api("GET","status",200)
            assert status["connected"] and status["controllerPort"]==controller
            assert time.monotonic()-start < 1
        finally:
            release_download.set()
        until=time.monotonic()+6
        while not all(s["userInfo"]["total"]==600 for s in api("GET","subscriptions",200)["subscriptions"]):
            assert time.monotonic()<until
            time.sleep(.03)
        assert api("GET","status",200)["controllerPort"]==controller
        api("POST","subscriptions/auto-merge",200,{"hours":0})
        mode="valid"
        api("POST","nodes/chain",409,{"hops":hops})
        api("POST","subscriptions/activate",409,{"id":entry})
        mixed = api("GET","status",200)["mixedPort"]
        with socket.create_connection(("127.0.0.1",mixed),timeout=6) as client:
            client.sendall(f"CONNECT origin.example:{origin_port} HTTP/1.1\r\nHost: origin.example:{origin_port}\r\n\r\n".encode())
            with origin.accept()[0] as server, client.makefile("rb") as reader:
                assert b" 200 " in reader.readline()
                while (line := reader.readline()) != b"\r\n":
                    assert line
                client.sendall(b"two-hop test\n")
                with server.makefile("rb") as receiver:
                    assert receiver.readline() == b"two-hop test\n"
                server.sendall(b"reply\n")
                assert reader.readline() == b"reply\n"
                assert observed == [(entry_port,middle_port),(middle_port,exit_port),(exit_port,origin_port)],observed
                for _ in range(30):
                    history = api("GET","traffic/history?granularity=hour",200)
                    assert history["samplingError"] is None, history
                    if history["uploadTotal"] >= 13 and history["downloadTotal"] >= 6:
                        break
                    time.sleep(.1)
                [flow] = [r for r in history["rows"] if r["target"] == f"origin.example:{origin_port}"]
                assert flow["upload"] == 13 and flow["download"] == 6,flow
                assert api("GET","traffic",200)["connectionCount"] > 0
        api("POST","disconnect",200)
        api("POST","shutdown",200)
        assert proc.wait(timeout=6) == 0 and not pids(kernel) and not pids(service)
        proc.stdout.close()
        proc = launch()
        bootstrap = json.loads(proc.stdout.readline())
        port = bootstrap["port"]
        for gran,fmt in [("hour","%Y-%m-%dT%H"),("day","%Y-%m-%d"),("month","%Y-%m"),("year","%Y")]:
            date = time.strftime(fmt,time.strptime(history["date"],"%Y-%m-%dT%H"))
            hist = api("GET","traffic/history?granularity="+gran+"&date="+date,200)
            [saved] = [r for r in hist["rows"] if r["id"] == flow["id"]]
            assert (saved["target"],saved["upload"],saved["download"]) == (flow["target"],13,6)
            assert hist["uploadTotal"] >= flow["upload"] and hist["downloadTotal"] >= flow["download"]
        print(f"Three-hop CONNECT verified; persisted {flow['upload']}/{flow['download']} bytes")
        with listener() as a, listener() as b:
            mixed, ctrl = a.getsockname()[1], b.getsockname()[1]
        options = {"directory":temp,"config":{
            "mixedPort":mixed,"controllerPort":ctrl,"proxies":[],"choices":["REJECT","DIRECT"],
            "proxyProviders":{},"ruleProviders":{},"domesticDirect":False,"preventDnsLeaks":False}}
        api("PUT","nodes/chain",200,{"enabled":False,"hops":[]})
        bad = {**options, "config":{**options["config"], "tun":True}}
        assert "unknown field" in api("POST","connect",400,bad)["error"] and not pids(kernel)
        api("POST", "connect", 200, options)
        assert not pids(kernel)
        state = api("GET","status",200)
        assert state["connected"] and state["mixedPort"] == mixed and not state["proxyEnabled"]
        assert api("GET","identify",200,auth=False)["running"]
        assert api("GET","proxies",200)["proxies"]["SmartVPN"]["now"] == "REJECT"
        api("PUT","proxies/SmartVPN",202,{"name":"DIRECT"})
        assert api("GET","proxies",200)["proxies"]["SmartVPN"]["now"] == "DIRECT"
        api("POST","settings",409,{"socksPort":18482})
        query = urlencode({"url":f"http://127.0.0.1:{probe.server_port}/check","timeout":3000})
        delays = {r["name"]:r for r in api("GET","nodes/latency?"+query,200)["results"]}
        assert delays["DIRECT"]["latencyMs"] >= 0 and delays["REJECT"]["error"]
        with closing(sqlite3.connect(Path(temp,"smartvpn.db"))) as db:
            assert db.execute("pragma journal_mode").fetchone() == ("wal",)
            assert db.execute("select latency_ms,timestamp from probe_results where name='DIRECT'").fetchone() == (
                delays["DIRECT"]["latencyMs"], delays["DIRECT"]["timestamp"])
        reports = api("GET","nodes/probes",200)["results"]
        assert {r["name"] for r in reports if r.get("name") in delays and r["hops"] == [r["name"]]} == set(delays)
        assert any(r["hops"] == hops for r in reports)
        before = api("GET","traffic",200)
        with socket.create_connection(("127.0.0.1",mixed),timeout=6) as client:
            client.sendall(f"CONNECT 127.0.0.1:{origin_port} HTTP/1.1\r\nHost: 127.0.0.1:{origin_port}\r\n\r\n".encode())
            with origin.accept()[0] as server, client.makefile("rb") as reader:
                server.settimeout(6)
                assert b" 200 " in reader.readline()
                while (line := reader.readline()) != b"\r\n":
                    assert line, "truncated CONNECT headers"
                client.sendall(b"phase4b\n")
                with server.makefile("rb") as upstream:
                    assert upstream.readline() == b"phase4b\n"
                server.sendall(b"ack\n")
                assert reader.readline() == b"ack\n"
                traffic = api("GET","traffic",200)
                for side in ("upload","download"):
                    assert traffic[side+"Total"] > before[side+"Total"] and traffic[side+"Rate"] > 0, traffic
                [row] = [c for c in traffic["connections"] if c["destinationPort"] == str(origin_port)]
                assert row["network"] == "tcp" and isinstance(row["sourcePort"],str)
                [flow] = [c for c in api("GET","connections",200)["connections"]
                          if c["metadata"]["destinationPort"] == str(origin_port)]
                assert flow["metadata"]["network"] == "tcp" and isinstance(flow["metadata"]["sourcePort"],str)
        api("POST","disconnect",200)
        assert not api("GET","status",200)["connected"] and not pids(kernel)
        api("POST","connect",200,options)
        assert not pids(kernel)
        api("POST","shutdown",200)
        assert proc.wait(timeout=6) == 0
        for _ in range(10):
            if not pids(kernel):
                break
            time.sleep(0.1)
        assert not pids(kernel) and not pids(service)
        proc.stdout.close()
        portable_env = child_env.copy()
        portable_env.pop("SMARTVPN_DATA_DIR", None)
        alias = bundle / "smartvpn-service-current.exe"
        proc = subprocess.Popen([alias], cwd=temp, env=portable_env, stdout=subprocess.PIPE, stderr=log)
        bootstrap = json.loads(proc.stdout.readline())
        port = bootstrap["port"]
        portable_dir = bundle / "SmartVPN-data"
        assert api("GET","state",200)["dataDir"] == str(portable_dir)
        assert (portable_dir / "smartvpn.db").is_file()
        api("POST","shutdown",200)
        assert proc.wait(timeout=6) == 0 and not pids(alias)
        proc.stdout.close()
        proc = launch()
        bootstrap = json.loads(proc.stdout.readline())
        port = bootstrap["port"]
        api("POST","connect",200,options)
        proc.kill()
        proc.wait(timeout=6)
        deadline = time.monotonic() + 6
        while pids(kernel) and time.monotonic() < deadline:
            time.sleep(0.05)
        assert not pids(kernel) and not pids(service)
        log.seek(0)
        messages = log.read().lower()
        print(f"SOCKS5 member {name!r}: {member['latencyMs']} ms; DIRECT {delays['DIRECT']['latencyMs']} ms; REJECT recorded failure")
        print(f"Traffic upload/download: {traffic['uploadTotal']}/{traffic['downloadTotal']} bytes, "
              f"{traffic['uploadRate']}/{traffic['downloadRate']} B/s")
        assert secret.encode() not in messages
        assert b"downloading" not in messages and b"re-downloading" not in messages
    print("Three-hop cold/warm probe, region verification/lock, GEOIP CN route, fetch-time quota and scheduled merge during a live tunnel passed")
    print("Graceful shutdown and forced termination: 0 fixture service or kernel processes")


if __name__ == "__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--zip",type=Path,help="run the packaged service")
    main(parser.parse_args().zip)
