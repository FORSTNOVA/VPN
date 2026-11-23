"""Offline actual-Go traffic differential and cross-read test. Synthetic temp DBs only."""
import pathlib,sys,os,tempfile,subprocess,shutil,json,hashlib,sqlite3,random
from contextlib import closing
root=pathlib.Path(__file__).resolve().parent
backend=pathlib.Path(sys.argv[1]);go=sys.argv[2];cargo_home=sys.argv[3]
source=(backend/'traffic_history.go').read_text(encoding='utf-8-sig')
def between(a,b):return source[source.index(a):source.index(b)]
schema=source.split('const trafficHistorySchema = `',1)[1].split('`',1)[0]
types=between('type trafficHistoryRow struct','func historyDate(')
if (backend/'store_traffic.go').exists():
 storage=(backend/'store_traffic.go').read_text(encoding='utf-8-sig')
 receiver='goStoreBackend' if 'func (s *goStoreBackend) recordTraffic(' in storage else 'store'
 methods=storage[storage.index('func (s *'+receiver+') recordTraffic('):]+types
 # Compile actual Go SQL methods against this standalone harness's one DB.
 # Never include the facade, backend selection, FFI or app lifecycle.
 methods=methods.replace('(s *goStoreBackend)', '(s *store)')
else:
 methods=between('func (s *store) recordTraffic(', 'func (a *app) startTrafficHistoryLocked(')+types+between('func (s *store) readTrafficHistory(', 'func (a *app) trafficHistory(')
counter_end='func (s *store) recordTraffic(' if 'func (s *store) recordTraffic(' in source else 'func (a *app) startTrafficHistoryLocked('
header='''package main
import("crypto/sha256";"database/sql";"fmt";"time";"encoding/json";"os";_ "modernc.org/sqlite")
type store struct{db *sql.DB}
type metadata struct{AppKey,App,Target,Network,Chain string}
type conn struct{ID,Start string;Upload,Download int64;Metadata metadata;Chains []string}
type kernelTraffic struct{Connections []conn}
func trafficApp(m metadata)(string,string){return m.AppKey,m.App}
func connectionTarget(m metadata)string{return m.Target}
func connectionChain(c []string)string{return c[0]}
func counterDelta(c,p int64)int64{return counterDeltaGo(c,p)}
'''
main=r'''
type Sample struct{HasID bool `json:"has_id"`;Key string `json:"connection_key"`;AppKey string `json:"app_key"`;App,Target,Network,Chain string;Upload,Download int64;ID,Start string}
type Step struct{Session,Bucket string;Now int64;Samples []Sample}
type Input struct{Steps []Step;Queries []json.RawMessage}
func must(e error){if e!=nil{panic(e)}}
func main(){var in Input;must(json.NewDecoder(os.Stdin).Decode(&in));db,e:=sql.Open("sqlite",os.Args[1]);must(e);defer db.Close();db.SetMaxOpenConns(1);_,e=db.Exec(schema);must(e);s:=&store{db}
for _,step:=range in.Steps {var snapshot kernelTraffic;for _,v:=range step.Samples {id:=v.ID;if !v.HasID{id=""};snapshot.Connections=append(snapshot.Connections,conn{ID:id,Start:v.Start,Upload:v.Upload,Download:v.Download,Metadata:metadata{v.AppKey,v.App,v.Target,v.Network,v.Chain},Chains:[]string{v.Chain}})};now:=time.Unix(step.Now,0).UTC();if now.Format("2006-01-02T15")!=step.Bucket {panic("fixture bucket mismatch")};must(s.recordTraffic(step.Session,snapshot,now))}
out:=[]trafficHistoryReport{};for _,raw:=range in.Queries {var q []json.RawMessage;must(json.Unmarshal(raw,&q));var r trafficHistoryReport;var key string;must(json.Unmarshal(q[0],&r));must(json.Unmarshal(q[1],&key));r,e=s.readTrafficHistory(r,key);must(e);out=append(out,r)};must(json.NewEncoder(os.Stdout).Encode(out))}
'''
rng=random.Random(731);steps=[]
from datetime import datetime,timezone
base=int(datetime(2026,11,16,tzinfo=timezone.utc).timestamp())
for n in range(60):
 session='session-'+str(n//20);now=base+(n//4)*3600+n%4;samples=[]
 for j in range(5):
  ident='conn-'+str(j);start='start-'+str(j%2);key=hashlib.sha256((session+'\0'+ident+'\0'+start).encode()).hexdigest()
  samples.append(dict(has_id=(j!=4 or n%2==0),connection_key=key,ID=ident,Start=start,app_key='app:'+str(j%3),app='APP'+str(j%3),target='target-'+str(n%3),network='tcp' if n%2 else 'udp',chain='synthetic',upload=rng.choice([-1,0,n*17+j,100]),download=rng.randrange(200)))
 steps.append(dict(session=session,bucket=datetime.fromtimestamp(now,timezone.utc).strftime('%Y-%m-%dT%H'),now=now,samples=samples))
 if n%7==0:steps.append(steps[-1])
queries=[]
for group in ['app','connection','other']:
 for date in ['2026','2026-11','2026-11-16','2026-11-16T03','2025','2026-%']:
  for key in ['','app:1','absent']:
   for offset,limit in [(0,100),(0,1),(1,2),(999,5),(-1,-1),(0,0)]:
    queries.append([dict(period='day',date=date,group=group,timezone='UTC +00:00',uploadTotal=0,downloadTotal=0,connectionCount=0,rowCount=0,offset=offset,limit=limit,hasMore=False,rows=[],samplingError='synthetic-status'),key])
with tempfile.TemporaryDirectory(prefix='smartvpn-traffic-oracle-') as tmp:
 tmp=pathlib.Path(tmp);h=tmp/'go';h.mkdir()
 for name in ['go.mod','go.sum']:shutil.copyfile(backend/name,h/name)
 (h/'main.go').write_text(header+'\nconst schema=`'+schema+'`\n'+between('func counterDeltaGo(', counter_end)+methods+main,encoding='utf-8')
 env=os.environ.copy();env.update(GOPROXY='off',GOSUMDB='off',GOTOOLCHAIN='local',GOWORK='off',GOFLAGS='-mod=readonly -buildvcs=false',GOPATH=r'D:\codebuddythink\vpn\.tools\gopath',CARGO_HOME=cargo_home,CARGO_INCREMENTAL='0')
 subprocess.run([go,'build','-o',str(tmp/'oracle.exe'),'.'],cwd=h,env=env,check=True)
 subprocess.run(['cargo','build','--offline','--locked','--manifest-path',str(root/'Cargo.toml'),'--example','traffic_fixture'],cwd='C:\\',env=env,check=True)
 rust=root/'target'/'debug'/'examples'/'traffic_fixture.exe'
 def run(exe,db,ss):
  p=subprocess.run([str(exe),str(db)],input=json.dumps(dict(steps=ss,queries=queries)),text=True,encoding='utf-8',capture_output=True,env=env,check=True);return json.loads(p.stdout)
 a=run(tmp/'oracle.exe',tmp/'go.db',steps);b=run(rust,tmp/'rust.db',steps);assert a==b,'report mismatch'
 for table in ['traffic_hours','traffic_checkpoints']:
  with closing(sqlite3.connect(tmp/'go.db')) as x,closing(sqlite3.connect(tmp/'rust.db')) as y:
   assert sorted(x.execute('SELECT * FROM '+table).fetchall())==sorted(y.execute('SELECT * FROM '+table).fetchall()),table+' mismatch'
 assert run(rust,tmp/'go.db',[])==a,'Rust reading Go DB mismatch'
 assert run(tmp/'oracle.exe',tmp/'rust.db',[])==b,'Go reading Rust DB mismatch'
 # Retry using the other implementation; checkpoint identity prevents double counts.
 expected=run(tmp/'oracle.exe',tmp/'rust.db',[steps[-1]])
 actual=run(rust,tmp/'go.db',[steps[-1]])
 assert actual==expected,'cross-writer checkpoint retry mismatch'
 print(f'PASS: {len(steps)} traffic snapshots, {len(queries)} query cases, identical raw tables and bidirectional Go/Rust reads')
