"""Run the second-hop VPN packet test against a loopback sing-box on an Android test device.
Requires an isolated SmartVPN profile, matching instrumentation APK and VPN consent.
"""
import argparse
import base64
import json
from pathlib import Path
import subprocess
import tempfile
import time
import re
import uuid

from session_fixture import BACK, ECHO, FRONT, configure


def run(args, folder):
    def adb(*params):
        return subprocess.check_output([args.adb,"-s",args.serial,*params],text=True,encoding="utf8",errors="replace")
    remote="/data/local/tmp/smartvpn-quic-"+uuid.uuid4().hex
    uri=configure(folder,args.protocol,True,"192.0.2.81")
    path=folder/"server.json"
    cfg=json.loads(path.read_text())
    entry=cfg["inbounds"][0]
    entry["tls"].update(certificate_path=remote+"/test.crt",key_path=remote+"/test.key")
    cfg["route"]["rules"][0].update(network="udp",override_port=BACK)
    path.write_text(json.dumps(cfg))
    adb("shell","mkdir","-m","700",remote)
    try:
        for src,dst in ((args.sing_box,"sing-box"),(path,"server.json"),(folder/"test.crt","test.crt"),(folder/"test.key","test.key")):
            adb("push",str(src),remote+"/"+dst)
        adb("shell","chmod","700",remote+"/sing-box")
        token=uuid.uuid4().hex
        with subprocess.Popen([args.adb,"-s",args.serial,"shell","am","instrument","-w","-r","-e","secondHop","true",
                "-e","tunPackets","true","-e","sessionMode","keepalive","-e","packetRun",token,"-e","nodeLink64",
                base64.urlsafe_b64encode(uri.encode()).decode(),"com.smartvpn.acceptance/.DeviceRun"],stdout=subprocess.PIPE,text=True,encoding="utf8") as instrument:
            for _ in range(100):
                line=re.search(token+r" (\d+)",adb("shell","logcat","-d","-s","SmartVPN-QuicTest:I"))
                if line: break
                if instrument.poll() is not None: raise RuntimeError(instrument.communicate()[0])
                time.sleep(.1)
            else: raise RuntimeError("system VPN readiness timeout")
            port=int(line.group(1))
            pid=int(adb("shell",f"{remote}/sing-box run -c {remote}/server.json >{remote}/server.log 2>&1 </dev/null & echo $!").strip())
            try:
                for _ in range(100):
                    log=adb("shell","cat",remote+"/server.log")
                    if "sing-box started" in log and "vpn enabled" in log: break
                    if "FATAL" in log: raise RuntimeError(log)
                    time.sleep(.1)
                else: raise RuntimeError("reference server did not observe the active VPN: "+log)
                adb("shell",f"echo Y | toybox nc -w 2 127.0.0.1 {port}")
                output=instrument.communicate(timeout=80)[0]
                (args.out/"instrumentation.txt").write_text(output,"utf8")
                if "INSTRUMENTATION_CODE: -1" not in output or "TUN TCP 32768 and UDP 1024 byte replies verified" not in output:
                    raise RuntimeError(output)
                evidence=adb("shell","cat",remote+"/server.log")
                for event in (f"inbound/socks[exit]: inbound packet connection to 192.0.2.81:{FRONT}",
                              f"inbound/{args.protocol}[entry]: inbound connection to 198.51.100.61:{ECHO}",
                              f"inbound/{args.protocol}[entry]: inbound packet connection to 198.51.100.61:{ECHO}"):
                    if event not in evidence: raise RuntimeError("missing "+event)
                print(f"{args.protocol}: VPN TCP 32768 / UDP 1024 bytes, both hops verified")
            finally:
                (args.out/"server.log").write_text(adb("shell","cat",remote+"/server.log"),"utf8")
                adb("shell",f"if [ -e /proc/{pid}/exe ] && [ \"$(readlink /proc/{pid}/exe)\" = {remote}/sing-box ]; then kill {pid}; fi")
    finally:
        adb("shell","rm","-rf",remote)


if __name__=="__main__":
    p=argparse.ArgumentParser(description=__doc__)
    for opt in ("adb","serial","sing-box"): p.add_argument("--"+opt,required=True)
    p.add_argument("--protocol",required=True,choices=("hysteria2","tuic"))
    p.add_argument("--out",type=Path,required=True)
    args=p.parse_args()
    args.out.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(dir=args.out) as temp:
        run(args,Path(temp).resolve())
