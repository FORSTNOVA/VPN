import base64
import http.server
import select
import socket
import socketserver
import struct
import threading
import time
import urllib.parse

ui_finished=threading.Event()
HTTP = 38471
SOCKS = 38472

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        if self.path == "/test/reset":
            ui_finished.clear()
        elif self.path == "/test/finish":
            ui_finished.set()
        else:
            self.send_error(404)
            return
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        # HTTP clients can send either an absolute URI or an origin-form target.
        self.path = urllib.parse.urlsplit(self.path).path
        if self.path in {"/domains.txt","/networks.yaml"}:
            print("HTTP provider request " + self.path, flush=True)
        if self.path == "/test/ui-finished":
            self.send_response(200 if ui_finished.is_set() else 202)
            self.end_headers()
            return
        routes = {
            "/rule-policy": b"""proxies:
  - {name: fixture, type: socks5, server: 127.0.0.1, port: 38472}
rule-providers:
  domains: {type: http, behavior: domain, format: text, url: 'http://127.0.0.1:38471/domains.txt', path: '../bad'}
  networks: {type: http, behavior: ipcidr, url: 'http://127.0.0.1:38471/networks.yaml'}
rules:
  - RULE-SET,domains,REJECT-DROP
  - RULE-SET,networks,REJECT-DROP,no-resolve
  - DOMAIN-REGEX,^blocked[.]example$,REJECT-DROP
  - GEOIP,CN,DIRECT
  - DST-PORT,45555,REJECT-DROP
  - AND,((NETWORK,TCP),(DST-PORT,45556)),REJECT
  - DOMAIN-WILDCARD,*.unsupported.example,DIRECT
""",
            "/domains.txt": b"blocked.example\n",
            "/networks.yaml": b"payload:\n  - 198.51.100.99/32\n",
            "/lan": b"LAN direct fixture",
            "/chain-exit": "proxies:\n - {name: '出口 [C]', type: socks5, server: 127.0.0.1, port: 38472}\n".encode(),
            "/chain-entry": "proxies:\n - {name: '中继 [B]', type: socks5, server: 127.0.0.1, port: 38474}\n - {name: '入口 [A]', type: socks5, server: 127.0.0.1, port: 38473}\n".encode(),
            "/chain-target": b"SmartVPN multihop exact body",
            "/yaml": b"\xef\xbb\xbfproxies:\n - {name: First, type: socks5, server: 127.0.0.1, port: 38473}\n - {name: \"\\u516c\\u544a\"}\n",
            "/base64": base64.urlsafe_b64encode(b"socks5://127.0.0.1:38472#Second").rstrip(b"="),
            "/china.txt": b"192.0.2.0/24\n",
            "/china6.txt": b"2001:db8::/32\n",
        }
        if self.path not in {"/chain-target","/domains.txt","/networks.yaml","/lan"} and self.headers.get("User-Agent") != "clash.meta":
            self.send_error(403)
            return
        if self.path == "/redirect":
            self.send_response(302)
            self.send_header("Location", "/yaml")
            self.end_headers()
            return
        if self.path == "/chain-target":
            time.sleep(2)
        data = routes.get(self.path)
        if data is None:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Subscription-Userinfo", "upload=1; download=2; total=1024; expire=1900000000")
        self.end_headers()
        self.wfile.write(data)

class Proxy(socketserver.StreamRequestHandler):
    def handshake(self, greeting, acknowledge=True):
        version, n = greeting
        assert version == 5 and 0 in self.rfile.read(n)
        self.wfile.write(b"\x05\0")
        version, cmd, _, kind = self.rfile.read(4)
        assert version == 5 and cmd == 1
        sizes = {1: 4, 4: 16}
        length = self.rfile.read(1)[0] if kind == 3 else sizes[kind]
        address = self.rfile.read(length)
        host = address.decode() if kind == 3 else socket.inet_ntop(socket.AF_INET if kind == 1 else socket.AF_INET6, address)
        port, = struct.unpack("!H", self.rfile.read(2))
        if acknowledge:
            self.wfile.write(b"\x05\0\0\x01\x7f\0\0\x01\0\0")
        return host, port

    def handle(self):
        self.request.settimeout(12)
        greeting = self.rfile.read(2)
        if greeting == b"":
            print("TCP client closed before SOCKS greeting", flush=True)
            return
        host, port = self.handshake(greeting)
        if hasattr(self.server, "recovery_tls") and (host,port) not in {
                ("recovery.example",443), ("198.51.100.7",80)}:
            return  # The recovery fixture serves only its two synthetic origins.
        if (host, port) == ("recovery.example",443):
            with self.server.recovery_tls.wrap_socket(self.request,server_side=True) as tls:
                with tls.makefile("rb") as request:
                    assert request.readline().startswith(b"GET /tunnel-proof HTTP/")
                    while (header := request.readline()) != b"\r\n":
                        assert header
                tls.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 19\r\nConnection: close\r\n\r\nSmartVPN-16k-tunnel")
            return
        local = self.server.server_address[1]
        if (host, port) == ("198.51.100.7", 38476):
            print(f"CONNECT {local} -> fixture HTTP {HTTP}", flush=True)
            host, port = "127.0.0.1", HTTP
        if (host, port) in {("127.0.0.1", SOCKS), ("127.0.0.1", 38474), ("127.0.0.1", HTTP)}:
            print(f"CONNECT {local} -> {host}:{port}", flush=True)
            with socket.create_connection((host, port), timeout=5) as peer:
                while True:
                    ready, _, _ = select.select([self.connection, peer], [], [], 15)
                    if not ready:
                        raise TimeoutError("relay idle")
                    for src in ready:
                        data = src.recv(65536)
                        if not data:
                            return
                        (peer if src is self.connection else self.connection).sendall(data)
        print(f"CONNECT {local} -> {host}:{port}", flush=True)
        if port == 80:
            line = self.rfile.readline()
            headers = {}
            while (header := self.rfile.readline()) not in (b"\r\n", b""):
                name, value = header.split(b":", 1)
                headers[name.lower()] = value.strip()
            if b"/recovery-hold" in line:
                self.wfile.write(b"HTTP/1.1 200 OK\r\nContent-Length: 1024\r\n\r\n")
                self.request.settimeout(60)
                assert self.rfile.read(1) == b"", "unexpected data on held socket"
                return
            if b"content-length" in headers:
                count = int(headers[b"content-length"])
                assert count <= 10_000_000
                assert len(self.rfile.read(count)) == count
            time.sleep(0.15 if self.server.server_address[1] == 38473 else 0.025)
            if b"/tunnel-proof" in line:
                data = b"SmartVPN-16k-tunnel"
                print("TUN HTTP request", flush=True)
            elif b"/trace" in line:
                data = b"loc=US\n"
            elif b"/country" in line:
                data = b"US"
            elif b"/egress" in line:
                data = b"192.0.2.99"
            elif b"/__down?" in line:
                count = int(urllib.parse.parse_qs(urllib.parse.urlsplit(line.split()[1]).query)[b"bytes"][0])
                assert count == 10_000_000
                data = bytes(count)
            else:
                data = b"OK"
            self.wfile.write(b"HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: " + str(len(data)).encode() + b"\r\n\r\n" + data)

if __name__ == "__main__":
    with http.server.ThreadingHTTPServer(("127.0.0.1", HTTP), Handler) as httpd, socketserver.ThreadingTCPServer(("127.0.0.1", SOCKS), Proxy) as socks, socketserver.ThreadingTCPServer(("127.0.0.1",38473),Proxy) as slow:
        middle = socketserver.ThreadingTCPServer(("127.0.0.1",38474),Proxy)
        threading.Thread(target=middle.serve_forever,daemon=True).start()
        threading.Thread(target=slow.serve_forever,daemon=True).start()
        threading.Thread(target=socks.serve_forever, daemon=True).start()
        httpd.serve_forever()
