"""Run after installing both APKs and forwarding the fixture ports on the new guest."""
import http.client
import os
from pathlib import Path
import re
import subprocess
import sys

serial, *baseline = sys.argv[1:]
assert re.fullmatch(r"emulator-\d+", serial)
if baseline:
    assert serial not in Path(baseline[0]).read_text().split()
else:
    assert serial == "emulator-5554"
adb = [os.path.join(os.environ["LOCALAPPDATA"], "Android/sdk/platform-tools/adb.exe"), "-s", serial]


def run(*args):
    return subprocess.check_output([*adb, *args], text=True, timeout=25)


def post(path):
    conn = http.client.HTTPConnection("127.0.0.1", 38471, timeout=3)
    try:
        conn.request("POST", "/test/" + path)
        assert conn.getresponse().status == 204
    finally:
        conn.close()


assert run("emu", "avd", "name").split()[0] == "Medium_Phone"
post("reset")
probe = passed = False
with subprocess.Popen([*adb, "shell", "am", "instrument", "-w", "-e", "scenario", "rules",
                       "com.smartvpn.acceptance/.Acceptance"], stdout=subprocess.PIPE,
                      text=True, encoding="utf-8") as test:
    for line in test.stdout:
        print(line.rstrip(), flush=True)
        if "RULE TUN READY" in line:
            assert "tun0" in run("shell", "ls", "/sys/class/net").split()
            apk = run("shell", "pm", "path", "com.smartvpn.acceptance").strip().removeprefix("package:")
            print(run("shell", "env", "CLASSPATH=" + apk, "app_process", "/",
                      "com.smartvpn.acceptance.TunnelProbe", "rules"), flush=True)
            probe = True
            post("finish")
        if "RULE ACCEPTANCE PASSED" in line:
            passed = True
    assert test.wait() == 0 and probe and passed
