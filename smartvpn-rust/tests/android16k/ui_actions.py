"""Perform and inspect accessibility actions on the AVD used by the acceptance suite."""
import re
import runpy
import sys
import time
from pathlib import Path

serial, dest, *actions = sys.argv[1:]
sys.argv = [sys.argv[0], serial, dest]
ui = runpy.run_path(str(Path(__file__).with_name("screens.py")))
run, tree, tap, capture, scroll = [ui[key] for key in ("run", "tree", "tap", "capture", "scroll_to")]
for action in actions:
    kind, _, value = action.partition("=")
    if kind == "tap":
        tap(value.replace("\\n","\n"))
    elif kind == "choose":
        bounds = next(b for label,b in tree() if label == value)
        x,y,u,v = map(int,re.findall(r"\d+",bounds))
        run("shell","input","tap",str(u-35),str((y+v)//2))
    elif kind == "text":
        run("shell","input","text",value)
    elif kind == "back":
        run("shell","input","keyevent","4")
    elif kind == "scroll":
        scroll(value)
    elif kind == "capture":
        name, label = value.split("|")
        capture(name,label)
    elif kind == "labels":
        print(tree(),flush=True)
    else:
        raise ValueError(action)
    time.sleep(.3)
