package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

type connectionFakeEffects struct {
	adapter     *appConnectionEffects
	Trace       []connectionEffect
	Running     bool
	Failure     map[string]error
	Nodes       []proxyNode
	Put         string
	Hook        func(connectionEffect)
	ProxyValues []bool
}

func newConnectionFake(borrowed bool) *connectionFakeEffects {
	a := testHealthApp(testControlApp(&app{cachedNodes: []proxyNode{{Name: "one"}}}, func(a *app) { testSetSettings(a, settings{ConnectionMode: modeSystemProxy, SelectionMode: "auto"}) }), func(p *healthDomainProjection) {
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
	})
	return &connectionFakeEffects{adapter: &appConnectionEffects{a: a, locked: borrowed}, Nodes: []proxyNode{{Name: "one"}}}
}
func (f *connectionFakeEffects) Release() { f.adapter.Release() }
func (f *connectionFakeEffects) Call(q connectionEffect) (r connectionReply) {
	f.Trace = append(f.Trace, q)
	if f.Hook != nil {
		f.Hook(q)
	}
	switch q.Kind {
	case "lock", "unlock", "snapshot", "commit", "region", "region-fresh", "latency":
		return f.adapter.Call(q)
	case "running":
		r.Bool = f.Running
	case "list":
		r.Nodes = cloneConnectionNodes(f.Nodes)
	case "put":
		f.Put = q.Name
	case "get":
		if q.Path == "/proxies/SmartVPN" {
			r.Value = f.Put
		} else {
			r.Value = "one"
		}
	case "system-proxy":
		f.ProxyValues = append(f.ProxyValues, q.Bool)
	case "stop-patrol":
		testEditHealth(f.adapter.a, func(p *healthDomainProjection) { p.Blocked = false })
		testEditHealth(f.adapter.a, func(p *healthDomainProjection) { p.BlockReason = "" })
	}
	r.Err = f.Failure[q.Kind]
	if f.adapter.locked {
		r.State = connectionStateFromApp(f.adapter.a)
	}
	return
}
func TestConnectionServiceSuccessAndOwnership(t *testing.T) {
	f := newConnectionFake(false)
	r := runGoConnectionService(connectionServiceRequest{Op: "connect"}, f)
	if r.Status != 200 || r.Data["selectedNode"] != "one" || !f.adapter.a.proxyOn || !reflect.DeepEqual(f.ProxyValues, []bool{true}) {
		t.Fatal(r, f.ProxyValues)
	}
	if !f.adapter.a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	f.adapter.a.mu.Unlock()
	f.Nodes[0].Name = "changed"
	if f.adapter.a.cachedNodes[0].Name != "one" {
		t.Fatal("cached nodes borrowed effects memory")
	}
}
func TestConnectionServiceFailureUnwind(t *testing.T) {
	for _, where := range []string{"start-kernel", "list", "put", "get", "save-settings", "save-cache", "system-proxy"} {
		t.Run(where, func(t *testing.T) {
			f := newConnectionFake(false)
			f.Failure = map[string]error{where: errors.New("injected")}
			r := runGoConnectionService(connectionServiceRequest{Op: "connect"}, f)
			if r.Status == 200 {
				t.Fatal(r)
			}
			stops := 0
			for _, q := range f.Trace {
				if q.Kind == "stop-kernel" {
					stops++
				}
			}
			want := 1
			if where == "start-kernel" {
				want = 0
			}
			if stops != want {
				t.Fatal(where, stops)
			}
			if where == "system-proxy" && !reflect.DeepEqual(f.ProxyValues, []bool{true, false}) {
				t.Fatal(f.ProxyValues)
			}
			if where == "save-cache" && len(f.adapter.a.cachedNodes) != 1 {
				t.Fatal("partial cache state rolled back")
			}
			if !f.adapter.a.mu.TryLock() {
				t.Fatal("lock leaked")
			}
			f.adapter.a.mu.Unlock()
		})
	}
}
func TestConnectionServiceDropBorrowedAndErrorPriority(t *testing.T) {
	kernelErr := fmt.Errorf("wrapped: %w", os.ErrProcessDone)
	proxyErr := errors.New("proxy failure")
	f := newConnectionFake(true)
	a := f.adapter.a
	a.mu.Lock()
	a.proxyOn = true
	testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = true })
	testEditHealth(a, func(p *healthDomainProjection) { p.BlockReason = "blocked" })
	f.Failure = map[string]error{"stop-kernel": kernelErr, "system-proxy": proxyErr}
	r := runGoConnectionService(connectionServiceRequest{Op: "drop"}, f)
	if r.Err != kernelErr || !a.proxyOn || a.healthDomainLocked().Blocked || a.healthDomainLocked().BlockReason != "" {
		t.Fatal(r, a.proxyOn, a.healthDomainLocked().Blocked)
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed caller lock was released")
	}
	a.mu.Unlock()
	f = newConnectionFake(false)
	f.adapter.a.proxyOn = true
	f.Failure = map[string]error{"stop-kernel": kernelErr, "system-proxy": proxyErr}
	if r = runGoConnectionService(connectionServiceRequest{Op: "disconnect"}, f); r.Status != 200 {
		t.Fatal(r)
	}
	f = newConnectionFake(false)
	f.Failure = map[string]error{"stop-kernel": errors.New(os.ErrProcessDone.Error())}
	if r = runGoConnectionService(connectionServiceRequest{Op: "disconnect"}, f); r.Status != 500 {
		t.Fatal(r)
	}
}
func TestConnectionServiceSelectUsesLatestState(t *testing.T) {
	f := newConnectionFake(false)
	f.Running = true
	a := f.adapter.a
	f.Hook = func(q connectionEffect) {
		if q.Kind == "put" {
			if !a.mu.TryLock() {
				t.Fatal("PUT while locked")
			}
			a.settings.Chain.Enabled = true
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = "concurrent" })
			testEditSettings(a, func(s *settings) { s.DNSProtection = true })
			a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "concurrent-region"})
			a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "concurrent-node"})
			a.mu.Unlock()
		}
	}
	r := runGoConnectionService(connectionServiceRequest{Op: "select", Name: "one"}, f)
	if r.Status != 200 || !a.settings.Chain.Enabled || a.settingsLocked().SubscriptionURL != "concurrent" || !a.settingsLocked().DNSProtection || a.settingsLocked().SelectedNode != "one" || a.controlLocked().RuntimeLockedRegion != "concurrent-region" {
		t.Fatal(r, a.settingsLocked(), a.controlLocked().RuntimeLockedRegion)
	}
	for _, q := range f.Trace {
		if q.Kind == "region" {
			t.Fatal("new chain state ignored")
		}
	}
}
func TestConnectionServiceManualFallbackPartial(t *testing.T) {
	f := newConnectionFake(false)
	a := f.adapter.a
	testEditSettings(a, func(s *settings) { s.SelectionMode = "manual" })
	testEditSettings(a, func(s *settings) { s.SelectedNode = "missing" })
	f.Failure = map[string]error{"put": errors.New("rejected")}
	r := runGoConnectionService(connectionServiceRequest{Op: "connect"}, f)
	if r.Status != 502 || a.settingsLocked().SelectionMode != "auto" || a.settingsLocked().SelectedNode != "" {
		t.Fatal(r, a.settingsLocked())
	}
}
func TestConnectionServicePanicReleasesOwnedOnly(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		f := newConnectionFake(borrowed)
		if borrowed {
			f.adapter.a.mu.Lock()
		}
		f.Hook = func(q connectionEffect) {
			if q.Kind == "stop-patrol" || q.Kind == "start-kernel" {
				panic("injected")
			}
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Error("missing panic")
				}
			}()
			op := "connect"
			if borrowed {
				op = "drop"
			}
			runGoConnectionService(connectionServiceRequest{Op: op}, f)
		}()
		acquired := f.adapter.a.mu.TryLock()
		if borrowed {
			if acquired {
				f.adapter.a.mu.Unlock()
				t.Fatal("borrowed unlock")
			}
			f.adapter.a.mu.Unlock()
		} else {
			if !acquired {
				t.Fatal("owned leak")
			}
			f.adapter.a.mu.Unlock()
		}
	}
}
