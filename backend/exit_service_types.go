package main

import (
	"errors"
	"log"
	"runtime"
)

type exitServiceRequest struct{}
type exitServiceResult struct{ Err error }
type exitServiceState struct{ Matches, RelayPresent, PatrolPresent, ProxyOn bool }
type exitEffect struct{ Kind, Message string }
type exitReply struct {
	State *exitServiceState
	Err   error
}
type exitServiceEffects interface {
	Call(exitEffect) exitReply
	Release()
}

// Only this Go executor sees runner identity; Rust receives the comparison fact.
type appExitEffects struct {
	a                                     *app
	which                                 kernelRunner
	proxyRestoreFailed                    bool
	owned                                 bool
	closeRelay                            func(*adaptiveDNS)
	stopHistory, removeConfig, dropTunnel func()
	proxyOff                              func() error
	logMessage                            func(string)
}

func newAppExitEffects(a *app, which kernelRunner) *appExitEffects {
	return &appExitEffects{a: a, which: which, closeRelay: func(r *adaptiveDNS) { r.close() }, stopHistory: a.stopTrafficHistoryLocked, removeConfig: a.removeConfigLocked, dropTunnel: a.dropTunnel, proxyOff: func() error { return a.setSystemProxy(false) }, logMessage: func(s string) { log.Print(s) }}
}
func (e *appExitEffects) state() *exitServiceState {
	return &exitServiceState{Matches: e.a.kernel == e.which, RelayPresent: e.a.dnsRelay != nil, PatrolPresent: e.a.patrolCancel != nil, ProxyOn: e.a.proxyOn}
}
func (e *appExitEffects) Release() {
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appExitEffects) Call(q exitEffect) (r exitReply) {
	if q.Kind == "lock" {
		if e.owned {
			panic("exit lock already held")
		}
		e.a.mu.Lock()
		e.owned = true
		r.State = e.state()
		return
	}
	if !e.owned {
		panic("exit effect outside lock")
	}
	defer func() { r.State = e.state() }()
	switch q.Kind {
	case "close-relay":
		e.closeRelay(e.a.dnsRelay)
		e.a.dnsRelay = nil
	case "history-stop":
		e.stopHistory()
	case "clear-kernel":
		if runtime.GOOS == "windows" && e.a.pathCancel != nil {
			e.a.stopPathMonitorLocked()
		}
		e.a.kernel = nil
		e.a.setChinaActiveLocked(false)
	case "remove-config":
		e.removeConfig()
	case "close-patrol":
		close(e.a.patrolCancel)
		e.a.patrolCancel = nil
	case "clear-block":
		e.a.setHealthBlockedLocked(false)
		e.a.setHealthReasonLocked("")
	case "proxy-off":
		r.Err = e.proxyOff()
		e.proxyRestoreFailed = r.Err != nil
	case "log":
		e.logMessage(q.Message)
	case "clear-proxy":
		if runtime.GOOS != "windows" || !e.proxyRestoreFailed {
			e.a.proxyOn = false
		}
	case "drop-tunnel":
		e.dropTunnel()
	default:
		panic("unknown exit effect: " + q.Kind)
	}
	return
}
func (a *app) runExitService(which kernelRunner) (out exitServiceResult) {
	defer func() {
		if recover() != nil {
			out = exitServiceResult{Err: errors.New("kernel exit cleanup interrupted")}
		}
	}()
	fx := newAppExitEffects(a, which)
	defer fx.Release()
	return runSelectedExitService(exitServiceRequest{}, fx)
}
