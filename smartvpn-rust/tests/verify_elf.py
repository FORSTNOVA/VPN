"""Validate the release artifact before publishing it into dist."""
import argparse
import io
import re
import struct
import subprocess
import zipfile
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("path", type=Path)
parser.add_argument("readelf", type=Path)
parser.add_argument("--abi", choices=("arm64-v8a", "x86_64"), default="arm64-v8a")
args = parser.parse_args()
path = args.path
data = path.read_bytes()
assert data[:7] == b"\x7fELF\x02\x01\x01"
assert struct.unpack_from("<HH", data, 16) == (3, {"arm64-v8a": 183, "x86_64": 62}[args.abi])
offset, = struct.unpack_from("<Q", data, 32)
stride, count = struct.unpack_from("<HH", data, 54)
alignments = []
for i in range(count):
    kind, _, file_off, addr, _, _, _, align = struct.unpack_from("<II6Q", data, offset+i*stride)
    if kind == 1:
        assert align >= 16384 and align & (align-1) == 0 and file_off % align == addr % align
        alignments.append(align)
assert alignments
symbols = subprocess.check_output([args.readelf, "--dyn-syms", "--wide", path], text=True)
exports = set(re.findall(r"FUNC\s+GLOBAL\s+DEFAULT\s+\d+\s+Java_com_smartvpn_client_CoreBridge_(\w+)", symbols))
assert exports == {"start", "stop", "setTunnel", "configureRustCore", "notifyNetworkChanged", "readGameBypass"}, exports
assert not re.search(r"mihomo|crosscall2|_cgo_|runtime\.|_cgoexp_|go:buildid|go\.itab|go\.buildid", symbols, re.I)
assert not re.search(r"\bUND\s+__(wrap|real)_socket\b", symbols)
assert not any(marker in data for marker in (b"Go build ID:", b"\xff Go buildinf:", b".gopclntab", b"github.com/metacubex/mihomo"))
archive = io.BytesIO()
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    z.writestr(f"lib/{args.abi}/libsmartvpn.so", data)
    compressed = z.infolist()[0].compress_size
print(f"ELF64 LE {args.abi} PT_LOAD={alignments} JNI={sorted(exports)} raw={len(data)} deflate={compressed} zip={len(archive.getvalue())}")
