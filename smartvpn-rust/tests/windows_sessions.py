"""Exercise the Windows distribution's service, mixed listener and embedded clash-rs kernel."""
import argparse
import asyncio
import http.client
import json
import os
from pathlib import Path
import secrets
import sys
import tempfile
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT/"backend-rust/host/android-acceptance"))
import session_fixture as fixture


async def connections(port, count):
    barrier = asyncio.Barrier(count)
    async def stream():
        reader, writer = await asyncio.open_connection(fixture.LOOP, port)
        try:
            writer.write(f"CONNECT 198.51.100.61:{fixture.ECHO} HTTP/1.1\r\nHost: 198.51.100.61:{fixture.ECHO}\r\n\r\n".encode())
            response = await reader.readuntil(b"\r\n\r\n")
            if b" 200 " not in response.splitlines()[0]:
                raise IOError(response.splitlines()[0])
            data = secrets.token_bytes(512)
            writer.write(data)
            await writer.drain()
            if await reader.readexactly(len(data)) != bytes(b ^ 90 for b in data):
                raise IOError("Incorrect echo response")
            await barrier.wait()
        finally:
            writer.close()
            await writer.wait_closed()
    await asyncio.wait_for(asyncio.gather(*(stream() for _ in range(count))), 15)


async def exercise(args, directory, uri):
    with ZipFile(args.archive) as archive:
        exe = archive.extract("smartvpn-service-current.exe",directory)
    with (directory/"service.log").open("wb") as log:
        child = await asyncio.create_subprocess_exec(exe,cwd=directory,
            env={**os.environ,"SMARTVPN_DATA_DIR":str(directory)},
            stdout=asyncio.subprocess.PIPE,stderr=log)
        try:
            info = json.loads(await asyncio.wait_for(child.stdout.readline(),20))
            def api(method, endpoint, data=None, *, expected_status=200):
                c = http.client.HTTPConnection(fixture.LOOP, info["port"], timeout=30)
                try:
                    c.request(method,"/api/"+endpoint,None if data is None else json.dumps(data),
                              {"Authorization":"Bearer "+info["token"],"Content-Type":"application/json"})
                    response = c.getresponse()
                    result = json.loads(response.read())
                    if response.status != expected_status:
                        raise RuntimeError(f"{endpoint} {response.status}: {result}")
                    return result
                finally:
                    c.close()
            api("PUT","settings",{"subscriptionUrl":uri})
            api("POST","nodes/fetch")
            nodes = api("GET","nodes")["nodes"]
            assert len(nodes)==1,nodes
            name = nodes[0]["name"]
            api("POST","nodes/manual",{"name":"hop-socks","uri":f"socks5://127.0.0.1:{fixture.EXIT}"})
            if args.protocol in ("hysteria2", "tuic"):
                api("POST","nodes/manual",{"name":"extra-socks","uri":f"socks5://127.0.0.1:{fixture.EXIT+4}"})
                invalid = {"name":"invalid","hops":["hop-socks","extra-socks",name],"original":""}
                result = api("PUT","nodes/chain/profile",invalid,expected_status=400)
                assert "only as the first or second hop" in str(result), result
            hops = ["hop-socks",name] if args.second_hop else [name,"hop-socks"]
            policy = {} if args.mode=="default" else {name:args.mode}
            api("PUT","nodes/chain/profile",{"name":"test","original":"","hops":hops,"sessions":policy})
            assert api("GET","state")["chainSessions"]["test"]==policy
            api("POST","nodes/chain",{"name":"test","hops":hops})
            api("POST","connect")
            port = api("GET","status")["mixedPort"]
            await connections(port,6)
            await asyncio.sleep(40)
            await connections(port,1)
            api("POST","disconnect")
            api("POST","shutdown")
            await asyncio.wait_for(child.wait(),10)
            assert child.returncode==0
        finally:
            if child.returncode is None:
                child.kill()
                await child.wait()


if __name__ == "__main__":
    p=argparse.ArgumentParser(description=__doc__)
    for arg in ("archive","sing-box","protocol","mode","out"):
        p.add_argument("--"+arg,required=True)
    p.add_argument("--second-hop",action="store_true")
    p.add_argument("--offset",type=int,default=0)
    args=p.parse_args()
    args.archive=Path(args.archive).resolve()
    out=Path(args.out).resolve()
    out.mkdir(parents=True,exist_ok=True)
    for key in ("FRONT","BACK","EXIT","ECHO"):
        setattr(fixture,key,getattr(fixture,key)+args.offset)
    with tempfile.TemporaryDirectory(dir=out) as temp:
        folder=Path(temp)
        try:
            asyncio.run(fixture.run(args,folder,lambda uri: exercise(args,folder,uri)))
        finally:
            for filename in ("server.log","service.log"):
                if (folder/filename).exists():
                    (out/filename).write_bytes((folder/filename).read_bytes())
