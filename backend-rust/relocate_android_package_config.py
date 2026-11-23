# Reuses an existing resolved graph; never invokes pub or resolves dependencies.
import json,re,sys,hashlib
from pathlib import Path
from urllib.parse import urlparse,unquote
from urllib.request import url2pathname
old,front,old_sdk,new_sdk,pub=map(Path,sys.argv[1:])
config_file=old/'.dart_tool/package_config.json'
config=json.loads(config_file.read_text(encoding='utf-8-sig'))
lock=(old/'pubspec.lock').read_text(encoding='utf-8-sig')
if (front/'pubspec.lock').read_bytes()!=(old/'pubspec.lock').read_bytes():raise ValueError('lock differs')
for p in config['packages']:
    u=urlparse(p['rootUri'])
    if u.scheme and u.scheme!='file':raise ValueError('non-local package root')
    source=Path(url2pathname(unquote(u.path))) if u.scheme else config_file.parent/Path(p['rootUri'])
    source=source.resolve()
    if not source.is_dir():raise ValueError('uncached root: '+str(source))
    if source==old.resolve():dest=front
    elif old_sdk.resolve() in source.parents:dest=new_sdk/source.relative_to(old_sdk.resolve())
    elif pub.resolve() in source.parents:
        dest=source
        m=re.search(r'^  '+re.escape(p['name'])+r':\n(.*?)(?=^  \w|\Z)',lock,re.M|re.S)
        if not m:raise ValueError('package not in lock')
        v=re.search(r'^    version: "?([^"\n]+)',m.group(1),re.M)
        if not v or source.name!=p['name']+'-'+v.group(1):raise ValueError('cached version differs from lock')
    else:raise ValueError('root outside reviewed SDK/cache/app: '+str(source))
    if not dest.is_dir():raise ValueError('relocated root missing')
    p['rootUri']=dest.resolve().as_uri()
config['flutterRoot']=new_sdk.resolve().as_uri();config['generator']='existing-local-config-relocation-no-pub'
out=front/'.dart_tool/package_config.json';out.parent.mkdir(exist_ok=True)
out.write_text(json.dumps(config,indent=2),encoding='utf-8')
print('Local existing resolution relocated:',len(config['packages']),'packages; no dependency resolution performed')

graph_file=old/'.dart_tool/package_graph.json'
graph=json.loads(graph_file.read_text(encoding='utf-8-sig'))
names={p['name'] for p in config['packages']}
if {p['name'] for p in graph['packages']}!=names:raise ValueError('package graph/config names differ')
for entry in graph['packages']:
    for dep in entry.get('dependencies',[])+entry.get('devDependencies',[]):
        if dep not in names:raise ValueError('unresolved graph edge')
    m=re.search(r'^  '+re.escape(entry['name'])+r':\n(.*?)(?=^  \w|\Z)',lock,re.M|re.S)
    if m:
        v=re.search(r'^    version: "?([^"\n]+)',m.group(1),re.M)
        if not v or v.group(1)!=entry['version']:raise ValueError('graph version differs from lock')
(front/'.dart_tool/package_graph.json').write_text(json.dumps(graph,indent=2),encoding='utf-8')
print('Existing package graph verified and copied; no resolution invoked')
