package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type connectionOracle struct {
	s  *connectionServiceState
	fx connectionServiceEffects
}

func (a *connectionOracle) call(q connectionEffect) connectionReply {
	r := a.fx.Call(q)
	if r.State != nil {
		a.s = r.State
	}
	return r
}
func connectionFailure(status int, message string) connectionServiceResult {
	return connectionServiceResult{Status: status, Message: message}
}
func (a *connectionOracle) log(f string, args ...any) {
	a.call(connectionEffect{Kind: "log", Name: fmt.Sprintf(f, args...)})
}
func (a *connectionOracle) selection(name string) {
	mode := "manual"
	selected := name
	switch name {
	case autoGroup:
		mode = "auto"
		selected = ""
	case fallbackGroup:
		mode = "fallback"
		selected = ""
	}
	a.call(connectionEffect{Kind: "commit", Mutation: "selection", Mode: mode, Name: selected})
}
func (a *connectionOracle) lockRegion(name string) {
	if a.s.Chain || name == "" {
		return
	}
	r := a.call(connectionEffect{Kind: "region", Name: name})
	if !r.Found || r.Region.Status != regionVerified {
		return
	}
	if !a.call(connectionEffect{Kind: "region-fresh", Name: name}).Bool {
		return
	}
	if a.s.LockedRegion == r.Region.Country && a.s.LockedNode == name {
		return
	}
	a.call(connectionEffect{Kind: "commit", Mutation: "region", Name: name, Region: r.Region.Country})
	if out := a.call(connectionEffect{Kind: "save-settings"}); out.Err != nil {
		a.log("could not persist the locked region: %v", out.Err)
	}
}
func (a *connectionOracle) activeNode() string {
	r := a.call(connectionEffect{Kind: "get", Path: "/proxies/SmartVPN"})
	if r.Err != nil {
		return ""
	}
	selected, _ := r.Value.(string)
	if selected == autoGroup || selected == fallbackGroup {
		nested := a.call(connectionEffect{Kind: "get", Path: "/proxies/" + selected})
		if nested.Err == nil {
			selected, _ = nested.Value.(string)
		}
	}
	return selected
}
func (a *connectionOracle) drop() error {
	a.call(connectionEffect{Kind: "stop-patrol"})
	a.call(connectionEffect{Kind: "stop-path"})
	stop := a.call(connectionEffect{Kind: "stop-kernel"}).Err
	var proxy error
	if a.s.ProxyOn {
		proxy = a.call(connectionEffect{Kind: "system-proxy", Bool: false}).Err
	}
	a.call(connectionEffect{Kind: "commit", Mutation: "proxy-on", Bool: proxy != nil})
	if stop == nil {
		return proxy
	}
	return stop
}
func connectionNodeExists(nodes []proxyNode, name string) bool {
	for _, n := range nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}
func runGoConnectionService(q connectionServiceRequest, fx connectionServiceEffects) connectionServiceResult {
	defer fx.Release()
	a := connectionOracle{fx: fx}
	ok := connectionServiceResult{Status: 200, Data: map[string]any{"ok": true}}
	if q.Op == "select" && strings.TrimSpace(q.Name) == "" {
		return connectionFailure(400, connectionMessages["invalid-selection"])
	}
	if q.Op == "drop" {
		a.call(connectionEffect{Kind: "snapshot"})
		ok.Err = a.drop()
		return ok
	}
	a.call(connectionEffect{Kind: "lock"})
	switch q.Op {
	case "disconnect":
		if err := a.drop(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return connectionFailure(500, "Mihomo could not be stopped")
		}
		return ok
	case "select":
		connected := a.call(connectionEffect{Kind: "running"}).Bool
		if a.s.Chain {
			a.call(connectionEffect{Kind: "unlock"})
			return connectionFailure(409, connectionMessages["chain-selection"])
		}
		if !connected {
			if q.Name != autoGroup && q.Name != fallbackGroup && !connectionNodeExists(a.s.CachedNodes, q.Name) {
				a.call(connectionEffect{Kind: "unlock"})
				return connectionFailure(409, "fetch the subscription nodes before selecting one")
			}
			a.selection(q.Name)
			if q.Name != autoGroup && q.Name != fallbackGroup {
				a.lockRegion(q.Name)
			}
			nodes := cloneConnectionNodes(a.s.CachedNodes)
			for i := range nodes {
				nodes[i].Selected = nodes[i].Name == q.Name
			}
			a.call(connectionEffect{Kind: "commit", Mutation: "cache", Nodes: nodes})
			if out := a.call(connectionEffect{Kind: "save-settings"}); out.Err != nil {
				a.call(connectionEffect{Kind: "unlock"})
				return connectionFailure(500, "could not save the pending node selection")
			}
			a.call(connectionEffect{Kind: "unlock"})
			ok.Data["pending"] = true
			return ok
		}
		a.call(connectionEffect{Kind: "unlock"})
		out := a.call(connectionEffect{Kind: "list"})
		if out.Err != nil {
			return connectionFailure(502, "could not read Mihomo nodes")
		}
		if q.Name != autoGroup && q.Name != fallbackGroup && !connectionNodeExists(out.Nodes, q.Name) {
			return connectionFailure(400, "node is no longer in the subscription; refresh nodes")
		}
		if a.call(connectionEffect{Kind: "put", Path: "/proxies/SmartVPN", Name: q.Name}).Err != nil {
			return connectionFailure(502, "Mihomo could not select that node")
		}
		a.call(connectionEffect{Kind: "lock"})
		if q.Name == autoGroup || q.Name == fallbackGroup {
			a.lockRegion(a.activeNode())
		} else {
			a.lockRegion(q.Name)
		}
		a.selection(q.Name)
		a.call(connectionEffect{Kind: "save-settings"})
		a.call(connectionEffect{Kind: "unlock"})
		return ok
	case "connect":
		if a.call(connectionEffect{Kind: "running"}).Bool {
			return ok
		}
		if a.s.Mode == modeSystemProxy && a.call(connectionEffect{Kind: "proxy-conflict"}).Bool {
			return connectionFailure(409, connectionMessages["proxy-conflict"])
		}
		if out := a.call(connectionEffect{Kind: "start-kernel"}); out.Err != nil {
			return connectionFailure(502, out.Err.Error())
		}
		out := a.call(connectionEffect{Kind: "list"})
		nodes := out.Nodes
		if out.Err != nil || len(nodes) == 0 {
			a.call(connectionEffect{Kind: "stop-kernel"})
			return connectionFailure(502, "subscription returned no selectable nodes; fetch nodes or check the subscription")
		}
		choice := a.s.Choice
		autoSwitched := false
		if a.s.Chain {
			choice = chainExit
		} else if a.s.LockedRegion != "" {
			if a.s.LockedNode != "" && connectionNodeExists(nodes, a.s.LockedNode) {
				choice = a.s.LockedNode
			} else {
				candidates := a.call(connectionEffect{Kind: "candidates", Region: a.s.LockedRegion}).Strings
				if len(candidates) > 0 {
					choice = candidates[0]
					a.call(connectionEffect{Kind: "commit", Mutation: "lock-node", Name: choice})
				} else {
					a.call(connectionEffect{Kind: "stop-kernel"})
					return connectionFailure(502, fmt.Sprintf(connectionMessages["no-region-node"], a.s.LockedRegion))
				}
			}
		} else if a.s.SelectionMode == "manual" {
			if !connectionNodeExists(nodes, choice) {
				choice = autoGroup
				a.call(connectionEffect{Kind: "commit", Mutation: "selection", Mode: "auto", Name: ""})
				autoSwitched = true
			}
		} else if choice == "" {
			choice = autoGroup
		}
		if a.call(connectionEffect{Kind: "put", Path: "/proxies/SmartVPN", Name: choice}).Err != nil {
			a.call(connectionEffect{Kind: "stop-kernel"})
			return connectionFailure(502, "could not select the proxy group")
		}
		group := a.call(connectionEffect{Kind: "get", Path: "/proxies/SmartVPN"})
		if group.Err != nil || group.Value != choice {
			a.call(connectionEffect{Kind: "stop-kernel"})
			return connectionFailure(502, "Mihomo did not activate the proxy group")
		}
		selected := choice
		if choice == autoGroup || choice == fallbackGroup {
			nested := a.call(connectionEffect{Kind: "get", Path: "/proxies/" + choice})
			if nested.Err == nil {
				if name, valid := nested.Value.(string); valid && name != "" {
					selected = name
				}
			}
		}
		if a.call(connectionEffect{Kind: "save-settings"}).Err != nil {
			a.call(connectionEffect{Kind: "stop-kernel"})
			return connectionFailure(500, "could not save the selected node")
		}
		for i := range nodes {
			nodes[i].Selected = nodes[i].Name == choice
		}
		a.call(connectionEffect{Kind: "commit", Mutation: "cache", Nodes: nodes})
		if a.call(connectionEffect{Kind: "save-cache", Nodes: nodes}).Err != nil {
			a.call(connectionEffect{Kind: "stop-kernel"})
			return connectionFailure(500, "could not save the fetched node list")
		}
		if a.s.StoreAvailable {
			if r := a.call(connectionEffect{Kind: "upsert", Nodes: nodes}); r.Err != nil {
				a.log("could not persist the node list: %v", r.Err)
			}
		}
		a.lockRegion(selected)
		if !a.s.Chain && a.s.LockedRegion != "" && a.s.LockedNode != "" && a.s.LockedNode != choice {
			if r := a.call(connectionEffect{Kind: "put", Path: "/proxies/SmartVPN", Name: a.s.LockedNode}); r.Err != nil {
				a.log("could not pin the group to %s: %v", a.s.LockedNode, r.Err)
			} else {
				choice, selected = a.s.LockedNode, a.s.LockedNode
			}
		}
		if a.s.LockedRegion == "" {
			a.log("no verified exit region; same-region failover stays off until a region is locked")
		}
		a.call(connectionEffect{Kind: "commit", Mutation: "clear-block"})
		if a.s.Mode == modeSystemProxy {
			if a.call(connectionEffect{Kind: "system-proxy", Bool: true}).Err != nil {
				a.call(connectionEffect{Kind: "system-proxy", Bool: false})
				a.call(connectionEffect{Kind: "stop-kernel"})
				return connectionFailure(500, "could not set the Windows system proxy")
			}
			a.call(connectionEffect{Kind: "commit", Mutation: "proxy-on", Bool: true})
		} else {
			a.call(connectionEffect{Kind: "commit", Mutation: "proxy-on", Bool: false})
		}
		a.call(connectionEffect{Kind: "start-patrol"})
		a.call(connectionEffect{Kind: "start-path"})
		a.call(connectionEffect{Kind: "async-patrol"})
		latency := a.call(connectionEffect{Kind: "latency", Name: selected}).Latency
		if latency < 0 {
			latency = 0
		}
		ok.Data = map[string]any{"ok": true, "selectedNode": selected, "autoSwitched": autoSwitched, "latencyMs": latency, "lockedRegion": a.s.LockedRegion, "mode": a.s.Mode}
		return ok
	}
	panic("unknown connection operation: " + q.Op)
}

var connectionMessages = map[string]string{
	"proxy-conflict": "另一个 SmartVPN 正在使用系统代理（可能是便携版或另一份安装）；" +
		"先在那一份里断开连接，或改用 TUN 模式（TUN 不使用系统代理）",
	"no-region-node":    "锁定地区 %s 没有已验证节点；为避免跨地区直连，本次未连接",
	"invalid-selection": "invalid node selection",
	"chain-selection":   "请先关闭链式代理，再选择单个节点",
}
