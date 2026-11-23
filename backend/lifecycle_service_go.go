package main

import "fmt"

type goLifecycleService struct {
	q      lifecycleServiceRequest
	fx     lifecycleServiceEffects
	locked bool
}

func (a *goLifecycleService) call(q lifecycleEffect) lifecycleReply { return a.fx.Call(q) }
func (a *goLifecycleService) snapshot() *lifecycleServiceState {
	return a.call(lifecycleEffect{Kind: "snapshot"}).State
}
func (a *goLifecycleService) set(n string, b bool, v int) {
	a.call(lifecycleEffect{Kind: "set", Name: n, Flag: b, Value: v})
}
func (a *goLifecycleService) log(code int, name string, err error) {
	a.call(lifecycleEffect{Kind: "log", Index: code, Name: name, Err: err})
}
func (a *goLifecycleService) lock()   { a.call(lifecycleEffect{Kind: "lock"}); a.locked = true }
func (a *goLifecycleService) unlock() { a.call(lifecycleEffect{Kind: "unlock"}); a.locked = false }
func (a *goLifecycleService) cache(selected string) {
	if a.call(lifecycleEffect{Kind: "cache-read"}).Err != nil {
		return
	}
	r := a.call(lifecycleEffect{Kind: "cache-decode"})
	if r.Err != nil {
		a.call(lifecycleEffect{Kind: "cache-remove"})
		return
	}
	if !a.call(lifecycleEffect{Kind: "cache-hash"}).Bool {
		a.call(lifecycleEffect{Kind: "cache-remove"})
		return
	}
	indices := make([]int, 0)
	for i, n := range r.Nodes {
		if n.Name == selected {
			indices = append(indices, i)
		}
	}
	a.call(lifecycleEffect{Kind: "cache-apply", Indices: indices})
}
func (a *goLifecycleService) load() {
	s := a.snapshot()
	if a.call(lifecycleEffect{Kind: "read-settings"}).Err == nil {
		s = a.call(lifecycleEffect{Kind: "decode-settings"}).State
		if s.ChainsNil && s.Hops >= 2 {
			a.set("legacy-chain", false, 0)
		}
		if s.StrictPrivacy {
			a.set("strict", false, 0)
		}
	}
	if s.HasURL {
		r := a.call(lifecycleEffect{Kind: "open-secret"})
		if r.Err != nil {
			a.log(0, "", r.Err)
		}
		a.set("url", r.Err != nil, 0)
	}
	if s.SelectionEmpty {
		a.set("selection", false, 0)
	}
	if s.PathDefault {
		a.set("path-default", false, 0)
	}
	a.cache(s.Selected)
}
func (a *goLifecycleService) sweep() {
	r := a.call(lifecycleEffect{Kind: "glob"})
	if r.Err != nil {
		return
	}
	own := a.call(lifecycleEffect{Kind: "own-config"}).Text
	for i, n := range r.Names {
		if n != own {
			a.call(lifecycleEffect{Kind: "remove-config", Index: i})
		}
	}
}
func (a *goLifecycleService) bundle() {
	if a.snapshot().Android {
		return
	}
	if !a.call(lifecycleEffect{Kind: "bundle-source"}).Bool {
		return
	}
	for _, n := range []string{"mihomo.exe", "wintun.dll"} {
		if a.call(lifecycleEffect{Kind: "bundle-target-stat", Name: n}).Err == nil {
			continue
		}
		if a.call(lifecycleEffect{Kind: "bundle-source-stat", Name: n}).Err != nil {
			continue
		}
		if r := a.call(lifecycleEffect{Kind: "bundle-read", Name: n}); r.Err != nil {
			a.log(1, n, r.Err)
			continue
		}
		if r := a.call(lifecycleEffect{Kind: "bundle-write", Name: n}); r.Err != nil {
			a.log(2, n, r.Err)
			continue
		}
		a.log(3, n, nil)
	}
}
func (a *goLifecycleService) create() {
	a.call(lifecycleEffect{Kind: "init"})
	a.load()
	a.set("locks", false, 0)
	a.sweep()
	if !a.snapshot().Portable {
		a.bundle()
	}
	if r := a.call(lifecycleEffect{Kind: "store-open"}); r.Err != nil {
		a.log(4, "", r.Err)
		return
	}
	r := a.call(lifecycleEffect{Kind: "load-params"})
	if r.Err != nil {
		a.log(5, "", r.Err)
	} else {
		if r.State.Android {
			if r.State.ConnectTimeout < 5000 {
				a.set("timeout", false, 6000)
			}
			if r.State.FailureThreshold < 3 {
				a.set("threshold", false, 3)
			}
		}
		a.set("params", false, 0)
	}
	if r := a.call(lifecycleEffect{Kind: "load-health"}); r.Err != nil {
		a.log(6, "", r.Err)
	} else {
		a.set("health", false, 0)
	}
	if r := a.call(lifecycleEffect{Kind: "load-regions"}); r.Err != nil {
		a.log(7, "", r.Err)
	} else {
		a.set("regions", false, 0)
	}
	a.call(lifecycleEffect{Kind: "manual"})
	a.call(lifecycleEffect{Kind: "subscriptions"})
	a.call(lifecycleEffect{Kind: "source-labels"})
}
func (a *goLifecycleService) prepare() {
	if a.call(lifecycleEffect{Kind: "proxy-other"}).Bool {
		a.log(8, "", nil)
	} else if r := a.call(lifecycleEffect{Kind: "proxy"}); r.Err != nil {
		a.log(9, "", r.Err)
	}
	a.call(lifecycleEffect{Kind: "redactor"})
	a.call(lifecycleEffect{Kind: "scrub"})
	a.call(lifecycleEffect{Kind: "china-refresh"})
}
func (a *goLifecycleService) serve() lifecycleServiceResult {
	s := a.snapshot()
	r := a.call(lifecycleEffect{Kind: "listen", Index: 0})
	if r.Err != nil {
		return lifecycleServiceResult{Err: r.Err}
	}
	index := 0
	if r.Value == s.SocksPort {
		r = a.call(lifecycleEffect{Kind: "listen", Index: 1})
		a.call(lifecycleEffect{Kind: "listener-close", Index: 0})
		if r.Err != nil {
			return lifecycleServiceResult{Err: r.Err}
		}
		index = 1
	}
	a.call(lifecycleEffect{Kind: "server-new"})
	a.call(lifecycleEffect{Kind: "server-serve", Index: index})
	a.set("api-port", false, r.Value)
	return lifecycleServiceResult{Port: r.Value}
}
func runGoLifecycleService(q lifecycleServiceRequest, fx lifecycleServiceEffects) (r lifecycleServiceResult) {
	defer fx.Release()
	complete := false
	defer func() {
		if complete {
			if f, ok := fx.(interface {
				FinalizeLifecycleResult(lifecycleServiceRequest, lifecycleServiceResult) lifecycleServiceResult
			}); ok {
				r = f.FinalizeLifecycleResult(q, r)
			}
		}
	}()
	a := &goLifecycleService{q: q, fx: fx}
	defer func() {
		if a.locked {
			a.unlock()
		}
	}()
	switch q.Op {
	case "new":
		a.create()
	case "load-settings":
		a.load()
	case "cache":
		a.cache(a.snapshot().Selected)
	case "bundle":
		a.bundle()
	case "sweep":
		a.sweep()
	case "prepare":
		a.prepare()
	case "serve":
		r = a.serve()
	case "cleanup":
		a.lock()
		a.call(lifecycleEffect{Kind: "stop-auto"})
		a.call(lifecycleEffect{Kind: "stop-kernel"})
		s := a.snapshot()
		if s.Android {
			a.set("proxy", false, 0)
		} else if s.ProxyOn {
			a.call(lifecycleEffect{Kind: "proxy"})
			a.set("proxy", false, 0)
		}
		if s.Store {
			a.call(lifecycleEffect{Kind: "store-close"})
		}
	case "shutdown":
		a.call(lifecycleEffect{Kind: "disconnect"})
		a.call(lifecycleEffect{Kind: "signal"})
	case "identify":
		a.lock()
		connected := a.call(lifecycleEffect{Kind: "running"}).Bool
		a.unlock()
		a.call(lifecycleEffect{Kind: "response", Name: "identify", Flag: connected})
	case "reapply":
		a.lock()
		if !a.call(lifecycleEffect{Kind: "running"}).Bool {
			r.Status = 409
			r.Message = "connect before enabling the Windows proxy"
		} else if a.call(lifecycleEffect{Kind: "mode"}).Text != modeSystemProxy {
			r.Status = 409
			r.Message = "this platform has no machine proxy setting to reapply"
		} else if err := a.call(lifecycleEffect{Kind: "proxy", Flag: true}).Err; err != nil {
			r.Status = 500
			r.Message = "could not re-enable the Windows system proxy"
		} else {
			a.set("proxy", true, 0)
		}
		a.call(lifecycleEffect{Kind: "response", Value: r.Status, Text: r.Message})
	default:
		r.Err = fmt.Errorf("unknown lifecycle operation %q", q.Op)
	}
	complete = true
	return
}
