"""Verify the ABI and backend of the Flutter APK before publishing it."""
from pathlib import Path
import sys
import zipfile


def verify(apk, mode, abis):
    required = (
        {"libsmartvpn_core.so", "libsmartvpn_host.so", "libsmartvpn_store.so", "libsmartvpn_engine.so"}
        if mode == "Rust"
        else {"libsmartvpn.so"}
    )
    with zipfile.ZipFile(apk) as source:
        actual = {path for path in source.namelist() if path.startswith("lib/") and path.endswith(".so")}
        if {path.split("/")[1] for path in actual} != set(abis):
            raise ValueError("APK ABI set does not match requested platforms")
        for abi in abis:
            libs = {Path(path).name for path in actual if path.startswith(f"lib/{abi}/")}
            if {name for name in libs if name.startswith("libsmartvpn")} != required:
                raise ValueError(f"wrong SmartVPN libraries for {mode} {abi}")
            for name in required | {"libapp.so", "libflutter.so"}:
                with source.open(f"lib/{abi}/{name}") as lib:
                    header = lib.read(20)
                machine = {"x86_64": 62, "arm64-v8a": 183}[abi]
                if header[:6] != b"\x7fELF\x02\x01" or int.from_bytes(header[18:20], "little") != machine:
                    raise ValueError(f"wrong ELF architecture for {abi}/{name}")


if __name__ == "__main__":
    verify(sys.argv[1], sys.argv[2], sys.argv[3].split(","))
