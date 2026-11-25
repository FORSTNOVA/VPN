"""Check the actual archive and compare its kernel/service with the built inputs."""
import argparse
import gzip
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SCRATCH = Path(tempfile.gettempdir())


def verify(path, abi):
    apk_libs = {f"lib/{abi}/" + name for name in ("libapp.so", "libflutter.so", "libsmartvpn.so")}
    with zipfile.ZipFile(path) as z, tempfile.TemporaryDirectory(dir=SCRATCH) as work:
        names = [i.filename.replace("\\", "/") for i in z.infolist() if not i.is_dir()]
        assert len(names) == len(set(n.casefold() for n in names)), "duplicate archive member"
        members = {i.filename.replace("\\", "/"): i for i in z.infolist()}
        assert not any(Path(n).name.lower() in {"mihomo.exe", "gpl-3.0.txt"} for n in names)
        for name in names:
            data = z.read(members[name])
            if name.endswith("NOTICES.Z"):
                data = gzip.decompress(data)
            assert b"GPL-3.0" not in data, name
            assert b"GNU GENERAL PUBLIC LICENSE" not in data or b"Version 3, 29 June 2007" not in data, name
        native = [n for n in names if Path(n).suffix.lower() in {".exe", ".dll", ".so"}]
        for n in native:
            data = z.read(members[n])
            assert not any(marker in data for marker in (b"Go build ID:", b"\xff Go buildinf:", b".gopclntab", b"github.com/metacubex/mihomo")), n
        if path.suffix == ".apk":
            dex = [z.read(members[name]) for name in names if name.endswith(".dex")]
            assert any(b"notifyNetworkChanged" in data for data in dex), "missing Java uplink bridge"
            assert not any(Path(n).name.lower() == "go-sqlite3.txt" for n in names)
            assert z.read(members["assets/licenses/Apache-2.0-clash-rs.txt"]) == (ROOT / "vendor/clash-rs/LICENSE").read_bytes()
            assert {n for n in names if n.startswith("lib/")} == apk_libs, native
            lib = Path(work, "libsmartvpn.so")
            lib.write_bytes(z.read(members[f"lib/{abi}/libsmartvpn.so"]))
            sdk = Path(os.environ["ANDROID_HOME"]) if "ANDROID_HOME" in os.environ else Path(os.environ["LOCALAPPDATA"], "Android/sdk")
            subprocess.run([sdk / "build-tools/36.0.0/zipalign.exe", "-c", "-P", "16", "4", path],check=True)
            readelf = sdk / "ndk/27.2.12479018/toolchains/llvm/prebuilt/windows-x86_64/bin/llvm-readelf.exe"
            # Reproduce AGP stripReleaseDebugSymbols using the pinned NDK.
            stripped = Path(work, "expected-libsmartvpn.so")
            subprocess.run([readelf.with_name("llvm-strip.exe"), "--strip-unneeded", "-o", stripped,
                            ROOT / f"dist/android/{abi}/libsmartvpn.so"], check=True)
            assert lib.read_bytes() == stripped.read_bytes(), "packaged JNI differs from the stripped Rust build"
            subprocess.run([sys.executable, "-I", "-B", ROOT / "tests/verify_elf.py", lib, readelf, "--abi", abi], check=True)
            print(*sorted(apk_libs), sep="\n")
        else:
            required = {"smartvpn_windows.exe", "flutter_windows.dll", "data/app.so","data/icudtl.dat","portable.txt","LICENSES/Apache-2.0-clash-rs.txt","README-rust-pure.txt","smartvpn-service-current.exe"}
            assert required <= set(names), required - set(names)
            assert set(native) == {"smartvpn_windows.exe", "flutter_windows.dll", "smartvpn-service-current.exe", "wintun.dll", "data/app.so"}
            wintun_hash = hashlib.sha256(z.read(members["wintun.dll"])).hexdigest()
            assert wintun_hash == "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce", wintun_hash
            print(f"wintun.dll (x64 0.14.1) SHA-256 {wintun_hash}")
            assert z.read(members["LICENSES/Apache-2.0-clash-rs.txt"]) == (ROOT / "vendor/clash-rs/LICENSE").read_bytes()
            exe = Path(work, "smartvpn-service-current.exe")
            exe.write_bytes(z.read(members[exe.name]))
            assert exe.read_bytes() == (ROOT / "dist/windows-x64/smartvpn-rust-service.exe").read_bytes()
            probe = subprocess.run([ROOT.parent / ".tools/go/bin/go.exe", "version", "-m", exe], capture_output=True)
            assert probe.returncode != 0 and b"not a Go executable" in probe.stderr, (probe.returncode, probe.stdout, probe.stderr)
            print("go version -m: not a Go executable")
    print(f"{path.stat().st_size} bytes ({path.stat().st_size / 2**20:.6f} MiB) SHA-256 {hashlib.sha256(path.read_bytes()).hexdigest()}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("path", type=Path)
    parser.add_argument("--abi", choices=("arm64-v8a", "x86_64"), default="arm64-v8a")
    args = parser.parse_args()
    verify(args.path, args.abi)
