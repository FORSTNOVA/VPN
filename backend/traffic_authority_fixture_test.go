package main

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type authorityTrafficStep struct {
	op                  string
	up, down            int64
	delta               time.Duration
	getFail, recordFail bool
}
type authorityTrafficRun func(trafficServiceRequest, trafficServiceEffects) trafficServiceResult
type authorityTrafficFixture struct {
	a                                   *app
	t                                   *testing.T
	clock                               time.Time
	gets, records, reads, spawns, stops int
}

func authorityTrafficNew(t *testing.T, factory authorityFactory) *authorityTrafficFixture {
	s := authorityNew(t, factory)
	return &authorityTrafficFixture{a: &app{service: s, store: &store{}, started: time.Unix(100, 0)}, t: t, clock: time.Now()}
}
func (f *authorityTrafficFixture) step(s authorityTrafficStep, run authorityTrafficRun) trafficServiceResult {
	q := trafficServiceRequest{Op: s.op}
	a := f.a
	var cancel chan struct{}
	if s.op == "loop" {
		cancel = a.trafficHistoryCancel
	}
	e := newAppTrafficEffects(a, q, cancel)
	e.running = func() bool { return true }
	f.clock = f.clock.Add(s.delta)
	e.now = func() time.Time { return f.clock }
	e.get = func() (kernelTraffic, error) {
		f.gets++
		if s.getFail {
			return kernelTraffic{}, errors.New("fake GET")
		}
		return kernelTraffic{UploadTotal: s.up, DownloadTotal: s.down}, nil
	}
	e.record = func(string, kernelTraffic, time.Time) error {
		f.records++
		if s.recordFail {
			return errors.New("fake record")
		}
		return nil
	}
	e.read = func(r trafficHistoryReport, _ string) (trafficHistoryReport, error) { f.reads++; return r, nil }
	e.spawn = func(c chan struct{}) {
		if a.trafficHistoryCancel != c {
			panic("spawn before publish")
		}
		f.spawns++
	}
	e.tickerStart = func() {}
	e.tickerStop = func() { f.stops++ }
	wait := 0
	e.wait = func() bool { wait++; return wait == 1 }
	if e.borrowed {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	return run(q, e)
}
func authorityTrafficGolden(t *testing.T, factory authorityFactory, run authorityTrafficRun) {
	f := authorityTrafficNew(t, factory)
	r := f.step(authorityTrafficStep{op: "current", up: 100, down: 200}, run)
	if r.Err != nil || r.Data["uploadRate"] != int64(0) {
		t.Fatal("first sample", r)
	}
	s := authoritySnapshot(t, f.a.service)
	if s.Revision != 1 || !s.HasBaseline || s.Up != 100 {
		t.Fatal(s)
	}
	r = f.step(authorityTrafficStep{op: "current", up: 130, down: 250, delta: time.Second}, run)
	if r.Err != nil || r.Data["uploadRate"] != int64(30) || r.Data["downloadRate"] != int64(50) {
		t.Fatal("shared baseline", r)
	}
	before := authoritySnapshot(t, f.a.service)
	f.step(authorityTrafficStep{op: "collect", up: 300, down: 400, getFail: true}, run)
	after := authoritySnapshot(t, f.a.service)
	if !after.HasBaseline || after.Up != before.Up || after.SamplingError == "" {
		t.Fatal("history failure changed realtime baseline")
	}
	historyError := after.SamplingError
	f.step(authorityTrafficStep{op: "current", getFail: true}, run)
	after = authoritySnapshot(t, f.a.service)
	if after.HasBaseline || after.SamplingError != historyError {
		t.Fatal("current GET fail changed history error")
	}
	r = f.step(authorityTrafficStep{op: "query"}, run)
	if r.Err != nil || r.Report == nil || r.Report.SamplingError != historyError || f.reads != 1 {
		t.Fatal("query snapshot", r)
	}
	f.step(authorityTrafficStep{op: "collect", up: 50, down: 60, recordFail: true}, run)
	if authoritySnapshot(t, f.a.service).SamplingError == "" {
		t.Fatal("store error lost")
	}
	f.step(authorityTrafficStep{op: "collect", up: 50, down: 60}, run)
	after = authoritySnapshot(t, f.a.service)
	if after.SamplingError != "" || after.HasBaseline || f.records != 2 {
		t.Fatal("collect IO replay or baseline dualwrite")
	}
	f.step(authorityTrafficStep{op: "start"}, run)
	if f.spawns != 1 {
		t.Fatal("start")
	}
	f.step(authorityTrafficStep{op: "loop", up: 1, down: 2}, run)
	if f.records != 3 || f.stops != 1 {
		t.Fatal("fake loop")
	}
	f.step(authorityTrafficStep{op: "stop"}, run)
	if f.a.trafficHistoryCancel != nil || f.records != 3 {
		t.Fatal("stop final collection added")
	}
	w := httptest.NewRecorder()
	writeTrafficServiceResult(w, r)
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal("history JSON response")
	}
}
func authorityTrafficCompare(t *testing.T, factory authorityFactory, run authorityTrafficRun) {
	g := authorityTrafficNew(t, authorityGoFactory)
	r := authorityTrafficNew(t, factory)
	r.clock = g.clock
	steps := []authorityTrafficStep{{op: "current", up: 10, down: 20}, {op: "current", up: 50, down: 60, delta: time.Second}, {op: "collect", recordFail: true}, {op: "query"}, {op: "current", getFail: true}, {op: "current", up: 1, down: 2, delta: time.Second}, {op: "collect"}, {op: "start"}, {op: "loop"}, {op: "stop"}}
	for i, s := range steps {
		a, b := g.step(s, runGoTrafficService), r.step(s, run)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("workflow step %d differs %v/%v", i, a, b)
		}
		as, bs := authoritySnapshot(t, g.a.service), authoritySnapshot(t, r.a.service)
		as.Epoch = 0
		bs.Epoch = 0
		if as != bs {
			t.Fatalf("authority step %d differs %+v/%+v", i, as, bs)
		}
	}
}
func TestServiceStateSafeTrafficLifecycle(t *testing.T) {
	authorityTrafficGolden(t, newSelectedServiceStateBackend, runSelectedTrafficService)
	authorityTrafficCompare(t, newSelectedServiceStateBackend, runSelectedTrafficService)
}
func authorityClosedGolden(t *testing.T, factory authorityFactory, run authorityTrafficRun) {
	f := authorityTrafficNew(t, factory)
	a := f.a
	old := make(chan struct{})
	a.trafficHistoryCancel = old
	owner := a.service
	a.mu.Lock()
	err := a.closeServiceStateLocked()
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	e := newAppTrafficEffects(a, trafficServiceRequest{Op: "loop"}, old)
	e.tickerStart = func() {}
	stops := 0
	e.tickerStop = func() { stops++ }
	wait := 0
	e.wait = func() bool { wait++; return wait == 1 }
	e.get = func() (kernelTraffic, error) { t.Fatal("stale loop attempted GET"); return kernelTraffic{}, nil }
	r := run(trafficServiceRequest{Op: "loop"}, e)
	if r.Err != nil || stops != 1 || a.service != owner {
		t.Fatal("old queued tick touched closed authority or recreated", r)
	}
	for _, path := range []string{"/api/traffic", "/api/traffic/history"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if path == "/api/traffic" {
			a.traffic(w, req)
		} else {
			a.trafficHistory(w, req)
		}
		if w.Code != 500 || a.service != owner {
			t.Fatal("closed HTTP silently served stale state", w.Code)
		}
		if !a.mu.TryLock() {
			t.Fatal("error response leaked mutex")
		}
		a.mu.Unlock()
	}
}
func TestServiceStateSafeClosedRoutes(t *testing.T) {
	authorityClosedGolden(t, newSelectedServiceStateBackend, runSelectedTrafficService)
}
