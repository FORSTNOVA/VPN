"""Run on a disposable, booted AVD with the APK, runner, and VPN consent installed.
Requires Python cryptography, adb and sing-box 1.14. Pass absolute executable paths.
"""
import argparse
import asyncio
import base64
import contextlib
import datetime
import json
from pathlib import Path
import socket
import tempfile
import time
import uuid

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID

LOOP = "127.0.0.1"
FRONT, BACK, EXIT, ECHO = 38679, 38680, 38681, 18061


def configure(folder, protocol, second_hop):
    password, user = str(uuid.uuid4()), str(uuid.uuid4())
    entry = dict(type=protocol, tag="entry", listen=LOOP, listen_port=BACK)
    peer = f"10.0.2.2:{FRONT}"
    if protocol in ("vmess","vless"):
        entry.update(users=[dict(uuid=user)], multiplex=dict(enabled=True),
                     transport=dict(type="ws", path="/mux"))
        share = dict(v="2", ps="session-entry", add="10.0.2.2", port=str(FRONT),
                     id=user, aid="0", scy="auto", net="ws", path="/mux", tls="")
        if protocol == "vmess":
            uri = "vmess://" + base64.b64encode(json.dumps(share).encode()).decode()
        else:
            uri = f"vless://{user}@{peer}?type=ws&path=%2Fmux&security=none#session-entry"
    elif protocol == "shadowsocks":
        method = "aes-128-gcm"
        entry.update(method=method, password=password, multiplex=dict(enabled=True))
        encoded = base64.urlsafe_b64encode(f"{method}:{password}".encode()).decode().rstrip("=")
        uri = f"ss://{encoded}@{peer}#session-entry"
    else:
        name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "avd.test")])
        key = ec.generate_private_key(ec.SECP256R1())
        now = datetime.datetime.now(datetime.timezone.utc)
        cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
                .public_key(key.public_key()).serial_number(x509.random_serial_number())
                .not_valid_before(now-datetime.timedelta(days=1)).not_valid_after(now+datetime.timedelta(days=1))
                .sign(key, hashes.SHA256()))
        (folder/"test.crt").write_bytes(cert.public_bytes(serialization.Encoding.PEM))
        (folder/"test.key").write_bytes(key.private_bytes(serialization.Encoding.PEM,
            serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
        entry["tls"] = dict(enabled=True, alpn=["h3"], certificate_path=str(folder/"test.crt"), key_path=str(folder/"test.key"))
        credentials = dict(password=password)
        auth = password
        if protocol == "tuic":
            credentials["uuid"] = user
            auth = f"{user}:{password}"
        if protocol == "trojan":
            entry["multiplex"] = dict(enabled=True)
            entry["tls"]["alpn"] = ["http/1.1"]
        entry["users"] = [credentials]
        uri = f"{protocol}://{auth}@{peer}?sni=avd.test&insecure=1#session-entry"
    config = dict(inbounds=[entry, dict(type="socks", tag="exit", listen=LOOP, listen_port=EXIT)],
        outbounds=[dict(type="direct",tag="local")],route=dict(rules=[
            dict(inbound="entry", ip_cidr="127.0.0.1/32", port=EXIT, action="route", outbound="local"),
            dict(inbound="exit", ip_cidr="198.51.100.61/32", port=ECHO, action="route", outbound="local", override_address=LOOP),
            dict(action="reject")]))
    if second_hop:
        config["route"]["rules"] = [
            dict(inbound="exit", ip_cidr="10.0.2.2/32", port=FRONT, action="route", outbound="local", override_address=LOOP),
            dict(inbound="entry", ip_cidr="198.51.100.61/32", port=ECHO, action="route", outbound="local", override_address=LOOP),
            dict(action="reject")]
    (folder/"server.json").write_text(json.dumps(config))
    return uri


async def command(*args):
    proc = await asyncio.create_subprocess_exec(*args, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.STDOUT)
    output = (await proc.communicate())[0].decode(errors="replace")
    if proc.returncode:
        raise RuntimeError(output)
    return output


async def run(args, folder):
    uri = configure(folder, args.protocol, args.second_hop)
    handshakes = []
    samples = []
    idle_wakeups = []
    loop = asyncio.get_running_loop()
    start = time.monotonic()
    def event(kind, ident):
        return dict(event=kind, time=round(time.monotonic()-start, 2), connection=ident)

    async def echo(reader, writer):
        try:
            data = await reader.readexactly(512)
            samples.append(event("echo", len(handshakes)))
            writer.write(bytes(b ^ 90 for b in data))
            await writer.drain()
            await reader.read()
        finally:
            writer.close()

    async def tcp_entry(reader, writer):
        remote, upstream = await asyncio.open_connection(LOOP, BACK)
        async def copy(source, dest):
            while data := await source.read(65536):
                dest.write(data)
                await dest.drain()
        try:
            if args.protocol in ("vmess","vless"):
                upstream.write(await reader.readuntil(b"\r\n\r\n"))
                response = await remote.readuntil(b"\r\n\r\n")
                if response.split()[1] != b"101":
                    raise RuntimeError("WebSocket upgrade rejected")
                writer.write(response)
                kind = "HTTP 101"
            elif args.protocol == "trojan":
                # TLS record type handshake (22), handshake type ClientHello (1).
                header = await reader.readexactly(6)
                if header[0] != 22 or header[5] != 1:
                    raise RuntimeError("Missing TLS ClientHello")
                upstream.write(header)
                kind = "TLS ClientHello"
            else:
                upstream.write(await reader.readexactly(16))
                kind = "Shadowsocks AEAD salt"
            handshakes.append(event(kind, writer.get_extra_info("peername")[1]))
            tasks = [asyncio.create_task(copy(reader, upstream)), asyncio.create_task(copy(remote, writer))]
            done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
            for task in pending:
                task.cancel()
            for task in done:
                task.result()
        finally:
            upstream.close()
            writer.close()

    async def udp(front):
        # Preserve a unique upstream socket per AVD source. Deduplicate retransmitted
        # QUIC v1 Initials by client source CID and source socket. The destination
        # CID can legitimately change mid-handshake after a server Initial or Retry.
        last_packet = time.monotonic()
        def observe(direction):
            nonlocal last_packet
            now = time.monotonic()
            if len(samples) == 6 and now-last_packet > 1:
                idle_wakeups.append(dict(time=round(now-start,2),sender=direction))
            last_packet = now
        mappings, replies, initials = {}, [], set()
        async def relay_back(sock, addr):
            while True:
                data = await loop.sock_recv(sock, 65535)
                observe("server")
                await loop.sock_sendto(front, data, addr)
        try:
            while True:
                data, addr = await loop.sock_recvfrom(front, 65535)
                observe("client")
                if data[0] & 0xf0 == 0xc0 and data[1:5] == bytes.fromhex("00000001"):
                    offset = 6 + data[5]
                    cid = f"{addr[1]}:" + data[offset+1:offset+1+data[offset]].hex()
                    if cid not in initials:
                        initials.add(cid)
                        handshakes.append(event("QUIC Initial", cid))
                if addr not in mappings:
                    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                    sock.setblocking(False)
                    sock.connect((LOOP, BACK))
                    mappings[addr] = sock
                    replies.append(asyncio.create_task(relay_back(sock, addr)))
                await loop.sock_sendall(mappings[addr], data)
        finally:
            for task in replies:
                task.cancel()
            for sock in mappings.values():
                sock.close()

    with (folder/"server.log").open("wb") as log:
        server = await asyncio.create_subprocess_exec(args.sing_box, "run", "-c", str(folder/"server.json"), stdout=log, stderr=log)
        try:
            async with contextlib.AsyncExitStack() as stack:
                await stack.enter_async_context(await asyncio.start_server(echo, LOOP, ECHO))
                if args.protocol in ("vmess","vless","trojan","shadowsocks"):
                    await stack.enter_async_context(await asyncio.start_server(tcp_entry, LOOP, FRONT))
                else:
                    front = stack.enter_context(socket.socket(socket.AF_INET, socket.SOCK_DGRAM))
                    front.setblocking(False)
                    front.bind((LOOP, FRONT))
                    task = asyncio.create_task(udp(front))
                    stack.callback(task.cancel)
                await asyncio.sleep(1)
                if server.returncode is not None:
                    raise RuntimeError((folder/"server.log").read_text())
                adb = [args.adb, "-s", args.serial, "shell"]
                device = await command(*adb, "getprop", "ro.kernel.qemu")
                if device.strip() != "1":
                    raise RuntimeError("Expected an Android Studio AVD")
                output = await command(*adb, "am", "instrument", "-w", "-r", "-e", "sessionMode", args.mode,
                    "-e", "secondHop", str(args.second_hop).lower(), "-e", "nodeLink64", base64.urlsafe_b64encode(uri.encode()).decode(), "com.smartvpn.acceptance/.DeviceRun")
                print(output, flush=True)
                print(json.dumps(dict(protocol=args.protocol,mode=args.mode,hop=2 if args.second_hop else 1,handshakes=handshakes,echoes=samples,idle_wakeups=idle_wakeups)),flush=True)
                if "INSTRUMENTATION_CODE: -1" not in output or len(samples) != 7 or samples[6]["time"] - samples[5]["time"] < 39:
                    raise RuntimeError("Missing instrumentation or idle traffic proof")
                if args.mode in ("mux","keepalive") and len(handshakes) != 1:
                    raise RuntimeError("Expected one reusable connection")
                if args.protocol == "vmess" and args.mode == "default" and len(handshakes) != 7:
                    raise RuntimeError("Expected seven independent VMess connections in control run")
                if not handshakes:
                    raise RuntimeError("No transport handshake captured")
        finally:
            if server.returncode is None:
                server.terminate()
            await server.wait()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ["adb","serial","sing-box"]:
        parser.add_argument("--"+key,required=True)
    parser.add_argument("--protocol",choices=["vmess","vless","trojan","shadowsocks","hysteria2","tuic"],required=True)
    parser.add_argument("--mode",choices=["default","keepalive","mux"],required=True)
    parser.add_argument("--second-hop",action="store_true",help="put the tested node after SOCKS5")
    parser.add_argument("--out",type=Path,required=True)
    args = parser.parse_args()
    args.out.mkdir(exist_ok=True, parents=True)
    with tempfile.TemporaryDirectory(dir=args.out) as certs:
        folder=Path(certs)
        try:
            asyncio.run(run(args,folder))
        finally:
            if (folder/"server.log").exists():
                (args.out/"server.log").write_bytes((folder/"server.log").read_bytes())
