package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func appIndependentChannelGolden(t *testing.T, run appIndependentRun) {
	a := &app{}
	q := appServiceRequest{Op: "loop", Interval: 7 * time.Second}
	e := newAppControlEffects(a, q)
	ticks := make(chan time.Time)
	cancel := make(chan struct{})
	stopped := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	stops, children := 0, 0
	e.cancel = cancel
	e.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != 7*time.Second {
			t.Fatal("interval changed", d)
		}
		return ticks, func() { stops++; once.Do(func() { close(stopped) }) }
	}
	e.once = func() {
		children++
		childQ := appServiceRequest{Op: "once"}
		child := newAppControlEffects(a, childQ)
		child.hooks = map[string]func(appServiceEffect) appServiceReply{"running": func(appServiceEffect) appServiceReply {
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Fatal("once running query outside lock")
			}
			return appServiceReply{}
		}}
		run(t, childQ, child)
		if children == 2 {
			close(cancel)
		}
	}
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			select {
			case ticks <- time.Unix(int64(i), 0):
			case <-stopped:
				return
			}
		}
	}()
	run(t, q, e)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fake ticker producer not joined")
	}
	if children != 2 || stops != 1 {
		t.Fatalf("children=%d ticker stops=%d", children, stops)
	}
	if !a.mu.TryLock() {
		t.Fatal("once retained application lock")
	}
	a.mu.Unlock()
	e.Release()
	if stops != 1 {
		t.Fatal("ticker stop replay")
	}
}
func TestAppServiceIndependentSelectedChannelLoop(t *testing.T) {
	appIndependentChannelGolden(t, func(_ *testing.T, q appServiceRequest, fx appServiceEffects) appServiceResult {
		return runSelectedAppService(q, fx)
	})
}

func appIndependentStateResourceGolden(t *testing.T, run appIndependentRun) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.started = time.Now()
	hops := []string{"first", "second"}
	catalog := map[string][]string{"keep": hops}
	a.settings.Chain = proxyChain{Enabled: true, Hops: hops}
	a.settings.Chains = catalog
	e := newAppControlEffects(a, appServiceRequest{Op: "state"})
	e.clock = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 123, time.FixedZone("east", 8*3600)) }
	var payload map[string]any
	e.hooks = map[string]func(appServiceEffect) appServiceReply{
		"running":    func(appServiceEffect) appServiceReply { return appServiceReply{} },
		"tun-reason": func(appServiceEffect) appServiceReply { return appServiceReply{Text: "unavailable"} },
		"resolved":   func(appServiceEffect) appServiceReply { e.resolved = "synthetic"; return appServiceReply{} },
		"response": func(q appServiceEffect) appServiceReply {
			if !e.owned {
				t.Fatal("state projection outside lock")
			}
			payload = e.stateProjection(q.Plan)
			return appServiceReply{}
		},
	}
	r := run(t, e.q, e)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if payload == nil {
		t.Fatal("missing projection")
	}
	if got := payload["startedAt"].(time.Time); got != a.started || got.Location() != a.started.Location() {
		t.Fatal("time identity changed")
	}
	out := payload["chains"].(map[string][]string)
	out["keep"][0] = "written"
	if catalog["keep"][0] != "written" || hops[0] != "written" {
		t.Fatal("chain graph aliases detached")
	}
	out["added"] = []string{"visible"}
	if a.settings.Chains["added"][0] != "visible" {
		t.Fatal("chains map detached")
	}
	chain := payload["chain"].(proxyChain)
	chain.Hops[1] = "changed"
	if hops[1] != "changed" {
		t.Fatal("chain slice detached")
	}
	if !a.mu.TryLock() {
		t.Fatal("state lock leaked")
	}
	a.mu.Unlock()
}
func TestAppServiceIndependentSelectedStateResourceIdentity(t *testing.T) {
	appIndependentStateResourceGolden(t, func(_ *testing.T, q appServiceRequest, fx appServiceEffects) appServiceResult {
		return runSelectedAppService(q, fx)
	})
}

func appIndependentProtectedHealthGolden(t *testing.T, run appIndependentRun) {
	for _, pass := range []bool{true, false} {
		name := "protected"
		if !pass {
			name = "exhausted"
		}
		t.Run(name, func(t *testing.T) {
			a, calls, _ := migrationHealthApp(t)
			a.confirmProbe = func() bool { return pass }
			now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
			a.writeRegionRecordLocked("active", regionRecord{Name: "active", Country: "JP", Status: regionVerified, VerifiedAt: now})
			a.writeRegionRecordLocked("foreign", regionRecord{Name: "foreign", Country: "US", Status: regionVerified, VerifiedAt: now})
			a.writeHealthRecordLocked("foreign", nodeHealth{Name: "foreign", State: healthHealthy, LatencyMs: 1})
			params := a.healthMetaLocked().Params
			rounds := params.FailureThreshold
			if pass {
				rounds += 2
			}
			for i := 0; i < rounds; i++ {
				q := appServiceRequest{Op: "once"}
				e := newAppControlEffects(a, q)
				at := now.Add(time.Duration(i) * time.Duration(params.PatrolIntervalSec) * time.Second)
				e.clock = func() time.Time { return at }
				e.hooks = map[string]func(appServiceEffect) appServiceReply{"running": func(appServiceEffect) appServiceReply { return appServiceReply{Flag: true} }, "active-get": func(appServiceEffect) appServiceReply { return appServiceReply{Text: "active"} }, "delays": func(appServiceEffect) appServiceReply {
					return appServiceReply{Err: errors.New("synthetic bulk failure")}
				}, "log": func(appServiceEffect) appServiceReply { return appServiceReply{} }}
				r := run(t, q, e)
				if r.Err != nil {
					t.Fatal(r.Err)
				}
			}
			h := a.healthDomainLocked()
			c := a.controlLocked()
			if h.Blocked == pass || c.RuntimeLockedRegion != "JP" || c.RuntimeLockedNode != "active" || h.Params != params {
				t.Fatalf("protected region/cadence changed health=%+v control=%+v", h, c)
			}
			puts := 0
			for _, call := range *calls {
				if strings.HasPrefix(call, "PUT ") {
					puts++
					if call != `PUT /proxies/SmartVPN {"name":"REJECT"}` {
						t.Fatal("cross-region selection", call)
					}
				}
			}
			if pass && puts != 0 || !pass && puts != 1 {
				t.Fatalf("unexpected mocked blocking count %d", puts)
			}
		})
	}
}
func TestAppServiceIndependentSelectedProtectedHealth(t *testing.T) {
	appIndependentProtectedHealthGolden(t, func(_ *testing.T, q appServiceRequest, fx appServiceEffects) appServiceResult {
		return runSelectedAppService(q, fx)
	})
}
