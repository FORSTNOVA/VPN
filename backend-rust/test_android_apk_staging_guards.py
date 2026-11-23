"""Offline guards/provenance tests; creates only disposable temporary files."""
import importlib.util,json,tempfile,unittest
from pathlib import Path
spec=importlib.util.spec_from_file_location('staging',Path(__file__).with_name('android_apk_staging.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class Guards(unittest.TestCase):
 def test_all_input_descendants(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d)
   for name in ('source','overlay','flutter','gradle','native','pub','sdk','java'):
    p=root/name;p.mkdir()
    with self.assertRaises(ValueError):m.guard_destination(p/'nested',[p])
 def test_sensitive_filter(self):
  for name in ('.env','.env.local','.ENV.PROD','credentials','credentials.json','google-services.json','signing.properties','secret.pem'):
   self.assertTrue(m.excluded(name),name)
 def test_original_overwritten_snapshot_and_exact_patch(self):
  with tempfile.TemporaryDirectory() as d:
   root=Path(d);source=root/'source';overlay=root/'overlay'
   for p in (source,overlay):(p/'android/app').mkdir(parents=True)
   (source/'.dart_tool').mkdir()
   for name in ('package_config.json','package_graph.json'):(source/'.dart_tool'/name).write_text('{}')
   a=source/'android/app/build.gradle.kts';b=overlay/'android/app/build.gradle.kts'
   a.write_text('original');b.write_text('overlay')
   snap=m.snapshot(source,overlay)
   self.assertNotEqual(snap['original'],snap['overlay'])
   self.assertTrue(m.gradle_bytes(snap,root/'debug.keystore').startswith(b'overlay\nandroid'))
   a.write_text('changed hidden original')
   with self.assertRaises(ValueError):m.verify_snapshot(snap,source,overlay)
 def test_reparse_predicate(self):
  from unittest.mock import patch
  with patch.object(Path,'is_junction',return_value=True):
   with self.assertRaises(ValueError):m.reject_reparse(Path(__file__).parent)
 def test_apk_abi_and_flutter_engine(self):
  import sys,zipfile
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as d:
   root=Path(d); native=root/'x86_64';native.mkdir()
   (native/'libsmartvpn.so').write_bytes(b'jni')
   apk=root/'client.apk';report=root/'report.json'
   with patch.object(sys,'argv',['staging','inspect',str(apk),str(native),'go',str(report)]):
    with zipfile.ZipFile(apk,'w') as z:
     z.writestr('AndroidManifest.xml',b'xml')
     z.writestr('classes.dex',b'com/smartvpn/client/CoreBridge com/smartvpn/client/RustCoreStartup configureRustCore')
     z.writestr('lib/x86_64/libsmartvpn.so',b'jni')
     z.writestr('lib/x86_64/libapp.so',b'app')
    with self.assertRaisesRegex(ValueError,'missing Flutter library'):m.main()
    with zipfile.ZipFile(apk,'a') as z:z.writestr('lib/x86_64/libflutter.so',b'engine')
    m.main()
    self.assertEqual(json.loads(report.read_text())['mode'],'go')
    with zipfile.ZipFile(apk,'a') as z:z.writestr('lib/arm64-v8a/libflutter.so',b'other')
    with self.assertRaisesRegex(ValueError,'unexpected ABI'):m.main()
if __name__=='__main__':unittest.main()
