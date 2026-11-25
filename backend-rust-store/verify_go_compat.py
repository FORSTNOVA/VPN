"""Offline Go -> Rust -> Go database compatibility test, synthetic temp DBs only.
Usage: python verify_go_compat.py <Go backend dir> <go.exe> <cargo-home>
No service process, VPN, user config or existing database is opened.
"""
import os,pathlib,shutil,subprocess,sys,tempfile
root=pathlib.Path(__file__).resolve().parent
backend=pathlib.Path(sys.argv[1]);go=sys.argv[2];cargo_home=sys.argv[3]
store=(backend/'store.go').read_text(encoding='utf-8-sig')
# Phase8 stores SQL on goStoreBackend. Extract only the pure facade wrappers;
# never compile the real facade/backend selector or Rust adapter into the oracle.
facade_helpers=''
if 'func openGoStoreBackend(' in store:
 facade=(backend/'store_backend.go').read_text(encoding='utf-8-sig')
 def facade_method(name):
  start=facade.index('func (s *store) '+name+'(')
  end=facade.find('\nfunc ',start+1)
  return facade[start:] if end<0 else facade[start:end]
 wrappers='\n'.join(facade_method(name) for name in ['upsertNodes','saveHealthParams','loadHealthParams'])
 wrappers=wrappers.replace('(s *store)', '(s *goStoreBackend)').replace('s.backend.upsertNodesAt(', 's.upsertNodesAt(')
 facade_helpers='package main\nimport("fmt";"time")\nfunc openStore(path string)(*goStoreBackend,error){return openGoStoreBackend(path)}\n'+wrappers

health=(backend/'health.go').read_text(encoding='utf-8-sig')
traffic=(backend/'traffic_history.go').read_text(encoding='utf-8-sig')
probe=(backend/'chain_probe.go').read_text(encoding='utf-8-sig')
health=health[health.index('type healthParams struct'):health.index('func newHealth(')]
if 'func (p healthParams) normalizedGo()' in health:
 health += '\nfunc (p healthParams) normalized() healthParams { return p.normalizedGo() }\n'
traffic=traffic.split('const trafficHistorySchema = `',1)[1].split('`',1)[0]
probe=probe[probe.index('type probeResult struct'):probe.index('func (a *app) currentProxyIndexLocked')]
main=r'''package main
import("encoding/json";"fmt";"os";"reflect";"time")
func must(err error){if err!=nil{panic(err)}}
func check(ok bool,msg string){if !ok{panic(msg)}}
func main(){mode,path:=os.Args[1],os.Args[2];if mode=="seed"{_,err:=os.Stat(path);check(os.IsNotExist(err),"seed refuses an existing path")};s,e:=openStore(path);must(e);defer s.Close()
if mode=="seed"{
 must(s.setValue("go-key","合成<&>"));must(s.upsertNodes([]proxyNode{{Name:"synthetic",Type:"ss",Network:"tcp",TLS:false}}))
 must(s.saveRegion(regionRecord{Name:"synthetic",ExitIP:"203.0.113.1",Country:"JP",Status:"verified",Sources:[]string{"go-a","go-b"},VerifiedAt:time.Unix(100,0)}))
 must(s.saveHealth(nodeHealth{Name:"synthetic",State:"HEALTHY",ConsecutiveSuccesses:1,LatencyMs:88,UpdatedAt:time.Unix(100,0)}))
 must(s.appendSwitchEvent(switchEvent{At:time.Unix(100,0),Group:"SmartVPN",Trigger:"go-seed"}))
 p:=probeResult{EntryAttempt:true,DiagnosticsVersion:1,EntryIP:"2001:db8::1",EntryFamily:"IPv6",Target:"https://www.gstatic.com/generate_204",WarmDelay:42,Hops:[]string{"入口<&>","出口\u2028"},Delay:123,Measured:1700000000123}
 // Interpret the literal Unicode line separator instead of six ASCII characters.
 p.Hops[1]="出口\u2028";p.Hops[1]="出口"+string(rune(0x2028))
 key,_:=json.Marshal(p.Hops);details,_:=json.Marshal(p);_,e=s.db.Exec("INSERT INTO probe_results(path,fingerprint,delay,error,measured,details) VALUES(?,?,?,?,?,?)",string(key),"go-fingerprint",p.Delay,p.Error,p.Measured,string(details));must(e)
 fmt.Println("Go synthetic fixture seeded");return
}
check(mode=="verify"||mode=="verify-fresh","unknown mode");v,found,e:=s.getValue("rust-key");must(e);check(found&&v=="written<&>","Rust KV not readable")
var count int;must(s.db.QueryRow("SELECT count(*) FROM probe_results").Scan(&count));check(count==1,"Go/Rust path escaping created duplicate rows")
var pathKey,signature,details string;var delay,measured int64;must(s.db.QueryRow("SELECT path,fingerprint,delay,measured,details FROM probe_results").Scan(&pathKey,&signature,&delay,&measured,&details));check(signature=="rust-fingerprint"&&delay==222&&measured==1700000000999,"probe columns mismatch")
var p probeResult;must(json.Unmarshal([]byte(details),&p));check(p.Delay==222&&p.WarmDelay==33&&p.Error=="故障<&>","Rust probe JSON unreadable");key,_:=json.Marshal(p.Hops);check(string(key)==pathKey,"path key mismatch")
regions,e:=s.loadRegions();must(e);check(regions["synthetic"].Country=="US"&&reflect.DeepEqual(regions["synthetic"].Sources,[]string{"x","y"}),"region mismatch")
health,e:=s.loadHealth();must(e);h:=health["synthetic"];check(h.State=="RECOVERY"&&h.LatencyMs==444&&h.LastSuccessAt.IsZero()&&h.CooldownUntil.IsZero(),"health mismatch")
events,e:=s.recentSwitchEvents(2);must(e);check(len(events)==2&&events[0].Trigger=="rust-second"&&events[1].Trigger=="rust-first","switch ordering mismatch")
params,e:=s.loadHealthParams();must(e);check(params.ConnectTimeoutMs==6000&&params.FailureThreshold==3,"params mismatch")
var proto,network string;var tls,updated int;must(s.db.QueryRow("SELECT protocol,network,tls,updated_at FROM nodes WHERE name='synthetic'").Scan(&proto,&network,&tls,&updated));check(proto=="trojan"&&network=="grpc"&&tls==1&&updated==333,"node mismatch")
fmt.Println("Go verified Rust schema, probe, KV, node, region, health, events and params")
}'''
with tempfile.TemporaryDirectory(prefix='smartvpn-store-go-compat-') as tmp:
 tmp=pathlib.Path(tmp); harness=tmp/'go';harness.mkdir()
 for name in ['go.mod','go.sum']:shutil.copyfile(backend/name,harness/name)
 (harness/'store.go').write_text(store,encoding='utf-8')
 if facade_helpers:(harness/'facade_helpers.go').write_text(facade_helpers,encoding='utf-8')
 (harness/'helpers.go').write_text('package main\nimport("encoding/json";"runtime";"time")\ntype proxyNode struct{Name,Type,Network string;TLS bool}\n'+health+'\nconst trafficHistorySchema = `'+traffic+'`\n'+probe,encoding='utf-8')
 (harness/'main.go').write_text(main,encoding='utf-8')
 env=os.environ.copy();env.update(GOPROXY='off',GOSUMDB='off',GOTOOLCHAIN='local',GOWORK='off',GOFLAGS='-mod=readonly -buildvcs=false',GOPATH=str(root.parent/'.tools'/'gopath'),GOCACHE=str(tmp/'go-cache'),CARGO_HOME=cargo_home,CARGO_INCREMENTAL='0')
 subprocess.run([go,'build','-o',str(tmp/'oracle.exe'),'.'],cwd=harness,env=env,check=True)
 for fresh in [False,True]:
  db=tmp/('fresh.db' if fresh else 'shared.db')
  if not fresh:subprocess.run([str(tmp/'oracle.exe'),'seed',str(db)],env=env,check=True)
  subprocess.run(['cargo','run','--offline','--locked','--manifest-path',str(root/'Cargo.toml'),'--example','fixture_bridge','--','create' if fresh else 'roundtrip',str(db)],cwd='C:\\',env=env,check=True)
  subprocess.run([str(tmp/'oracle.exe'),'verify-fresh' if fresh else 'verify',str(db)],env=env,check=True)
print('PASS: Go-created and Rust-created synthetic databases are cross-readable')
