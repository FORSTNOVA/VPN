"""Inspect Flutter semantics and capture the six screens on the owned AVD."""
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import xml.etree.ElementTree as ET

serial, output = sys.argv[1:]
assert re.fullmatch(r"emulator-\d+", serial)
adb = [os.path.join(os.environ["LOCALAPPDATA"], "Android/sdk/platform-tools/adb.exe"), "-s", serial]

def run(*args):
    return subprocess.check_output([*adb, *args])

assert run("emu","avd","name").decode().split()[0] == "Medium_Phone"
out = Path(output)
out.mkdir(parents=True,exist_ok=True)
def tree():
    run("shell","uiautomator","dump","/data/local/tmp/smartvpn-ui.xml")
    xml = ET.fromstring(run("shell","cat","/data/local/tmp/smartvpn-ui.xml"))
    return [(n.get("content-desc") or n.get("text") or n.get("hint"),n.get("bounds"))
            for n in xml.iter("node") if n.get("content-desc") or n.get("text") or n.get("hint")]
def tap(prefix):
    for label,bounds in tree():
        if label.startswith(prefix):
            x,y,u,v=map(int,re.findall(r"\d+",bounds))
            run("shell","input","tap",str((x+u)//2),str((y+v)//2))
            time.sleep(.5)
            return
    raise RuntimeError("Missing UI control "+prefix)
def capture(name,expect):
    labels=[v for v,_ in tree()]
    assert any(expect in l for l in labels),(name,expect,labels[:3])
    for label in labels:
        assert not re.search(r"unknown route|Exception:|HTTP (?:404|500)|未能连接本地服务", label,re.I),(name,label)
    (out/(name+".png")).write_bytes(run("exec-out","screencap","-p"))
    print(name,len(labels),"labels",flush=True)

def scroll_to(prefix):
    for _ in range(14):
        if any(label.startswith(prefix) for label,_ in tree()):
            return
        run("shell","input","swipe","590","1880","590","770","400")
    raise RuntimeError("Control did not appear: "+prefix)

if __name__ == '__main__':
    for tab,name in [("总览","01-overview"),("线路","02-routes"),("订阅","03-subscriptions"),("流量","04-traffic")]:
        tap(tab+"\nTab");capture(name,tab)
    for label,name in [("保护策略","05-protection"),("工具","06-tools")]:
        tap("更多\nTab");tap(label+"\n");capture(name,label)
    
    tap("总览\nTab")
    capture("12-disconnected","启动代理连接")
    scroll_to("代理模式与分流")
    tap("代理模式与分流")
    scroll_to("更新直连列表")
    tap("更新直连列表")
    time.sleep(1.5)
    capture("07-direct-refresh","2 条地址段")
    tap("线路\nTab")
    tap("线路工具")
    tap("测全部节点")
    time.sleep(2)
    tap("线路工具")
    tap("代理链")
    tap("新建代理链")
    tap("追加下一跳节点")
    capture("08-chain-picker","搜索节点名或订阅分组")
    assert sum(bool(re.search(r"测单节点.*首次 \d+ ms.*连接后 \d+ ms", label)) for label,_ in tree())==2
    tap("Base64")
    capture("09-source-filter","Base64-Second")
    assert not any("YAML-First" in label for label,_ in tree())
    tap("搜索节点名或订阅分组")
    run("shell","input","text","Second")
    time.sleep(.5)
    assert any("Base64-Second" in label for label,_ in tree())
    assert not any("YAML-First" in label for label,_ in tree())
    capture("10-chain-search","Base64-Second")
