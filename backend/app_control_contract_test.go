package main

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func appIndependentGolden(t *testing.T, run appIndependentRun) {
	appIndependentOverflowGolden(t, run)
	t.Run("params-zero-commits-before-save-failure", func(t *testing.T) {
		f := &appIndependentFX{s: appIndependentSpec{Store: true, Cancel: true, Fail: "save-params"}}
		run(t, appServiceRequest{Op: "params"}, f)
		if f.s.Params.PatrolIntervalSec != 10 || !f.s.Cancel || f.spawns != 0 || f.pathStarts != 0 {
			t.Fatalf("partial commit %+v", f)
		}
		want := []string{"lock", "commit-params", "store-present", "save-params", "response", "unlock"}
		if !reflect.DeepEqual(appIndependentKinds(f), want) {
			t.Fatal(f.trace)
		}
		if f.trace[4].Status != 500 {
			t.Fatal(f.trace)
		}
	})
	t.Run("params-restart-after-save", func(t *testing.T) {
		f := &appIndependentFX{s: appIndependentSpec{Store: true, Cancel: true}}
		run(t, appServiceRequest{Op: "params"}, f)
		if !f.s.Cancel || f.closes != 1 || f.spawns != 1 || f.pathStarts != 1 {
			t.Fatalf("restart %+v", f)
		}
		want := []string{"lock", "commit-params", "store-present", "save-params", "cancel-present", "cancel-close", "cancel-present", "meta", "cancel-new", "spawn", "path-start", "meta", "response", "unlock"}
		if !reflect.DeepEqual(appIndependentKinds(f), want) {
			t.Fatal(f.trace)
		}
	})
	t.Run("existing-start-never-reads-authority", func(t *testing.T) {
		f := &appIndependentFX{s: appIndependentSpec{Cancel: true, Panic: "meta"}}
		run(t, appServiceRequest{Op: "start"}, f)
		if !reflect.DeepEqual(appIndependentKinds(f), []string{"cancel-present"}) || f.releases != 1 {
			t.Fatal(f.trace)
		}
	})
	t.Run("stop-switch-error-still-clears", func(t *testing.T) {
		f := &appIndependentFX{s: appIndependentSpec{Cancel: true, Blocked: true, Running: true, Fail: "switch", Choice: appServiceReply{SelectionMode: "manual", SelectedNode: "chosen"}}}
		run(t, appServiceRequest{Op: "stop"}, f)
		if f.s.Cancel || f.s.Blocked || f.closes != 1 {
			t.Fatal("stop rolled back", f)
		}
		want := []string{"cancel-present", "cancel-close", "meta", "running", "choice", "switch", "log", "clear-blocked", "clear-reason"}
		if !reflect.DeepEqual(appIndependentKinds(f), want) {
			t.Fatal(f.trace)
		}
	})
	t.Run("snapshot-single-facts-unknown-state", func(t *testing.T) {
		f := &appIndependentFX{s: appIndependentSpec{Pin: "JP", Health: []appHealthFact{{State: "custom"}, {State: healthHealthy, Cooldown: true}}, Regions: []appRegionFact{{Country: "JP", Verified: true}, {Country: "JP", Verified: true, Expired: true}, {Country: "US", Verified: true}}}}
		r := run(t, appServiceRequest{Op: "snapshot"}, f)
		m := map[string]int{}
		for _, c := range r.Plan.Counts {
			m[c.Name] = c.Count
		}
		if m["custom"] != 1 || m[healthCooldown] != 1 || m[healthHealthy] != 0 || r.Plan.PoolSize != 1 || len(m) != 6 {
			t.Fatal(r)
		}
		if !reflect.DeepEqual(appIndependentKinds(f), []string{"health-facts", "pin", "pin"}) {
			t.Fatal(f.trace)
		}
	})
	t.Run("loop-no-initial-or-identity-check", func(t *testing.T) {
		f := &appIndependentFX{s: appIndependentSpec{Ticks: 2, Running: false, Cancel: false}}
		run(t, appServiceRequest{Op: "loop", Interval: 3 * time.Second}, f)
		want := []string{"ticker-new", "wait", "once", "wait", "once", "wait", "ticker-stop"}
		if !reflect.DeepEqual(appIndependentKinds(f), want) {
			t.Fatal(f.trace)
		}
	})
	t.Run("events-unlocked-limit", func(t *testing.T) {
		for _, raw := range []string{" 1", "0", "501", "500", "+1", "999999999999999999999"} {
			f := &appIndependentFX{s: appIndependentSpec{Store: true}}
			run(t, appServiceRequest{Op: "events", Limit: raw}, f)
			want := 50
			if raw == "+1" {
				want = 1
			}
			if raw == "500" {
				want = 500
			}
			if f.trace[2].Kind != "events-read" || f.trace[2].Count != want || f.owned {
				t.Fatal(f.trace)
			}
			if !reflect.DeepEqual(appIndependentKinds(f), []string{"parse-limit", "store-present", "events-read", "events-project", "response"}) {
				t.Fatal(f.trace)
			}
		}
	})
}
func TestAppServiceIndependentSelectedGolden(t *testing.T) {
	appIndependentGolden(t, func(_ *testing.T, q appServiceRequest, fx appServiceEffects) appServiceResult {
		return runSelectedAppService(q, fx)
	})
}
func TestAppServiceIndependentSelectedStopAuthorityPanic(t *testing.T) {
	f := &appIndependentFX{s: appIndependentSpec{Cancel: true, Panic: "meta"}}
	var p any
	func() {
		defer func() { p = recover() }()
		r := runSelectedAppService(appServiceRequest{Op: "stop"}, f)
		if r.Err != nil {
			panic(r.Err)
		}
	}()
	if p == nil || f.s.Cancel || f.closes != 1 || f.releases != 1 || !reflect.DeepEqual(appIndependentKinds(f), []string{"cancel-present", "cancel-close", "meta"}) {
		t.Fatalf("cancel must survive authority failure without replay: panic=%v fixture=%+v trace=%v", p, f, f.trace)
	}
}

func TestAppServiceIndependentSelectedBulkFailureDispatch(t *testing.T) {
	f := &appIndependentFX{s: appIndependentSpec{Running: true, Active: "pinned-JP", Fail: "delays", Params: defaultHealthParams()}}
	runSelectedAppService(appServiceRequest{Op: "sweep"}, f)
	want := []string{"running", "job-running", "now", "active-get", "meta", "delays", "log", "sweep"}
	if !reflect.DeepEqual(appIndependentKinds(f), want) || f.trace[len(f.trace)-1].Count != 1 || f.trace[len(f.trace)-1].Text != "pinned-JP" {
		t.Fatalf("bulk failure must reach health child as failure facts: %v", f.trace)
	}
	if f.trace[5].Interval != 3*time.Second {
		t.Fatal("probe budget changed", f.trace[5])
	}
}

func appIndependentOverflowGolden(t *testing.T, run appIndependentRun) {
	for _, n := range []int{int(^uint(0) >> 1), -int(^uint(0)>>1) - 1} {
		for _, op := range []string{"start", "sweep"} {
			t.Run("duration-overflow-"+op+strconv.Itoa(n), func(t *testing.T) {
				s := appIndependentSpec{Params: healthParams{PatrolIntervalSec: n, ConnectTimeoutMs: n}, Running: true, Active: "node"}
				f := &appIndependentFX{s: s}
				run(t, appServiceRequest{Op: op}, f)
				kind := "spawn"
				want := time.Duration(n) * time.Second
				if op == "sweep" {
					kind = "delays"
					want = time.Duration(n) * time.Millisecond
				}
				found := false
				for _, e := range f.trace {
					if e.Kind == kind {
						found = true
						if e.Interval != want {
							t.Fatalf("%s overflow changed got=%d want=%d", kind, e.Interval, want)
						}
					}
				}
				if !found {
					t.Fatalf("missing %s", kind)
				}
			})
		}
	}
}
func TestAppServiceIndependentStopClosedAuthorityFacade(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	c := make(chan struct{})
	a.patrolCancel = c
	if err := a.service.Close(); err != nil {
		t.Fatal(err)
	}
	var p any
	a.mu.Lock()
	func() { defer func() { p = recover() }(); a.stopPatrolLocked() }()
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	a.mu.Unlock()
	if p == nil || a.patrolCancel != nil {
		t.Fatalf("closed authority hidden or cancel restored: panic=%v", p)
	}
	select {
	case <-c:
	default:
		t.Fatal("cancel must close before failed authority read")
	}
}
