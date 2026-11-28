"""Windows TCP/UDP acceptance through SOCKS5 -> Hysteria2 / TUIC. Run with a fresh --out directory."""
import argparse
import asyncio
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "backend-rust/host/android-acceptance"))
from session_fixture import configure

LOOP = "127.0.0.1"
ADAPTER = "SmartVPN-QA-QUIC"
MIXED = 39240
PROTOCOLS = ("hysteria2", "tuic")


def setup(folder, tun):
    proxies, rules, routes, cases = [], [], [], []
    for i, proto in enumerate(PROTOCOLS):
        dest = f"198.51.100.{81+i}"
        peer = f"192.0.2.{81+i}"
        port = 39250 + i*10
        case = folder / proto
        case.mkdir()
        configure(case, proto, True, LOOP)
        server = json.loads((case/"server.json").read_text())
        entry, socks = server["inbounds"]
        entry["listen_port"] = port+1
        socks["listen_port"] = port
        server["route"]["rules"] = [
            dict(inbound="exit", network="udp", ip_cidr=peer+"/32", port=port+1,
                 action="route", outbound="local", override_address=LOOP),
            dict(inbound="entry", ip_cidr=dest+"/32", port=port+2,
                 action="route", outbound="local", override_address=LOOP),
            dict(action="reject")]
        (case/"server.json").write_text(json.dumps(server))
        first = "first-"+proto
        proxies += [dict(name=first,type="socks5",server=LOOP,port=port,udp=True),
                    {"name":proto,"type":proto,"server":peer,"port":port+1,
                     "sni":"avd.test","skip-cert-verify":True,"udp":True,
                     "dialer-proxy":first, **entry["users"][0]}]
        rules.append(f"IP-CIDR,{dest}/32,{proto},no-resolve")
        routes.append(dest+"/32")
        cases.append((proto,dest,port+2,peer))
    config = {"mode":"rule","mixed-port":MIXED,"allow-lan":False,"log-level":"debug",
              "proxies":proxies,"rules":rules+["MATCH,REJECT"],"mmdb":"Country.mmdb"}
    if tun:
        config["tun"] = {"enable":True,"device-id":"dev://"+ADAPTER,"route-all":False,
                         "routes":routes,"gateway":"198.19.241.1/30","mtu":1500,"dns-hijack":False}
    (folder/"kernel.json").write_text(json.dumps(config))
    shutil.copyfile(ROOT/"smartvpn-rust/vendor/Country.mmdb",folder/"Country.mmdb")
    return cases


def exchange(dest, port, tun):
    addr = socket.inet_aton(dest) + port.to_bytes(2,"big")
    results = {}
    for kind in ("tcp","udp"):
        body = secrets.token_bytes(512)
        expected = bytes(b ^ 90 for b in body)
        with socket.socket() as control, socket.socket(type=socket.SOCK_DGRAM) as udp:
            control.settimeout(15)
            udp.settimeout(15)
            if not tun:
                control.connect((LOOP,MIXED))
                stream = control.makefile("rb",buffering=0)
                control.sendall(b"\x05\x01\x00")
                assert stream.read(2)==b"\x05\x00"
                op = b"\x01" if kind=="tcp" else b"\x03"
                control.sendall(b"\x05"+op+b"\x00\x01"+(addr if kind=="tcp" else bytes(6)))
                head = stream.read(4)
                assert head[:2]==b"\x05\x00",head
                stream.read({1:4,4:16}[head[3]])
                relay_port=int.from_bytes(stream.read(2),"big")
            if kind=="tcp":
                if tun:
                    control.connect((dest,port))
                control.sendall(body)
                with control.makefile("rb") as data:
                    response=data.read(len(body))
            elif tun:
                udp.sendto(body,(dest,port))
                response,source=udp.recvfrom(2048)
                assert source==(dest,port),source
            else:
                udp.sendto(b"\x00\x00\x00\x01"+addr+body,(LOOP,relay_port))
                frame=udp.recv(2048)
                assert frame[:10]==b"\x00\x00\x00\x01"+addr
                response=frame[10:]
            assert response==expected,(kind,dest,len(response))
            results[kind]=len(response)
    return results


class EchoUDP(asyncio.DatagramProtocol):
    def __init__(self):
        self.probes = []

    def connection_made(self, transport):
        self.transport=transport
    def datagram_received(self, data, addr):
        if len(data) == 1024 and data.startswith(b"SVP1"):
            self.probes.append((data[4], data[5]))
        self.transport.sendto(bytes(b^90 for b in data),addr)


def probe_udp_round(dest, port, round_id):
    with socket.socket(type=socket.SOCK_DGRAM) as udp:
        udp.settimeout(1.5)
        udp.connect((dest, port))
        for seq in range(20):
            body = b"SVP1" + bytes((round_id, seq)) + secrets.token_bytes(1018)
            udp.send(body)
            try:
                reply = udp.recv(2048)
            except TimeoutError as err:
                raise RuntimeError(f"UDP timeout at round {round_id}, sequence {seq}") from err
            if reply != bytes(b ^ 90 for b in body):
                raise ValueError(f"Incorrect UDP response at round {round_id}, sequence {seq}")


async def echo(reader,writer):
    try:
        data=await reader.readexactly(512)
        writer.write(bytes(b^90 for b in data))
        await writer.drain()
    finally:
        writer.close()


async def run(args):
    from contextlib import ExitStack, AsyncExitStack
    folder=Path(args.out).resolve()
    folder.mkdir(parents=True,exist_ok=False)
    cases=setup(folder,args.tun)
    with ExitStack() as stack, (folder/"kernel.log").open("wb") as logs:
        async with AsyncExitStack() as astack:
            echoes = {}
            for proto,dest,port,peer in cases:
                case=folder/proto
                log=stack.enter_context((case/"server.log").open("wb"))
                proc=subprocess.Popen([args.sing_box,"run","-c",str(case/"server.json")],stdout=log,stderr=log)
                stack.callback(stop_process,proc)
                await astack.enter_async_context(await asyncio.start_server(echo,LOOP,port))
                transport,echoes[proto]=await asyncio.get_running_loop().create_datagram_endpoint(EchoUDP,local_addr=(LOOP,port))
                stack.callback(transport.close)
            if args.tun:
                # The parent can stay unprivileged. Elevate only the dedicated kernel with two /32 routes.
                env={**os.environ,"QA_EXE":str(Path(args.kernel).resolve()),"QA_DIR":str(folder)}
                launch=["powershell","-NoProfile","-Command",
                        "$ErrorActionPreference='Stop'; $p=Start-Process -Verb RunAs -WindowStyle Hidden -PassThru -FilePath $env:QA_EXE -ArgumentList ('\"'+$env:QA_DIR+'\"'); $p.WaitForExit(); exit $p.ExitCode"]
            else:
                env=os.environ
                launch=[args.kernel,str(folder)]
            kernel=subprocess.Popen(launch,env=env,stdout=logs,stderr=logs)
            try:
                for _ in range(120):
                    if kernel.poll() is not None:
                        raise RuntimeError(f"kernel exited {kernel.returncode}; see {folder}")
                    if args.tun:
                        command="(Get-NetRoute -DestinationPrefix '198.51.100.81/32','198.51.100.82/32' -ErrorAction SilentlyContinue | Where-Object InterfaceAlias -eq '"+ADAPTER+"').Count"
                        count=await asyncio.to_thread(subprocess.check_output,["powershell","-NoProfile","-Command",command],text=True)
                        if count.strip()=="2":
                            break
                    else:
                        with socket.socket() as check:
                            if check.connect_ex((LOOP,MIXED))==0:
                                break
                    await asyncio.sleep(0.25)
                else:
                    raise TimeoutError("kernel route/listener did not start")
                for proto,dest,port,peer in cases:
                    result=await asyncio.to_thread(exchange,dest,port,args.tun)
                    if args.tun and proto == "hysteria2":
                        for round_id in range(1, 6):
                            await asyncio.to_thread(probe_udp_round, dest, port, round_id)
                            if echoes[proto].probes != [(round_id, seq) for seq in range(20)]:
                                raise AssertionError(f"Unexpected echo sequence: {echoes[proto].probes}")
                            echoes[proto].probes.clear()
                            print(f"{proto} Wintun round {round_id}: 20 sent, 20 echoed, 20 verified (1024 bytes)", flush=True)
                    evidence=(folder/proto/"server.log").read_text(encoding="utf8")
                    for event in (f"inbound/socks[exit]: inbound packet connection to {peer}:{port-1}",
                                  f"inbound/{proto}[entry]: inbound connection to {dest}:{port}",
                                  f"inbound/{proto}[entry]: inbound packet connection to {dest}:{port}"):
                        assert event in evidence, event
                    print(json.dumps(dict(protocol=proto,tun=args.tun,adapter=ADAPTER if args.tun else None,
                                          first_hop_udp_to=peer,second_hop_target=dest,verified_bytes=result)),flush=True)
            finally:
                (folder/"stop").touch()
                kernel.wait(timeout=25)
            assert kernel.returncode==0,kernel.returncode


def stop_process(process):
    if process.poll() is None:
        process.terminate()
    process.wait(timeout=10)


if __name__=="__main__":
    p=argparse.ArgumentParser(description=__doc__)
    for arg in ("out","kernel","sing-box"):
        p.add_argument("--"+arg,required=True)
    p.add_argument("--tun",action="store_true")
    asyncio.run(run(p.parse_args()))
