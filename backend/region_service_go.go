package main

import (
	"fmt"
	"sort"
	"time"
)

type regionOracle struct {
	s  *regionServiceState
	fx regionServiceEffects
}

func (a *regionOracle) call(q regionEffect) regionReply {
	r := a.fx.Call(q)
	if r.State != nil {
		a.s = r.State
	}
	return r
}
func regionFailure(status int, message string) regionServiceResult {
	return regionServiceResult{Status: status, Message: message}
}
func regionCandidatePool(s *regionServiceState, now time.Time) []string {
	return candidatesForRegion(s.LockedRegion, s.Regions, s.Health, now)
}
func regionBestCandidate(s *regionServiceState, now time.Time) string {
	pool := regionCandidatePool(s, now)
	if len(pool) == 0 {
		return ""
	}
	nodes := make([]proxyNode, 0, len(pool))
	delays := map[string]int{}
	for _, name := range pool {
		nodes = append(nodes, proxyNode{Name: name})
		if h, ok := s.Health[name]; ok && h.LatencyMs > 0 {
			delays[name] = h.LatencyMs
		}
	}
	if best, _ := chooseReachableNodeGo(nodes, delays, ""); best != "" {
		return best
	}
	return pool[0]
}
func (a *regionOracle) activeNode() string {
	r := a.call(regionEffect{Kind: "get", Path: "/proxies/SmartVPN"})
	if r.Err != nil {
		return ""
	}
	selected, _ := r.Value.(string)
	if selected == autoGroup || selected == fallbackGroup {
		nested := a.call(regionEffect{Kind: "get", Path: "/proxies/" + selected})
		if nested.Err == nil {
			selected, _ = nested.Value.(string)
		}
	}
	return selected
}
func runGoRegionService(q regionServiceRequest, fx regionServiceEffects) regionServiceResult {
	defer fx.Release()
	if q.Op == "lock" && q.Country != "" && !looksLikeCountryCode(q.Country) {
		return regionFailure(400, "the region must be a two-letter country code")
	}
	a := regionOracle{fx: fx}
	a.call(regionEffect{Kind: "lock"})
	if q.Op == "report" {
		names := make([]string, 0, len(a.s.Regions))
		for name := range a.s.Regions {
			names = append(names, name)
		}
		sort.Strings(names)
		now := a.call(regionEffect{Kind: "now"}).Now
		stale := make([]bool, len(names))
		for i, name := range names {
			r := a.s.Regions[name]
			stale[i] = r.Status == regionVerified && now.Sub(r.VerifiedAt) > regionTTL
		}
		pool := regionCandidatePool(a.s, now)
		return regionServiceResult{Status: 200, Data: projectRegionReport(a.s, names, stale, pool)}
	}
	if q.Op != "lock" {
		panic("unknown region operation: " + q.Op)
	}
	country := q.Country
	if country != "" {
		if a.s.Chain {
			return regionFailure(409, regionServiceChainError)
		}
		now := a.call(regionEffect{Kind: "now"}).Now
		usable := false
		for _, r := range a.s.Regions {
			if r.Status == regionVerified && r.Country == country && now.Sub(r.VerifiedAt) < regionTTL {
				usable = true
				break
			}
		}
		if !usable {
			return regionFailure(400, errNoLockableRegion.Error())
		}
	}
	a.call(regionEffect{Kind: "set-country", Country: country})
	node := ""
	if country != "" {
		now := a.call(regionEffect{Kind: "now"}).Now
		node = regionBestCandidate(a.s, now)
	}
	a.call(regionEffect{Kind: "set-node", Name: node})
	if a.call(regionEffect{Kind: "save"}).Err != nil {
		return regionFailure(500, "could not save the locked region")
	}
	if country != "" && a.call(regionEffect{Kind: "running"}).Bool {
		now := a.call(regionEffect{Kind: "now"}).Now
		if a.s.LockedNode == "" {
			a.call(regionEffect{Kind: "block", Now: now, Message: regionBlockMessage(country)})
		} else if a.activeNode() != a.s.LockedNode {
			if out := a.call(regionEffect{Kind: "put", Path: "/proxies/SmartVPN", Name: a.s.LockedNode}); out.Err != nil {
				a.call(regionEffect{Kind: "log", Message: fmt.Sprintf("could not pin the group to %s after locking %s: %v", a.s.LockedNode, country, out.Err)})
			}
		}
	}
	at := a.call(regionEffect{Kind: "now"}).Now
	a.call(regionEffect{Kind: "event", Event: switchEvent{At: at, Group: "SmartVPN", ToNode: "-", Trigger: "region_lock", Evidence: regionEvidence(country)}})
	now := a.call(regionEffect{Kind: "now"}).Now
	return regionServiceResult{Status: 200, Data: map[string]any{"ok": true, "lockedRegion": a.s.LockedRegion, "pool": len(regionCandidatePool(a.s, now))}}
}

const regionServiceChainError = "请先关闭链式代理，再更改锁定地区"

func regionBlockMessage(country string) string {
	return "锁定地区 " + country + " 目前没有可用节点"
}
func regionEvidence(country string) string { return "锁定地区：" + country }
