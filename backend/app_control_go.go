package main

import (
	"sort"
	"time"
)

type appControlOracle struct{ fx appServiceEffects }

func (o appControlOracle) call(k string) appServiceReply { return o.fx.Call(appServiceEffect{Kind: k}) }
func (o appControlOracle) active() string {
	r := o.fx.Call(appServiceEffect{Kind: "active-get", Text: "SmartVPN"})
	if r.Err != nil {
		return ""
	}
	n := r.Text
	if n == autoGroup || n == fallbackGroup {
		if r = o.fx.Call(appServiceEffect{Kind: "active-get", Text: n}); r.Err == nil {
			n = r.Text
		}
	}
	return n
}
func (o appControlOracle) choice() string {
	r := o.call("choice")
	if r.ChainEnabled {
		return chainExit
	}
	if r.LockedRegion != "" && r.LockedNode != "" {
		return r.LockedNode
	}
	switch r.SelectionMode {
	case "fallback":
		return fallbackGroup
	case "manual":
		return r.SelectedNode
	}
	return autoGroup
}
func (o appControlOracle) mode() string {
	r := o.call("mode")
	if r.ModeSupported {
		return r.Mode
	}
	return r.DefaultMode
}
func (o appControlOracle) snapshot() appServicePlan {
	r := o.call("health-facts")
	p := appServicePlan{}
	counts := map[string]int{healthHealthy: 0, healthSuspect: 0, healthUnhealthy: 0, healthCooldown: 0, healthRecovery: 0}
	for _, h := range r.Health {
		if h.Cooldown {
			counts[healthCooldown]++
		} else {
			counts[h.State]++
		}
	}
	p.LockedRegion = o.call("pin").Text
	country := o.call("pin").Text
	if country != "" {
		for _, v := range r.Regions {
			if v.Verified && v.Country == country && !v.Expired && !v.Cooldown {
				p.PoolSize++
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.Counts = append(p.Counts, appCount{k, counts[k]})
	}
	return p
}
func (o appControlOracle) start() {
	if o.call("cancel-present").Flag {
		return
	}
	p := o.call("meta").Params
	o.call("cancel-new")
	o.fx.Call(appServiceEffect{Kind: "spawn", Interval: time.Duration(p.PatrolIntervalSec) * time.Second})
}
func (o appControlOracle) stop() {
	if o.call("cancel-present").Flag {
		o.call("cancel-close")
	}
	if !o.call("meta").Flag {
		return
	}
	if o.call("running").Flag {
		r := o.fx.Call(appServiceEffect{Kind: "switch", Text: o.choice()})
		if r.Err != nil {
			o.fx.Call(appServiceEffect{Kind: "log", Text: "stop"})
		}
	}
	o.call("clear-blocked")
	o.call("clear-reason")
}
func (o appControlOracle) sweep() {
	if !o.call("running").Flag {
		return
	}
	if o.call("job-running").Flag {
		return
	}
	o.call("now")
	active := o.active()
	if active == "" || active == blockedChoice || active == "DIRECT" {
		if o.call("meta").Flag {
			o.call("unblock")
		}
		return
	}
	p := o.call("meta").Params
	r := o.fx.Call(appServiceEffect{Kind: "delays", Interval: time.Duration(p.ConnectTimeoutMs) * time.Millisecond})
	if r.Err != nil {
		o.fx.Call(appServiceEffect{Kind: "log", Text: "sweep"})
		o.fx.Call(appServiceEffect{Kind: "sweep", Text: active, Count: 1})
		return
	}
	o.fx.Call(appServiceEffect{Kind: "sweep", Text: active})
}
func appNormalizeParams(p healthParams) healthParams { return p.normalizedGo() }
func runGoAppService(q appServiceRequest, fx appServiceEffects) (out appServiceResult) {
	o := appControlOracle{fx}
	defer fx.Release()
	response := func(status int, text string, p appServicePlan) {
		fx.Call(appServiceEffect{Kind: "response", Status: status, Text: text, Plan: p})
	}
	switch q.Op {
	case "snapshot":
		out.Plan = o.snapshot()
	case "start":
		o.start()
	case "stop":
		o.stop()
	case "sweep":
		o.sweep()
	case "once":
		o.call("lock")
		defer o.call("unlock")
		o.sweep()
	case "loop":
		fx.Call(appServiceEffect{Kind: "ticker-new", Interval: q.Interval})
		defer o.call("ticker-stop")
		for o.call("wait").Flag {
			o.call("once")
		}
	case "params":
		if q.Params.PatrolIntervalSec != 0 && q.Params.PatrolIntervalSec < 10 {
			response(400, "interval", appServicePlan{})
			return
		}
		o.call("lock")
		defer o.call("unlock")
		p := appNormalizeParams(q.Params)
		fx.Call(appServiceEffect{Kind: "commit-params", Params: p})
		if o.call("store-present").Flag {
			if fx.Call(appServiceEffect{Kind: "save-params", Params: p}).Err != nil {
				response(500, "save-params", appServicePlan{})
				return
			}
		}
		if o.call("cancel-present").Flag && p.PatrolIntervalSec != 0 {
			o.call("cancel-close")
			o.start()
		}
		o.call("path-start")
		o.call("meta")
		response(200, "params", appServicePlan{})
	case "patrol":
		o.call("lock")
		defer o.call("unlock")
		o.call("meta")
		if !o.call("running").Flag {
			response(409, "connect", appServicePlan{})
			return
		}
		o.sweep()
		out.Plan = o.snapshot()
		response(200, "patrol", out.Plan)
	case "events":
		n := fx.Call(appServiceEffect{Kind: "parse-limit", Text: q.Limit}).Count
		if n < 1 || n > 500 {
			n = 50
		}
		if !o.call("store-present").Flag {
			response(500, "no-store", appServicePlan{})
			return
		}
		events := fx.Call(appServiceEffect{Kind: "events-read", Count: n})
		if events.Err != nil {
			response(500, "events", appServicePlan{})
			return
		}
		for i := 0; i < events.Count; i++ {
			fx.Call(appServiceEffect{Kind: "events-project", Count: i})
		}
		response(200, "events", appServicePlan{})
	case "state":
		o.call("lock")
		defer o.call("unlock")
		s := o.call("state-base")
		p := appServicePlan{ZeroJob: s.JobNil, StrictPrivacy: s.WebRTC && s.DNS}
		p.Running = o.call("running").Flag
		if p.Running && s.ProxyOn {
			r := o.call("proxy")
			p.ProxyEnabled = r.Flag
			if r.Err != nil {
				fx.Call(appServiceEffect{Kind: "log", Text: "proxy"})
			}
		}
		if p.Running {
			p.ActiveNode = o.active()
			p.ActiveRegion = fx.Call(appServiceEffect{Kind: "active-region", Text: p.ActiveNode}).Text
		}
		p.TunReason = o.call("tun-reason").Text
		o.call("resolved")
		selected := o.choice()
		fx.Call(appServiceEffect{Kind: "project-choice", Text: selected})
		h := o.snapshot()
		p.Counts = h.Counts
		p.PoolSize = h.PoolSize
		p.LockedRegion = h.LockedRegion
		mode := o.mode()
		fx.Call(appServiceEffect{Kind: "project-mode", Text: mode})
		p.TunActive = p.Running && o.mode() != modeSystemProxy
		out.Plan = p
		response(200, "state", p)
	default:
		panic("invalid application control operation")
	}
	return
}
