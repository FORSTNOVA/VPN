"""Observe the pinned chain through Android's external VPN client."""
import os
import re
import subprocess
import sys
from pathlib import Path

serial, output = sys.argv[1:]
assert re.fullmatch(r"emulator-\d+", serial)
adb = [str(Path(os.environ["LOCALAPPDATA"], "Android/sdk/platform-tools/adb.exe")), "-s", serial]
def run(*args):
    return subprocess.check_output([*adb, *args])
assert run("emu","avd","name").decode().split()[0] == "Medium_Phone"
out = Path(output)
out.mkdir(parents=True, exist_ok=True)
with subprocess.Popen([*adb,"shell","am","instrument","-w","-e","scenario","chain",
                       "com.smartvpn.acceptance/.Acceptance"],stdout=subprocess.PIPE,encoding="utf-8") as test:
    done = False
    for line in test.stdout:
        print(line.rstrip(),flush=True)
        if line.strip() == "CHAIN CONNECTED WINDOW":
            assert b"tun0" in run("shell","ls","/sys/class/net").split()
            rules = run("shell","ip","rule").decode()
            ranges = [(int(a),int(b)) for a,b in re.findall(r"oif tun0 uidrange (\d+)-(\d+)",rules)]
            assert ranges, "VPN routing rules missing"
            for name in ("com.android.settings","com.android.chrome","com.android.shell"):
                row = run("shell","cmd","package","list","packages","-U",name).decode()
                uid = int(re.search(r"package:"+re.escape(name)+r" uid:(\d+)",row)[1])
                routed = any(a<=uid<=b for a,b in ranges)
                assert routed == (name=="com.android.shell"), (name,uid,ranges)
                print(f"kernel VPN route {name} uid={uid} routed={routed}",flush=True)
            (out/"ip-rules.txt").write_text(rules, encoding="utf-8")
            package = run("shell","pm","path","com.smartvpn.acceptance").decode().strip()
            assert package.startswith("package:/data/app/")
            output = run("shell","env","CLASSPATH="+package[8:],"app_process","/",
                        "com.smartvpn.acceptance.TunnelProbe","chain")
            print(output.decode().strip(),flush=True)
            (out/"chain-connected.png").write_bytes(run("exec-out","screencap","-p"))
            (out/"vpn.txt").write_bytes(run("shell","dumpsys","vpn_management"))
        if line.strip()=="CHAIN PROBES DONE":
            done = True
    assert done, "chain scenario failed"
assert b"tun0" not in run("shell","ls","/sys/class/net").split()
