package main

import (
	"reflect"
	"testing"
)

type pathAuthorityRun func(pathServiceRequest, pathServiceEffects) pathServiceResult
type monitorAuthorityRun func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult

func pathAuthoritySeed(t *testing.T, a *app, factory authorityFactory) {
	t.Helper()
	control := a.controlLocked()
	catalog := a.catalogEntriesLocked()
	fromCache := a.profileFromCacheLocked()
	old := a.service
	state := a.pathStateLocked()
	state.Status = a.pathStatusLocked()
	a.service = authorityNew(t, factory)
	a.patchControlLocked(controlAll, control)
	a.restoreCatalogEntriesLocked(catalog)
	a.setProfileFromCacheLocked(fromCache)
	testEditPath(a, func(p *servicePathState) { p.Config = state.Config; p.Status = state.Status; p.Busy = state.Busy })
	if state.MonitorActive {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
	}
	if old != nil {
		_ = old.Close()
	}
}
func pathAuthorityRoundGolden(t *testing.T, factory authorityFactory, run pathAuthorityRun) {
	q := pathServiceRequest{Op: "baseline"}
	s := pathDiffBase()
	f := pathDiffNew(q, s)
	pathAuthoritySeed(t, f.a, factory)
	owner := f.a.service.backend
	if e := f.a.service.baseline(trafficSample{at: pathDiffNow, up: 123, down: 456}); e != nil {
		t.Fatal(e)
	}
	if e := f.a.service.samplingError("history untouched"); e != nil {
		t.Fatal(e)
	}
	before := authoritySnapshot(t, f.a.service)
	if r := run(q, f); r.Err != nil {
		t.Fatal(r.Err)
	}
	p := pathAuthorityState(t, f.a.service)
	if p.Busy || !p.Status.Running || len(p.Status.Results) != 2 {
		t.Fatal("baseline full workflow", p)
	}
	q = pathServiceRequest{Op: "recovery", Mode: "scan"}
	f.e.recovery = true
	if r := run(q, f); r.Err != nil {
		t.Fatal(r.Err)
	}
	p = pathAuthorityState(t, f.a.service)
	if p.Busy || p.Status.Running {
		t.Fatal("recovery cleanup")
	}
	after := authoritySnapshot(t, f.a.service)
	if after.Up != before.Up || after.TimeToken != before.TimeToken || after.SamplingError != before.SamplingError || after.Revision <= before.Revision || f.a.service.backend != owner {
		t.Fatal("path requests changed owner/traffic")
	}
	for _, put := range f.puts {
		if put == blockedChoice || put == "foreign" {
			t.Fatal("unsafe switch")
		}
	}
}
func pathAuthorityMonitorGolden(t *testing.T, factory authorityFactory, run monitorAuthorityRun) {
	q := monitorDiffRequest("update")
	s := monitorDiffBase()
	s.SaveFail = true
	f := monitorDiffNew(q, s)
	pathAuthoritySeed(t, f.a, factory)
	owner := f.a.service.backend
	before := authoritySnapshot(t, f.a.service)
	r := run(q, f)
	if r.Err == nil || f.w.Code != 500 || f.a.pathConfigLocked().IntervalSec != 45 || f.a.service.backend != owner {
		t.Fatal("save failed config rollback/owner", r, f.w.Code)
	}
	after := authoritySnapshot(t, f.a.service)
	if after.Revision <= before.Revision {
		t.Fatal("config not authority mutation")
	}
	saved := f.a.pathConfigLocked()
	testEditSettings(f.a, func(s *settings) { s.SelectedNode = "unrelated edit" })
	if !reflect.DeepEqual(saved, f.a.pathConfigLocked()) {
		t.Fatal("settings overwrote path")
	}
	q = monitorDiffRequest("snapshot")
	f.q = q
	f.e.q = q
	if r = run(q, f); r.Err != nil {
		t.Fatal(r.Err)
	}
	if f.a.service.backend != owner {
		t.Fatal("snapshot switched owner")
	}
}
func pathAuthorityFreshCompare(t *testing.T, factory authorityFactory, run pathAuthorityRun) {
	for i, q := range []pathServiceRequest{{Op: "baseline"}, {Op: "recovery", Mode: "scan"}, {Op: "recovery", Mode: "manual"}, {Op: "round"}, {Op: "switch", Target: "b", Region: "US"}} {
		s := pathDiffBase()
		s.SaveFail = true
		if i == 1 {
			s.Pass = map[string]bool{"a|Google": false, "a|YouTube": false}
		}
		g, r := pathDiffNew(q, s), pathDiffNew(q, s)
		pathAuthoritySeed(t, g.a, authorityGoFactory)
		pathAuthoritySeed(t, r.a, factory)
		a, b := runGoPathService(q, g), run(q, r)
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(g.trace, r.trace) {
			t.Fatalf("path whole effect sequence differs op=%s", q.Op)
		}
		gs, rs := authoritySnapshot(t, g.a.service), authoritySnapshot(t, r.a.service)
		gs.Epoch = 0
		rs.Epoch = 0
		if gs != rs {
			t.Fatal("shared final state differs")
		}
	}
}
func TestServiceStateSafePathWorkflows(t *testing.T) {
	pathAuthorityRoundGolden(t, newSelectedServiceStateBackend, runSelectedPathService)
	pathAuthorityMonitorGolden(t, newSelectedServiceStateBackend, runSelectedPathMonitorService)
	pathAuthorityFreshCompare(t, newSelectedServiceStateBackend, runSelectedPathService)
}
func monitorAuthorityDiffRun(t *testing.T, q pathMonitorServiceRequest, s monitorDiffSpec, factory authorityFactory, run monitorAuthorityRun) (pathMonitorServiceResult, *monitorDiffFX) {
	f := monitorDiffNew(q, s)
	pathAuthoritySeed(t, f.a, factory)
	owner := f.a.service.backend
	borrowed := q.Op == "start" || q.Op == "stop"
	if borrowed {
		f.a.mu.Lock()
	}
	r := run(q, f)
	f.Release()
	if borrowed {
		if f.a.mu.TryLock() {
			f.a.mu.Unlock()
			t.Fatal("released borrowed lock")
		}
		f.a.mu.Unlock()
	}
	if !f.a.mu.TryLock() {
		t.Fatal("lock leak")
	}
	f.a.mu.Unlock()
	for _, work := range f.queue {
		work()
	}
	if f.a.service.backend != owner {
		t.Fatal("scheduler changed authority owner")
	}
	return r, f
}
func monitorAuthorityDiff(t *testing.T, op string, s monitorDiffSpec, factory authorityFactory, run monitorAuthorityRun) {
	a, x := monitorAuthorityDiffRun(t, monitorDiffRequest(op), s, authorityGoFactory, runGoPathMonitorService)
	b, y := monitorAuthorityDiffRun(t, monitorDiffRequest(op), s, factory, run)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.work, y.work) || !reflect.DeepEqual(x.stops, y.stops) || x.w.Body.String() != y.w.Body.String() {
		t.Fatalf("monitor op=%s differs\ngo=%+v\nrust=%+v", op, x.trace, y.trace)
	}
	xs, ys := authoritySnapshot(t, x.a.service), authoritySnapshot(t, y.a.service)
	xs.Epoch = 0
	ys.Epoch = 0
	if xs != ys {
		t.Fatalf("scheduler state differs %s", op)
	}
}
