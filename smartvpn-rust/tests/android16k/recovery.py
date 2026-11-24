"""Run the recovery acceptance case on a fresh copy of Medium_Phone."""
import contextlib
import http.client
import hashlib
import os
import queue
import shutil
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
from http.server import ThreadingHTTPServer
from pathlib import Path
from mock import Handler, Proxy, ui_finished

ROOT = Path(__file__).resolve().parents[2]
SDK = Path(os.environ["LOCALAPPDATA"], "Android/sdk")
ADB = SDK / "platform-tools/adb.exe"
PORT = 5584
SERIAL = f"emulator-{PORT}"
PKG = "com.smartvpn.client"

def command(*args, timeout=60):
    return subprocess.check_output(args, text=True, timeout=timeout, encoding="utf8", errors="replace")

def adb(*args, timeout=60):
    return command(ADB, "-s", SERIAL, *args, timeout=timeout).strip()

def tls_fixture(work):
    import datetime
    import ssl
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import rsa
    name = x509.Name([x509.NameAttribute(x509.NameOID.COMMON_NAME, "recovery.example")])
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    now = datetime.datetime.now(datetime.timezone.utc)
    cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
            .public_key(key.public_key()).serial_number(x509.random_serial_number())
            .not_valid_before(now-datetime.timedelta(days=1)).not_valid_after(now+datetime.timedelta(days=1))
            .add_extension(x509.SubjectAlternativeName([x509.DNSName("recovery.example")]),critical=False)
            .sign(key,hashes.SHA256()))
    ca=work/"recovery.crt"
    ca.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    private=work/"server.key"
    private.write_bytes(key.private_bytes(serialization.Encoding.PEM,serialization.PrivateFormat.PKCS8,
                                          serialization.NoEncryption()))
    ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(ca,private)
    adb("push",str(ca),"/data/local/tmp/recovery.crt")
    return ctx

def exercise(apk, helper, stack):
    for device_port, factory, handler in [(38471, ThreadingHTTPServer, Handler),
                                          (38472, socketserver.ThreadingTCPServer, Proxy)]:
        server = stack.enter_context(factory(("127.0.0.1", 0), handler))
        threading.Thread(target=server.serve_forever, daemon=True).start()
        stack.callback(server.shutdown)
        if device_port == 38472:
            server.recovery_tls = tls_fixture(helper.parent)
        port = server.server_address[1]
        adb("reverse", f"tcp:{device_port}", f"tcp:{port}")
        if device_port == 38471:
            http_port = port
    adb("shell","settings","put","global","mobile_data_always_on","1")
    with apk.open("rb") as file:
        print("Installing x86_64 APK SHA-256",hashlib.file_digest(file,"sha256").hexdigest(),flush=True)
    adb("install", str(apk))
    adb("install", str(helper))
    adb("shell", "appops", "set", PKG, "ACTIVATE_VPN", "allow")
    adb("push", str(helper), "/data/local/tmp/recovery-helper.apk")
    runner = subprocess.Popen([ADB,"-s",SERIAL,"shell","am","instrument","-w","-r","-e",
                               "scenario","recovery","com.smartvpn.acceptance/.Acceptance"],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    lines = queue.Queue()
    def read():
        for line in runner.stdout:
            lines.put(line.strip())
        lines.put(None)
    threading.Thread(target=read,daemon=True).start()
    proof = False
    passes = 0
    try:
        while True:
            line = lines.get(timeout=60)
            if line is None:
                break
            if "Underlying Wi-Fi=" in line:
                print(line,flush=True)
                assert ("Wi-Fi=true" if passes==0 else "cellular=true") in line, "unexpected starting transport"
            if "READY FOR UPLINK SWITCH" in line:
                ui_finished.clear()
                switched=time.monotonic()
                action="disable" if passes==0 else "enable"
                adb("shell","svc","wifi",action)
                print(f"Wi-Fi {action} requested in fresh guest",flush=True)
            if "STALE FLOW CLOSED SAME TUNNEL" in line:
                elapsed=time.monotonic()-switched
                # Radio acquisition and VPN recovery have separate start times.
                entries=adb("logcat","-d","-v","monotonic","-s","SmartVPN:I").splitlines()
                def timestamp(marker):
                    return float(next(line for line in reversed(entries) if marker in line).split()[0])
                recovery=timestamp("Uplink recovery completed")-timestamp("New physical uplink available")
                close_time=timestamp("Held fixture socket closed")-timestamp("New physical uplink available")
                assert 0 <= close_time < 2, f"old socket still open for {close_time:.2f}s after switch"
                print(f"Physical network callback through held socket EOF: {close_time:.3f}s",flush=True)
                assert 0 <= recovery < 2, f"controller recovery took {recovery:.2f}s"
                print(f"Physical network callback through controller reset: {recovery:.3f}s",flush=True)
                print(f"Old socket closed with same controller and TUN fd in {elapsed:.2f}s",flush=True)
                result=adb("shell","CLASSPATH=/data/local/tmp/recovery-helper.apk","app_process","/",
                           "com.smartvpn.acceptance.TunnelProbe","recovery",timeout=10)
                assert "Post-switch TUN HTTPS 200" in result and "TUN DNS fake-ip=" in result,result
                print(result,flush=True)
                conn=http.client.HTTPConnection("127.0.0.1",http_port,timeout=3)
                conn.request("POST","/test/finish")
                assert conn.getresponse().status==204
                conn.close()
                passes+=1
            if "RECOVERY PASSED" in line:
                proof=True
            if "FAIL " in line:
                messages=adb("logcat","-d","-s","SmartVPN:I").splitlines()
                for message in messages:
                    if "Uplink recovery" in message: print(message,flush=True)
                raise AssertionError(line)
        assert proof and passes==2 and runner.wait(timeout=5)==0
        print("Recovery acceptance passed",flush=True)
    finally:
        if runner.poll() is None:
            runner.kill()
        runner.wait()
        runner.stdout.close()

def main():
    baseline=command(ADB,"devices")
    print("ADB baseline", baseline,flush=True)
    assert SERIAL not in baseline.split(), "serial in use"
    for port in (PORT,PORT+1):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1",port))
    version=(ROOT/"VERSION").read_text().strip().split("+")[0]
    apk=ROOT/f"dist/SmartVPN-{version}-rust-pure-x86_64.apk"
    assert apk.is_file()
    root=ROOT/".build"
    with tempfile.TemporaryDirectory(dir=root,prefix="emulator-") as temp:
        work=Path(temp)
        (work/"baseline.txt").write_text(baseline)
        command("pwsh","-File",str(Path(__file__).with_name("build-test.ps1")),"-Work",str(work/"helper"))
        avd=work/"Medium_Phone.avd"
        avd.mkdir()
        shutil.copyfile(Path.home()/".android/avd/Medium_Phone.avd/config.ini",avd/"config.ini")
        (work/"Medium_Phone.ini").write_text(f"avd.ini.encoding=UTF-8\npath={avd}\n")
        args=[SDK/"emulator/emulator.exe","-avd","Medium_Phone","-port",str(PORT),"-no-window",
              "-no-audio","-no-snapshot","-gpu","swiftshader_indirect"]
        with open(os.devnull,"w") as log:
            guest=subprocess.Popen(args,env={**os.environ,"ANDROID_AVD_HOME":temp},stdout=log,stderr=log)
            owned=False
            try:
                adb("wait-for-device",timeout=150)
                assert guest.poll() is None and adb("emu","avd","name").splitlines()[0]=="Medium_Phone"
                owned=True
                until=time.monotonic()+150
                while adb("shell","getprop","sys.boot_completed") != "1":
                    assert time.monotonic()<until and guest.poll() is None,"guest boot deadline"
                    time.sleep(1)
                assert adb("shell","getconf","PAGESIZE")=="16384"
                with contextlib.ExitStack() as stack:
                    exercise(apk, work/"helper/test.apk", stack)
            finally:
                if owned and guest.poll() is None:
                    adb("emu","kill")
                if not owned and guest.poll() is None:
                    guest.terminate()
                guest.wait(timeout=45)
                until=time.monotonic()+30
                while SERIAL in command(ADB,"devices").split():
                    assert time.monotonic()<until,"emulator still registered"
                    time.sleep(.25)
                print("Fresh emulator shut down",flush=True)

if __name__=="__main__":
    main()
