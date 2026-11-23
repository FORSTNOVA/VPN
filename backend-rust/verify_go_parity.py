"""Refresh offline compatibility fixtures from the existing Go implementation.
Does not import profiles, run services, or open network sockets.
Usage: python verify_go_parity.py <SmartVPN repo> <go.exe>
"""
import hashlib, ipaddress, os, pathlib, random, struct, subprocess, sys, tempfile
root = pathlib.Path(__file__).resolve().parent
repo = pathlib.Path(sys.argv[1]); go = sys.argv[2]
source = (repo/'backend/stun.go').read_text(encoding='utf-8-sig')
# Copy only pure functions and constants, excluding all probe/network functions.
constants = source[source.index('const ('):source.index('// The servers')]
parser = source[source.index('func parseSTUNAddressGo('):source.index('// probeUDPEgress')]
pure = constants + parser
program = 'package main\nimport("encoding/binary";"errors";"net";"strconv";"bufio";"os";"encoding/hex";"fmt")\n'+pure+'''\nfunc main(){s:=bufio.NewScanner(os.Stdin);for s.Scan(){b,e:=hex.DecodeString(s.Text());if e!=nil{panic(e)};v,e:=parseSTUNAddressGo(b);if e!=nil{fmt.Println("ERR:"+e.Error())}else{fmt.Println(v)}};if e:=s.Err();e!=nil{panic(e)}}'''
tx=bytes(range(12)); cookie=bytes.fromhex('2112a442')
def attr(ip, port, xor=True):
    raw=ipaddress.ip_address(ip).packed; family=1 if len(raw)==4 else 2
    if xor: raw=bytes(a^b for a,b in zip(raw,cookie+tx)); port ^= 0x2112
    value=bytes([0,family])+struct.pack('!H',port)+raw
    return struct.pack('!HH',32 if xor else 1,len(value))+value
def packet(attrs): return struct.pack('!HHI',0x101,len(attrs),0x2112a442)+tx+attrs
vectors=[b'',b'\x01',bytes(20)]
for ip in ['203.0.113.7','198.51.100.9','2001:db8::1234','::1','::ffff:192.0.2.1']:
    for port in [0,1,443,54321,65535]:
        for xor in [False,True]: vectors.append(packet(attr(ip,port,xor)))
valid=packet(attr('203.0.113.7',54321));vectors += [valid[:n] for n in range(len(valid))]
vectors += [packet(attr('198.51.100.9',1,False)+attr('203.0.113.7',443)),packet(struct.pack('!HH',0x7777,1)+b'x\0\0\0'+attr('::1',443)),packet(attr('203.0.113.7',443)+b'\0'),packet(attr('198.51.100.9',1,False)+struct.pack('!HH',32,2)+b'xx')]
for n in [0,1,7,8,11,12,100,65535]: vectors.append(valid[:2]+struct.pack('!H',n)+valid[4:])
rng=random.Random(42)
for n in range(100):
    v=bytearray(valid)
    for _ in range(3):v[rng.randrange(len(v))]=rng.randrange(256)
    vectors.append(bytes(v))
with tempfile.TemporaryDirectory(prefix='smartvpn-go-oracle-') as tmp:
    path=pathlib.Path(tmp)/'main.go';path.write_text(program,encoding='utf-8')
    env=os.environ.copy();env.update(GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off',GOFLAGS='',GOCACHE=str(pathlib.Path(tmp)/'cache'))
    result=subprocess.run([go,'run',str(path)],input='\n'.join(v.hex() for v in vectors)+'\n',text=True,encoding='utf-8',capture_output=True,env=env,check=True)
answers=result.stdout.splitlines()
assert len(answers)==len(vectors)
(root/'tests/stun-fixtures.tsv').write_text(''.join(v.hex()+'\t'+a+'\n' for v,a in zip(vectors,answers)),encoding='utf-8')
(root/'tests/fixture-source.txt').write_text('backend/stun.go SHA256 '+hashlib.sha256(source.encode()).hexdigest()+'\n'+str(len(vectors))+' deterministic offline Go oracle vectors\n',encoding='utf-8')
print(f'Generated {len(vectors)} Go-oracle fixtures')
