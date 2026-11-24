"""Observe the on-device acceptance test and measure traffic outside the VPN process."""
import os
import re
import time
import http.client
import xml.etree.ElementTree as ET
import subprocess
import sys
from pathlib import Path

serial, output = sys.argv[1:]
assert re.fullmatch(r"emulator-\d+",serial)
adb=[os.path.join(os.environ["LOCALAPPDATA"],"Android/sdk/platform-tools/adb.exe"),"-s",serial]
def run(*args):
    return subprocess.check_output([*adb,*args])
assert run("emu","avd","name").decode().split()[0]=="Medium_Phone"
out=Path(output)
out.mkdir(parents=True,exist_ok=True)
rows=[]
def record(text):
    print(text,flush=True)
    rows.append(text)
with subprocess.Popen([*adb,"shell","am","instrument","-w","com.smartvpn.acceptance/.Acceptance"],stdout=subprocess.PIPE,text=True,encoding="utf-8") as test:
    for line in test.stdout:
        record(line.strip())
        if line.strip()=="CONNECTED WINDOW":
            assert "tun0" in run("shell","ls","/sys/class/net").decode().split()
            installed=run("shell","pm","path","com.smartvpn.acceptance").decode().strip()
            assert installed.startswith("package:/data/app/") and installed.endswith("/base.apk")
            evidence=run("shell","env","CLASSPATH="+installed.removeprefix("package:"),"app_process","/","com.smartvpn.acceptance.TunnelProbe").decode().strip()
            assert len(re.findall(r"TUN HTTP 200 RTT \d+ ms",evidence))==5,evidence
            record(evidence)
            for _ in range(10):
                run("shell","uiautomator","dump","/data/local/tmp/smartvpn-ui.xml")
                if "断开代理连接" in run("shell","cat","/data/local/tmp/smartvpn-ui.xml").decode():break
                time.sleep(.25)
            else: raise AssertionError("UI did not show active connection")
            xml=ET.fromstring(run("shell","cat","/data/local/tmp/smartvpn-ui.xml"))
            button=next(n for n in xml.iter("node") if n.get("content-desc","").startswith("测试真连接延迟"))
            x,y,u,v=map(int,re.findall(r"\d+",button.get("bounds")))
            run("shell","input","tap",str((x+u)//2),str((y+v)//2))
            time.sleep(.7)
            run("shell","uiautomator","dump","/data/local/tmp/smartvpn-ui.xml")
            label=re.search(r"测试真连接延迟 · 首次 \d+ ms · 连接后 \d+ ms", run("shell","cat","/data/local/tmp/smartvpn-ui.xml").decode())
            assert label,"UI probe did not show both timings"
            record(label.group(0))
            (out/"11-connected.png").write_bytes(run("exec-out","screencap","-p"))
        if line.strip()=="DISCONNECTED UI WINDOW":
            time.sleep(2)
            subprocess.run([sys.executable,"-X","utf8",str(Path(__file__).with_name("screens.py")),serial,str(out)],check=True)
            client=http.client.HTTPConnection("127.0.0.1",38471,timeout=3)
            client.request("POST","/test/finish")
            assert client.getresponse().status==204
            client.close()
assert "ACCEPTANCE DONE" in rows,rows[-3:]
assert "tun0" not in run("shell","ls","/sys/class/net").decode().split()
record("tun0 absent after disconnect")
(out/"api-results.txt").write_text("\n".join(rows)+"\n",encoding="utf-8")
