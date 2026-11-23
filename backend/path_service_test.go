package main

import (
	"errors"
	"testing"
	"time"
)

func pathServiceSafeFixture(t *testing.T, q pathServiceRequest) (*app, *appPathEffects, *string) {
	t.Helper()
	a := &app{}
	testEditPath(a, func(p *servicePathState) { p.Config = defaultPathChecks() })
	testEditPath(a, func(p *servicePathState) { p.Config.Enabled = true })
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "US"})
	testEditHealth(a, func(p *healthDomainProjection) { p.Regions = map[string]regionRecord{} })
	testEditHealth(a, func(p *healthDomainProjection) { p.Health = map[string]nodeHealth{} })
	active := "original"
	e := newAppPathEffects(a, nil, q)
	e.now = func() time.Time { return time.Unix(100000, 0) }
	e.hooks = map[string]func(pathServiceEffect) pathServiceReply{}
	e.hooks["running"] = func(pathServiceEffect) pathServiceReply { return pathServiceReply{Bool: true} }
	e.hooks["get"] = func(pathServiceEffect) pathServiceReply { return pathServiceReply{Text: active} }
	e.hooks["put"] = func(q pathServiceEffect) pathServiceReply {
		if q.Name == blockedChoice {
			t.Fatal("path rejected")
		}
		active = q.Name
		return pathServiceReply{}
	}
	e.hooks["check"] = func(q pathServiceEffect) pathServiceReply {
		if !a.mu.TryLock() {
			t.Fatal("check holds mutex")
		}
		a.mu.Unlock()
		return pathServiceReply{Result: siteCheckResult{Name: q.Target.Label, State: "restricted"}}
	}
	e.hooks["save"] = func(pathServiceEffect) pathServiceReply { return pathServiceReply{Err: errors.New("synthetic")} }
	e.hooks["event"] = func(pathServiceEffect) pathServiceReply { return pathServiceReply{} }
	return a, e, &active
}
func TestPathServiceBaselineCleanup(t *testing.T) {
	q := pathServiceRequest{Op: "baseline"}
	a, e, _ := pathServiceSafeFixture(t, q)
	testEditPath(a, func(p *servicePathState) { p.Status.RetryMode = "scan" })
	testEditPath(a, func(p *servicePathState) { p.Status.RetryAt = "old" })
	testEditPath(a, func(p *servicePathState) { p.Status.Demanded = true })
	testEditPath(a, func(p *servicePathState) {
		p.Config.Targets = append(p.Config.Targets, pathTarget{Label: "extra", URL: "https://example.invalid", Enabled: true})
	})
	runGoPathService(q, e)
	if a.pathStateLocked().Busy || !a.pathStatusLocked().Running || a.pathStatusLocked().RetryMode != "targets" {
		t.Fatalf("status %+v", a.pathStatusLocked())
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}
func TestPathServiceRecoveryTargetsNoSwitch(t *testing.T) {
	q := pathServiceRequest{Op: "recovery", Mode: "targets"}
	a, e, active := pathServiceSafeFixture(t, q)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: ""})
	a.settings.Chain.Enabled = true
	testEditPath(a, func(p *servicePathState) { p.Config.Enabled = false })
	testEditPath(a, func(p *servicePathState) {
		p.Config.Targets = append(p.Config.Targets, pathTarget{Label: "extra", URL: "https://example.invalid", Enabled: true})
	})
	old := e.hooks["check"]
	e.hooks["check"] = func(x pathServiceEffect) pathServiceReply {
		r := old(x)
		if x.Target.Label == "extra" {
			r.Result.State = "error"
		}
		return r
	}
	runGoPathService(q, e)
	if *active != "original" || a.pathStateLocked().Busy || a.pathStatusLocked().Running || a.pathStatusLocked().RetryMode != "targets" {
		t.Fatalf("status %+v", a.pathStatusLocked())
	}
}
func TestPathServiceSwitchBoundaries(t *testing.T) {
	for _, delta := range []time.Duration{0, time.Nanosecond} {
		q := pathServiceRequest{Op: "switch", Target: "next", Region: "US"}
		a, e, active := pathServiceSafeFixture(t, q)
		a.writeRegionRecordLocked("next", regionRecord{Status: regionVerified, Country: "US", VerifiedAt: e.now().Add(-regionTTL - delta)})
		r := runGoPathService(q, e)
		if delta == 0 {
			if r.Err != nil || *active != "next" || a.settingsLocked().LockedNode != "next" {
				t.Fatal(r, a.settingsLocked().LockedNode)
			}
		} else if r.Err == nil || *active != "original" {
			t.Fatal("stale allowed")
		}
	}
}
func TestPathServiceSameNodeSkipsValidation(t *testing.T) {
	q := pathServiceRequest{Op: "switch", Target: "original", Region: "missing"}
	_, e, _ := pathServiceSafeFixture(t, q)
	e.hooks["region"] = func(pathServiceEffect) pathServiceReply { t.Fatal("same-node validated"); return pathServiceReply{} }
	if r := runGoPathService(q, e); r.Err != nil {
		t.Fatal(r.Err)
	}
}
func TestPathServiceRecoverySortedRegion(t *testing.T) {
	q := pathServiceRequest{Op: "recovery", Mode: "scan"}
	a, e, active := pathServiceSafeFixture(t, q)
	for _, n := range []string{"z", "a", "outside"} {
		country := "US"
		if n == "outside" {
			country = "CA"
		}
		a.writeRegionRecordLocked(n, regionRecord{Status: regionVerified, Country: country, VerifiedAt: e.now()})
	}
	old := e.hooks["check"]
	e.hooks["check"] = func(x pathServiceEffect) pathServiceReply {
		r := old(x)
		if *active == "original" {
			r.Result.State = "error"
		}
		return r
	}
	runGoPathService(q, e)
	if *active != "a" || a.pathStateLocked().Busy || a.pathStatusLocked().Running {
		t.Fatal(*active, a.pathStatusLocked())
	}
}
func TestPathServicePanicCleanup(t *testing.T) {
	for _, op := range []string{"baseline", "recovery", "round"} {
		q := pathServiceRequest{Op: op}
		a, e, _ := pathServiceSafeFixture(t, q)
		e.hooks["check"] = func(pathServiceEffect) pathServiceReply { panic("synthetic") }
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			runGoPathService(q, e)
		}()
		e.Release()
		if a.pathStateLocked().Busy || !a.mu.TryLock() {
			t.Fatal("leaked ownership", op)
		}
		a.mu.Unlock()
	}
}
