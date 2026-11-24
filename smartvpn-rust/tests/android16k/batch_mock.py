"""Loopback fixtures for the batch instrumentation scenario."""
import base64
import http.server
import json
import select
import socket
import socketserver
import threading
import time
import uuid

import mock


class Subscription(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path not in ("/sub", "/dns", "/dead"):
            self.send_error(404)
            return
        nodes = [
            {"name": f"fixture-{i}", "type": "socks5",
             "server": "localhost" if i == 3 else "127.0.0.1", "port": "38572"}
            for i in range(4)
        ]
        nodes.extend([
            {"name": "fixture-hy", "type": "hysteria2", "server": "127.0.0.1",
             "port": 9, "password": "fixture", "up": "100 Mbps", "down": "3000 Mbps"},
            {"name": "fixture-vl", "type": "vless", "server": "127.0.0.1", "port": 9,
             "uuid": str(uuid.UUID(int=1)), "tls": True, "servername": "example.invalid",
             "reality-opts": {"public-key": base64.urlsafe_b64encode(bytes(range(32))).decode().rstrip("="),
                             "short-id": "ab"}},
            {"name": "unsupported", "type": "ssr", "server": "localhost", "port": 9},
        ])
        if self.path == "/dns":
            nodes = [{"name": "primary", "type": "socks5", "server": "localhost", "port": 38572},
                     {"name": "exit", "type": "socks5", "server": "localhost", "port": 38572}]
            nodes.extend({"name": f"dead-{i}", "type": "socks5", "server": "localhost",
                          "port": 9} for i in range(32))
        elif self.path == "/dead":
            nodes = [{"name": f"stall-{i}", "type": "socks5", "server": "localhost",
                      "port": 38572} for i in range(34)]
        data = json.dumps({"proxies": nodes}).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class DnsFixtureProxy(mock.Proxy):
    def handle(self):
        greeting = self.rfile.read(2)
        if not greeting:
            return
        host, port = self.handshake(greeting, acknowledge=False)
        success = b"\x05\x00\x00\x01" + bytes(6)
        if (host, port) == ("localhost", 38572):
            target = ("127.0.0.1", port)
        elif host in ("www.gstatic.com", "www.youtube.com", "api.ipify.org", "1.1.1.1", "8.8.8.8") and port == 443:
            target = (host, port)
        elif host == "timeout.example" and port == 80:
            self.wfile.write(success)
            self.rfile.readline()
            time.sleep(4)  # Deliberately exceed the controller's 2.5s deadline.
            return
        elif host == "browser.example" and port == 80:
            self.wfile.write(success)
            self.rfile.readline()
            while self.rfile.readline() not in (b"\r\n", b""):
                pass
            data = b"SmartVPN domain tunnel"
            self.wfile.write(b"HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: " +
                             str(len(data)).encode() + b"\r\n\r\n" + data)
            return
        elif host == "127.0.0.1" and port == 38572:
            target = (host, port)
        else:
            self.wfile.write(b"\x05\x02\x00\x01" + bytes(6))
            return
        with socket.create_connection(target, timeout=6) as peer:
            self.wfile.write(success)
            while True:
                ready, _, _ = select.select([self.connection, peer], [], [], 12)
                if not ready:
                    return
                for src in ready:
                    data = src.recv(65536)
                    if not data:
                        return
                    (peer if src is self.connection else self.connection).sendall(data)


if __name__ == "__main__":
    import sys
    handler = DnsFixtureProxy if sys.argv[1:] == ["dns"] else mock.Proxy
    mock.SOCKS = 38572
    with http.server.ThreadingHTTPServer(("127.0.0.1", 38571), Subscription) as source, socketserver.ThreadingTCPServer(("127.0.0.1", mock.SOCKS), handler) as socks:
        threading.Thread(target=socks.serve_forever, daemon=True).start()
        source.serve_forever()
