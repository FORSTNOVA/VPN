package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type pathDiffSpec struct {
	Running, Enabled, Busy, Chain, Demanded, SaveFail, GetFail bool
	FailPut, Active, Region                                    string
	Pass                                                       map[string]bool
	EditAt                                                     int
	Cooldown, Expired                                          bool
}
type pathDiffFrame struct {
	Effect           pathServiceEffect
	State            *pathServiceState
	Pin, SettingsPin string
	LastSwitch       time.Time
	Locked           bool
	Interval         int
}
type pathDiffFX struct {
	a            *app
	e            *appPathEffects
	active       string
	trace        []pathDiffFrame
	puts, checks []string
	saves        int
	events       []switchEvent
	spec         pathDiffSpec
}

var pathDiffNow = time.Date(2026, 5, 6, 7, 8, 9, 123, time.FixedZone("X", 3600))

func pathDiffBase() pathDiffSpec {
	return pathDiffSpec{Running: true, Enabled: true, Active: "a", Region: "US", EditAt: -1, Pass: map[string]bool{}}
}
func pathDiffNew(q pathServiceRequest, s pathDiffSpec) *pathDiffFX {
	a := &app{}
	testEditPath(a, func(p *servicePathState) {
		p.Config = pathCheckSettings{Enabled: s.Enabled, IntervalSec: 120, Targets: []pathTarget{{Label: "disabled-google", URL: pathCheckPresets[0].URL, Enabled: false}, {Label: "Extra", URL: "https://synthetic.invalid", Enabled: true}}}
	})
	a.settings.Chain.Enabled = s.Chain
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: s.Region})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: s.Active})
	testEditSettings(a, func(v *settings) { v.LockedNode = s.Active })
	testEditPath(a, func(p *servicePathState) { p.Busy = s.Busy })
	testEditPath(a, func(p *servicePathState) {
		p.Status = pathStatus{Demanded: s.Demanded, Note: "old note", AlertID: 7, Results: []siteCheckResult{{Name: "old", State: "ok"}}}
	})
	testEditHealth(a, func(p *healthDomainProjection) { p.Regions = map[string]regionRecord{} })
	testEditHealth(a, func(p *healthDomainProjection) { p.Health = map[string]nodeHealth{} })
	for _, n := range []string{"a", "b", "c", "foreign"} {
		country := "US"
		if n == "foreign" {
			country = "FR"
		}
		v := regionRecord{Name: n, Country: country, Status: regionVerified, VerifiedAt: pathDiffNow.Add(-regionTTL)}
		if s.Expired && n == "b" {
			v.VerifiedAt = v.VerifiedAt.Add(-1)
		}
		a.writeRegionRecordLocked(n, v)
	}
	if s.Cooldown {
		a.writeHealthRecordLocked("b", nodeHealth{CooldownUntil: pathDiffNow.Add(time.Second)})
	}
	e := newAppPathEffects(a, nil, q)
	f := &pathDiffFX{a: a, e: e, active: s.Active, spec: s}
	e.hooks = map[string]func(pathServiceEffect) pathServiceReply{}
	e.now = func() time.Time { return pathDiffNow }
	e.hooks["running"] = func(pathServiceEffect) pathServiceReply { return pathServiceReply{Bool: s.Running} }
	e.hooks["get"] = func(pathServiceEffect) pathServiceReply {
		if s.GetFail {
			return pathServiceReply{Err: errors.New("synthetic GET")}
		}
		return pathServiceReply{Text: f.active}
	}
	e.hooks["put"] = func(x pathServiceEffect) pathServiceReply {
		if x.Name == blockedChoice || x.Name == "foreign" {
			panic("REJECT/cross-region attempted")
		}
		f.puts = append(f.puts, x.Name)
		if x.Name == s.FailPut {
			return pathServiceReply{Err: errors.New("synthetic PUT")}
		}
		f.active = x.Name
		return pathServiceReply{}
	}
	e.hooks["check"] = func(x pathServiceEffect) pathServiceReply {
		if !a.mu.TryLock() {
			panic("site check holds app lock")
		}
		if len(f.checks) == s.EditAt {
			testEditPath(a, func(p *servicePathState) { p.Config.IntervalSec = 777 })
			a.settings.Chain.Enabled = !a.settings.Chain.Enabled
			a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "edited-region"})
		}
		a.mu.Unlock()
		key := f.active + "|" + x.Target.Label
		f.checks = append(f.checks, key)
		pass, exists := s.Pass[key]
		if !exists {
			pass = true
		}
		r := siteCheckResult{Name: x.Target.Label, State: "restricted", HTTPCode: 403, Detail: f.active}
		if !pass {
			r.State = "error"
			r.HTTPCode = 0
		}
		return pathServiceReply{Result: r}
	}
	e.hooks["save"] = func(pathServiceEffect) pathServiceReply {
		f.saves++
		if s.SaveFail {
			return pathServiceReply{Err: errors.New("synthetic save")}
		}
		return pathServiceReply{}
	}
	e.hooks["event"] = func(x pathServiceEffect) pathServiceReply {
		f.events = append(f.events, x.Event)
		return pathServiceReply{}
	}
	return f
}
func (f *pathDiffFX) Call(x pathServiceEffect) pathServiceReply {
	if f.e.hooks[x.Kind] == nil {
		switch x.Kind {
		case "lock", "unlock", "proxy", "now", "region", "age", "candidates", "set":
		default:
			panic("unmocked path port " + x.Kind)
		}
	}
	r := f.e.Call(x)
	x.Results = append([]siteCheckResult(nil), x.Results...)
	x.Strings = append([]string(nil), x.Strings...)
	f.trace = append(f.trace, pathDiffFrame{x, f.e.snapshot(), f.a.controlLocked().RuntimeLockedNode, f.a.settingsLocked().LockedNode, f.a.healthDomainLocked().LastSwitch, f.e.locked, f.a.pathConfigLocked().IntervalSec})
	return r
}
func (f *pathDiffFX) Release() { f.e.Release() }
func pathDiffRun(t *testing.T, q pathServiceRequest, s pathDiffSpec, run func(pathServiceRequest, pathServiceEffects) pathServiceResult) (pathServiceResult, *pathDiffFX) {
	t.Helper()
	f := pathDiffNew(q, s)
	r := run(q, f)
	f.Release()
	if !f.a.mu.TryLock() {
		t.Fatal("path mutex leaked")
	}
	f.a.mu.Unlock()
	return r, f
}
func pathDiffCompare(t *testing.T, q pathServiceRequest, s pathDiffSpec, run func(pathServiceRequest, pathServiceEffects) pathServiceResult) {
	t.Helper()
	a, x := pathDiffRun(t, q, s, runGoPathService)
	b, y := pathDiffRun(t, q, s, run)
	if fmt.Sprint(a.Err) != fmt.Sprint(b.Err) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.e.snapshot(), y.e.snapshot()) || !reflect.DeepEqual(x.puts, y.puts) || !reflect.DeepEqual(x.checks, y.checks) || !reflect.DeepEqual(x.events, y.events) || x.saves != y.saves {
		t.Fatalf("request=%#v err=%v/%v\nGoTrace=%#v\nRustTrace=%#v\nchecks=%v/%v", q, a.Err, b.Err, x.trace, y.trace, x.checks, y.checks)
	}
}
func pathDiffRequests() []pathServiceRequest {
	return []pathServiceRequest{{Op: "baseline"}, {Op: "round"}, {Op: "switch", Target: "b", Region: "US", Trigger: "synthetic", Evidence: "synthetic"}, {Op: "recovery", Mode: "scan"}, {Op: "recovery", Mode: "demand"}, {Op: "recovery", Mode: "manual"}, {Op: "recovery", Mode: "targets"}}
}
func TestMigrationPathSafeSelectedLifecycle(t *testing.T) {
	for _, q := range pathDiffRequests() {
		pathDiffCompare(t, q, pathDiffBase(), runSelectedPathService)
	}
}
func pathLegacyGoldens(t *testing.T, run func(pathServiceRequest, pathServiceEffects) pathServiceResult) {
	s := pathDiffBase()
	_, f := pathDiffRun(t, pathServiceRequest{Op: "baseline"}, s, run)
	if !reflect.DeepEqual(f.checks, []string{"a|Google", "a|YouTube"}) || f.a.pathStateLocked().Busy || !f.a.pathStatusLocked().Running {
		t.Fatal("baseline fixed targets/cleanup", f.checks)
	}
	s = pathDiffBase()
	s.Pass = map[string]bool{"a|Google": false}
	_, f = pathDiffRun(t, pathServiceRequest{Op: "baseline"}, s, run)
	if len(f.puts) != 0 || f.a.pathStatusLocked().RetryMode != "scan" || f.a.pathStatusLocked().RetryAt != pathDiffNow.Add(30*time.Second).UTC().Format(time.RFC3339Nano) {
		t.Fatal("baseline retry30s")
	}
	s = pathDiffBase()
	s.Pass = map[string]bool{"a|Google": false}
	_, f = pathDiffRun(t, pathServiceRequest{Op: "recovery", Mode: "targets"}, s, run)
	if len(f.puts) != 0 || len(f.checks) != 2 || f.a.pathStatusLocked().RetryMode != "scan" {
		t.Fatal("targets baseline failure")
	}
	s = pathDiffBase()
	s.Pass = map[string]bool{"a|Extra": false}
	_, f = pathDiffRun(t, pathServiceRequest{Op: "recovery", Mode: "targets"}, s, run)
	if len(f.puts) != 0 || f.a.pathStatusLocked().RetryMode != "targets" || f.a.pathStatusLocked().Alert != "" {
		t.Fatal("optional failure switched")
	}
	for _, mode := range []string{"scan", "demand", "manual"} {
		s = pathDiffBase()
		_, f = pathDiffRun(t, pathServiceRequest{Op: "recovery", Mode: mode}, s, run)
		want := 2
		if mode != "scan" {
			want = 3
		}
		if len(f.checks) != want || f.a.pathStateLocked().Busy || f.a.pathStatusLocked().Running {
			t.Fatal("demand gating/cleanup", mode, f.checks)
		}
	}
	for _, failRestore := range []bool{false, true} {
		s = pathDiffBase()
		s.Pass = map[string]bool{"a|Extra": false, "b|Extra": false, "c|Extra": false}
		if failRestore {
			s.FailPut = "a"
		}
		r, f := pathDiffRun(t, pathServiceRequest{Op: "recovery", Mode: "demand"}, s, run)
		if r.Err != nil || !reflect.DeepEqual(f.puts, []string{"b", "c", "a"}) {
			t.Fatal("restore/order", f.puts, r.Err)
		}
		rs := f.a.pathStatusLocked().Results
		if len(rs) != 3 || rs[0].Detail != "a" || rs[2].Detail != "c" || f.a.pathStatusLocked().RetryMode != "targets" {
			t.Fatal("legacy mixed results", rs)
		}
		want := "a"
		if failRestore {
			want = "c"
		}
		if f.active != want {
			t.Fatal("restore error")
		}
	}
	for _, cool := range []bool{false, true} {
		s = pathDiffBase()
		s.Pass = map[string]bool{"a|Google": false}
		s.Cooldown = cool
		_, f = pathDiffRun(t, pathServiceRequest{Op: "recovery", Mode: "scan"}, s, run)
		want := "b"
		if cool {
			want = "c"
		}
		if len(f.puts) != 1 || f.puts[0] != want {
			t.Fatal("candidate TTL/sort/cooldown", f.puts)
		}
	}
	for _, failPut := range []bool{false, true} {
		s = pathDiffBase()
		s.SaveFail = true
		if failPut {
			s.FailPut = "b"
		}
		r, f := pathDiffRun(t, pathServiceRequest{Op: "switch", Target: "b", Region: "US"}, s, run)
		if failPut {
			if r.Err == nil || f.a.controlLocked().RuntimeLockedNode != "a" || f.saves != 0 {
				t.Fatal("PUT failure commit")
			}
		} else if r.Err != nil || f.a.controlLocked().RuntimeLockedNode != "b" || f.a.settingsLocked().LockedNode != "b" || f.saves != 1 {
			t.Fatal("save rollback")
		}
	}
	s = pathDiffBase()
	q := pathServiceRequest{Op: "switch", Target: "a", Region: "WRONG"}
	f = pathDiffNew(q, s)
	testEditHealth(f.a, func(p *healthDomainProjection) { p.Regions = nil })
	r := run(q, f)
	f.Release()
	if r.Err != nil || len(f.puts) != 0 || f.saves != 0 {
		t.Fatal("current revalidated")
	}
	s = pathDiffBase()
	s.EditAt = 0
	_, f = pathDiffRun(t, pathServiceRequest{Op: "recovery", Mode: "targets"}, s, run)
	if f.a.pathConfigLocked().IntervalSec != 777 || f.a.controlLocked().RuntimeLockedRegion != "edited-region" || !f.a.settings.Chain.Enabled {
		t.Fatal("concurrent edits lost")
	}
	s = pathDiffBase()
	s.Pass = map[string]bool{"a|Extra": false}
	_, f = pathDiffRun(t, pathServiceRequest{Op: "round"}, s, run)
	if !reflect.DeepEqual(f.checks, []string{"a|Extra", "b|Extra"}) || !reflect.DeepEqual(f.puts, []string{"b"}) {
		t.Fatal("legacy round", f.checks, f.puts)
	}
}
func TestMigrationPathSafeLegacyGoldens(t *testing.T) { pathLegacyGoldens(t, runSelectedPathService) }
func pathRevalidationGoldens(t *testing.T, run func(pathServiceRequest, pathServiceEffects) pathServiceResult) {
	for _, expire := range []bool{false, true} {
		s := pathDiffBase()
		s.Pass = map[string]bool{"a|Google": false}
		q := pathServiceRequest{Op: "recovery", Mode: "scan"}
		f := pathDiffNew(q, s)
		check := f.e.hooks["check"]
		f.e.hooks["check"] = func(x pathServiceEffect) pathServiceReply {
			r := check(x)
			if len(f.checks) == 1 {
				f.a.mu.Lock()
				if expire {
					v := f.a.healthDomainLocked().Regions["b"]
					v.VerifiedAt = pathDiffNow.Add(-regionTTL - time.Nanosecond)
					f.a.writeRegionRecordLocked("b", v)
				} else {
					f.a.writeHealthRecordLocked("b", nodeHealth{CooldownUntil: pathDiffNow.Add(time.Hour)})
				}
				f.a.mu.Unlock()
			}
			return r
		}
		run(q, f)
		f.Release()
		want := "b"
		if expire {
			want = "c"
		}
		if !reflect.DeepEqual(f.puts, []string{want}) {
			t.Fatal("fixed pool / fresh TTL / no cooldown recheck", expire, f.puts)
		}
	}
}
func TestMigrationPathSafeRevalidation(t *testing.T) {
	pathRevalidationGoldens(t, runSelectedPathService)
}
