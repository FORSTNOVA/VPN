"""Strict source-only Flutter staging and APK archive inventory; no device access."""
import argparse, hashlib, json, os, shutil, zipfile
from pathlib import Path
SKIP_DIRS={'.git','.dart_tool','.gradle','.kotlin','.cxx','.idea','build','outputs','jnilibs','keys','keystore','keystores','credentials'}
SKIP_FILES={'local.properties','key.properties','keystore.properties','signing.properties','gradle.properties','.flutter-plugins','.flutter-plugins-dependencies','google-services.json','credentials','credentials.json'}
SKIP_EXT={'.apk','.aab','.so','.dll','.jks','.keystore','.p12','.pfx','.pem','.key','.iml'}

def reject_reparse(p):
    p=Path(p).absolute()
    for q in (p,*p.parents):
        if q.exists() and (q.is_symlink() or q.is_junction()):raise ValueError('reparse input/output: '+str(q))

def guard_destination(dst, inputs):
    dst=Path(dst).absolute();reject_reparse(dst)
    for source in inputs:
        source=Path(source).absolute();reject_reparse(source)
        if dst==source or source in dst.parents:raise ValueError('destination within protected input')
    if dst.exists():raise ValueError('destination must be new')

def excluded(name):
    n=name.lower()
    return n in SKIP_FILES or n=='.env' or n.startswith('.env.') or n.startswith('credentials.') or Path(n).suffix in SKIP_EXT

def digest(p):
    with open(p,'rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()

def source_inventory(src):
    src=Path(src).absolute();reject_reparse(src);result={}
    for base,dirs,files in os.walk(src,followlinks=False):
        dirs[:]=[d for d in dirs if d.lower() not in SKIP_DIRS]
        for d in dirs:reject_reparse(Path(base)/d)
        for f in files:
            p=Path(base)/f
            if excluded(f):continue
            reject_reparse(p)
            result[p.relative_to(src).as_posix()]={'source':str(p),'sha256':digest(p)}
    return result

def snapshot(source,overlay):
    extra={}
    for n in ('package_config.json','package_graph.json'):
        p=Path(source)/'.dart_tool'/n;reject_reparse(p);extra[n]={'source':str(p.absolute()),'sha256':digest(p)}
    return {'original':source_inventory(source),'overlay':source_inventory(overlay),'resolution':extra}

def verify_snapshot(snap,source,overlay):
    if snapshot(source,overlay)!=snap:raise ValueError('source or overlay changed since pre-copy snapshot')

def effective(snap):
    return snap['original']|snap['overlay']

def gradle_bytes(snap,key):
    original=Path(effective(snap)['android/app/build.gradle.kts']['source']).read_bytes()
    key=Path(key).absolute().as_posix()
    if any(c in key for c in ('"','\\','\n','\r','$')):raise ValueError('unsafe key path')
    patch='\nandroid { signingConfigs.getByName("debug") { storeFile = file("'+key+'"); storePassword = "android"; keyAlias = "androiddebugkey"; keyPassword = "android" } }\n'
    patch+='android { packaging { jniLibs { keepDebugSymbols += setOf("**/libsmartvpn*.so") } }; buildTypes.getByName("release") { isMinifyEnabled = false; isShrinkResources = false } }\n'
    return original+patch.encode()

def main():
    ap=argparse.ArgumentParser();sub=ap.add_subparsers(dest='op',required=True)
    c=sub.add_parser('copy');c.add_argument('source');c.add_argument('overlay');c.add_argument('destination')
    v=sub.add_parser('inspect');v.add_argument('apk');v.add_argument('native');v.add_argument('mode',choices=['go','rust']);v.add_argument('output')
    for name in ('copy','audit-source','patch-gradle'):
        if name=='copy':continue
        h=sub.add_parser(name);h.add_argument('source');h.add_argument('overlay');h.add_argument('staged');h.add_argument('snapshot');h.add_argument('key')
    c.add_argument('snapshot');c.add_argument('--protect',action='append',default=[])
    a=ap.parse_args()
    if a.op=='copy':
        src=Path(a.source).absolute();overlay=Path(a.overlay).absolute();dst=Path(a.destination).absolute()
        guard_destination(dst,[src,overlay,*a.protect])
        snap=snapshot(src,overlay)
        Path(a.snapshot).write_text(json.dumps(snap,indent=2),encoding='utf-8')
        for rel,entry in effective(snap).items():
            q=dst/rel;q.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(entry['source'],q)
            if digest(q)!=entry['sha256']:raise ValueError('copy changed: '+rel)
        verify_snapshot(snap,src,overlay)
        print('Source-only staging:',dst);return
    if a.op in ('audit-source','patch-gradle'):
        snap=json.loads(Path(a.snapshot).read_text(encoding='utf-8'));verify_snapshot(snap,a.source,a.overlay)
        expected=gradle_bytes(snap,a.key)
        if a.op=='patch-gradle':
            p=Path(a.staged)/'android/app/build.gradle.kts'
            if digest(p)!=effective(snap)['android/app/build.gradle.kts']['sha256']:raise ValueError('unexpected pre-patch Gradle changes')
            p.write_bytes(expected);return
        for rel,entry in effective(snap).items():
            want=hashlib.sha256(expected).hexdigest() if rel=='android/app/build.gradle.kts' else entry['sha256']
            if digest(Path(a.staged)/rel)!=want:raise ValueError('unexpected staged change: '+rel)
        print('Pre-copy original + overlay snapshot and exact staging patch verified');return
    apk=Path(a.apk);native=Path(a.native)
    abi=native.name
    if abi not in {"arm64-v8a","x86_64"}:raise ValueError("unexpected staged ABI")
    with zipfile.ZipFile(apk) as z:
        names=z.namelist()
        if 'AndroidManifest.xml' not in names or not any(n.startswith('classes') and n.endswith('.dex') for n in names):raise ValueError('missing manifest/classes')
        libs=[n for n in names if n.startswith('lib/') and n.endswith('.so')]
        if any(not n.startswith(f'lib/{abi}/') for n in libs):raise ValueError('unexpected ABI')
        for library in ("libflutter.so", "libapp.so"):
            if f"lib/{abi}/{library}" not in libs:raise ValueError("missing Flutter library: "+library)
        expected=['libsmartvpn.so']+(['libsmartvpn_core.so','libsmartvpn_host.so'] if a.mode=='rust' else [])
        store='libsmartvpn_store.so'
        if a.mode=='rust' and (native/store).is_file():expected.append(store)
        elif f'lib/{abi}/{store}' in libs:raise ValueError('unstaged Rust store in APK')
        for f in expected:
            data=z.read(f'lib/{abi}/'+f)
            if hashlib.sha256(data).hexdigest()!=digest(native/f):raise ValueError('packaged native bytes differ: '+f)
        if a.mode=='go' and any(n.endswith(('/libsmartvpn_core.so','/libsmartvpn_host.so')) for n in libs):raise ValueError('Go APK unexpectedly contains Rust core')
        dex=b''.join(z.read(n) for n in names if n.startswith('classes') and n.endswith('.dex'))
        for marker in [b'com/smartvpn/client/CoreBridge',b'com/smartvpn/client/RustCoreStartup',b'configureRustCore']:
            if marker not in dex:raise ValueError('missing DEX class/method marker '+repr(marker))
        report={'apk':str(apk),'sha256':digest(apk),'bytes':apk.stat().st_size,'mode':a.mode,'nativeLibraries':libs,'entries':len(names),'manifestPresent':True,'classMarkersPresent':True,'runtimeValidated':False}
        Path(a.output).write_text(json.dumps(report,indent=2),encoding='utf-8');print(json.dumps(report,indent=2))
if __name__=='__main__':main()
