"""Create, edit, connect and remove a two-hop profile using Flutter's UI."""
import re
import runpy
import sys
import time
from pathlib import Path

ui = runpy.run_path(str(Path(__file__).with_name("screens.py")))
run, tree, tap, capture, out = [ui[key] for key in ("run", "tree", "tap", "capture", "out")]
run("shell","am","force-stop","com.smartvpn.client")
run("shell","am","start","-n","com.smartvpn.client/.MainActivity")
time.sleep(2)
tap("线路\nTab")
tap("搜索节点名称")
run("shell","input","text","Second")
assert any(label.startswith("Base64-Second\n") for label,_ in tree())
assert not any(label.startswith("YAML-First\n") for label,_ in tree())
capture("22-node-search","Base64-Second")
run("shell","input","keyevent","4")
tap("线路工具")
tap("代理链")
tap("新建代理链")
tap("代理链名称")
run("shell","input","text","QA-Two")
run("shell","input","keyevent","4")
for name in ["YAML-First","Base64-Second"]:
    tap("追加下一跳节点")
    bounds = next(b for label,b in tree() if label == name)
    x,y,u,v=map(int,re.findall(r"\d+",bounds))
    run("shell","input","tap",str(u-30),str((y+v)//2))
tap("测整链")
time.sleep(1)
capture("23-editor","转发顺序 (2/8)")
tap("保存")
tap("查看 / 编辑")
tap("QA-Two")
run("shell","input","keycombination","113","29")
run("shell","input","text","QA-Renamed")
run("shell","input","keyevent","4")
tap("保存")
assert any("QA-Renamed" in label for label,_ in tree())
tap("选用")
capture("24-selected","已选用")
run("shell","input","keyevent","4")
tap("总览\nTab")
tap("启动代理连接")
for _ in range(12):
    labels=[label for label,_ in tree()]
    if "Allow" in labels:
        bounds = next(b for label,b in tree() if label == "Allow")
        x,y,u,v=map(int,re.findall(r"\d+",bounds))
        run("shell","input","tap",str((x+u)//2),str((y+v)//2))
    if "断开代理连接" in labels:
        break
else:
    raise AssertionError(labels)
assert b"tun0" in run("shell","ls","/sys/class/net").split()
package=run("shell","pm","path","com.smartvpn.acceptance").decode().strip()
assert package.startswith("package:/data/app/")
print(run("shell","env","CLASSPATH="+package[8:],"app_process","/","com.smartvpn.acceptance.TunnelProbe","chain").decode(),flush=True)
capture("25-two-connected","断开代理连接")
tap("断开代理连接")
capture("26-two-disconnected","启动代理连接")
assert b"tun0" not in run("shell","ls","/sys/class/net").split()
tap("线路\nTab")
tap("线路工具")
tap("代理链")
tap("停用代理链")
tap("删除代理链")
tap("确认删除")
assert not any("QA-Renamed" in s for s,_ in tree())
capture("27-deleted","新建代理链")
print("UI CHAIN CRUD TUN DONE",flush=True)
