package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type trafficDiffSpec struct {
	Store, Cancel, Running, GetFail, RecordFail, ReadFail bool
	Events                                                []bool
	ReplaceAt                                             int
	Error                                                 string
	Times                                                 []time.Time
	Snapshot                                              kernelTraffic
}
type trafficDiffFrame struct {
	Effect   trafficEffect
	State    trafficServiceState
	Baseline trafficSample
	Started  time.Time
	Owned    bool
}
type trafficDiffFX struct {
	e                                                 *appTrafficEffects
	a                                                 *app
	trace                                             []trafficDiffFrame
	calls                                             []string
	ticks, stops, spawns, records, reads, gets, waits int
	sessions                                          []string
	nowCalls                                          int
	spec                                              trafficDiffSpec
}

func trafficDiffBase() trafficDiffSpec {
	return trafficDiffSpec{Store: true, Running: true, Error: "old sampling error", ReplaceAt: -1, Events: []bool{true, true, false}, Times: []time.Time{time.Date(2026, 3, 8, 1, 59, 59, 123, time.FixedZone("EST", -5*3600)), time.Date(2026, 3, 8, 3, 0, 1, 456, time.FixedZone("EDT", -4*3600))}, Snapshot: kernelTraffic{UploadTotal: 100, DownloadTotal: 200, Connections: []kernelConnection{{ID: "synthetic", Upload: 9, Download: 8, Metadata: kernelMetadata{Process: "fake"}}}}}
}
func trafficDiffNew(q trafficServiceRequest, s trafficDiffSpec) *trafficDiffFX {
	a := testSeedTraffic(&app{started: time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("X", 3600))}, trafficSample{at: s.Times[0].Add(-time.Second), up: 10, down: 20}, s.Error)
	if s.Store {
		a.store = &store{}
	}
	var cancel chan struct{}
	if s.Cancel || q.Op == "loop" {
		cancel = make(chan struct{})
		a.trafficHistoryCancel = cancel
	}
	e := newAppTrafficEffects(a, q, cancel)
	f := &trafficDiffFX{a: a, e: e, spec: s}
	e.running = func() bool { return s.Running }
	e.now = func() time.Time {
		i := f.nowCalls
		f.nowCalls++
		if i >= len(s.Times) {
			i = len(s.Times) - 1
		}
		return s.Times[i]
	}
	e.get = func() (kernelTraffic, error) {
		f.gets++
		if s.GetFail {
			return kernelTraffic{}, errors.New("fake GET failure")
		}
		return s.Snapshot, nil
	}
	e.record = func(session string, k kernelTraffic, n time.Time) error {
		f.records++
		f.sessions = append(f.sessions, session)
		if s.RecordFail {
			return errors.New("fake write failure")
		}
		return nil
	}
	e.read = func(r trafficHistoryReport, key string) (trafficHistoryReport, error) {
		f.reads++
		r.Rows = []trafficHistoryRow{{AppKey: key, Upload: 1 << 60}}
		r.UploadTotal = 1 << 60
		if s.ReadFail {
			return r, errors.New("fake read failure")
		}
		return r, nil
	}
	e.spawn = func(c chan struct{}) {
		if c == nil || a.trafficHistoryCancel != c {
			panic("spawn before publish")
		}
		f.spawns++
	}
	e.tickerStart = func() { f.ticks++ }
	e.tickerStop = func() { f.stops++ }
	e.wait = func() bool {
		i := f.waits
		f.waits++
		if i == s.ReplaceAt {
			a.trafficHistoryCancel = make(chan struct{})
			a.started = a.started.Add(time.Hour)
		}
		if i >= len(s.Events) {
			return false
		}
		return s.Events[i]
	}
	return f
}
func (f *trafficDiffFX) Call(e trafficEffect) trafficReply {
	f.calls = append(f.calls, e.Kind)
	r := f.e.Call(e)
	f.trace = append(f.trace, trafficDiffFrame{Effect: e, State: *f.e.snapshot(), Baseline: testTrafficBaseline(f.a), Started: f.a.started, Owned: f.e.owned})
	return r
}
func (f *trafficDiffFX) Release() { f.e.Release() }
func trafficDiffRun(t *testing.T, q trafficServiceRequest, s trafficDiffSpec, run func(trafficServiceRequest, trafficServiceEffects) trafficServiceResult) (trafficServiceResult, *trafficDiffFX) {
	t.Helper()
	f := trafficDiffNew(q, s)
	if f.e.borrowed {
		f.a.mu.Lock()
	}
	r := run(q, f)
	f.Release()
	if f.e.borrowed {
		if f.a.mu.TryLock() {
			f.a.mu.Unlock()
			t.Fatal("borrowed lock released")
		}
		f.a.mu.Unlock()
	}
	if !f.a.mu.TryLock() {
		t.Fatal("owned lock leaked")
	}
	f.a.mu.Unlock()
	return r, f
}
func trafficDiffCompare(t *testing.T, q trafficServiceRequest, s trafficDiffSpec, run func(trafficServiceRequest, trafficServiceEffects) trafficServiceResult) {
	t.Helper()
	gr, g := trafficDiffRun(t, q, s, runGoTrafficService)
	rr, r := trafficDiffRun(t, q, s, run)
	if fmt.Sprint(gr.Err) != fmt.Sprint(rr.Err) {
		t.Fatalf("error mismatch %v %v", gr.Err, rr.Err)
	}
	gr.Err = nil
	rr.Err = nil
	if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.trace, r.trace) || !reflect.DeepEqual(g.sessions, r.sessions) || g.stops != r.stops || g.spawns != r.spawns || g.nowCalls != r.nowCalls {
		t.Fatalf("op=%s result Go=%#v Rust=%#v\nGo trace=%#v\nRust trace=%#v", q.Op, gr, rr, g.trace, r.trace)
	}
}
func TestMigrationTrafficSafeSelectedLifecycle(t *testing.T) {
	for _, op := range []string{"start", "stop", "collect", "loop", "current", "query"} {
		s := trafficDiffBase()
		s.Cancel = op == "stop"
		trafficDiffCompare(t, trafficServiceRequest{Op: op}, s, runSelectedTrafficService)
	}
}
func TestMigrationTrafficHTTPRejections(t *testing.T) {
	for _, q := range []string{"period=week", "period=day%20", "date=not-date", "group=APP", "offset=-1", "limit=0", "limit=201", "offset=%201", "offset=999999999999999999999999999999", "app=raw%20key"} {
		a := &app{}
		w := httptest.NewRecorder()
		a.trafficHistory(w, httptest.NewRequest("GET", "/api/traffic/history?"+q, nil))
		want := 400
		if q == "app=raw%20key" {
			want = 503
		}
		if w.Code != want {
			t.Fatalf("query %q status %d want %d", q, w.Code, want)
		}
		if !a.mu.TryLock() {
			t.Fatal("HTTP lock leak")
		}
		a.mu.Unlock()
	}
}
func trafficLegacyGoldens(t *testing.T, run func(trafficServiceRequest, trafficServiceEffects) trafficServiceResult) {
	for _, op := range []string{"start", "stop"} {
		s := trafficDiffBase()
		s.Cancel = op == "stop"
		_, f := trafficDiffRun(t, trafficServiceRequest{Op: op}, s, run)
		if f.gets != 0 || f.records != 0 || f.ticks != 0 {
			t.Fatal("start/stop sampled or created ticker")
		}
		if op == "start" && f.spawns != 1 {
			t.Fatal("missing publication/spawn")
		}
		if op == "stop" && f.a.trafficHistoryCancel != nil {
			t.Fatal("cancel not cleared")
		}
	}
	for _, replacement := range []int{-1, 0, 1} {
		s := trafficDiffBase()
		s.ReplaceAt = replacement
		_, f := trafficDiffRun(t, trafficServiceRequest{Op: "loop"}, s, run)
		want := 2
		if replacement >= 0 {
			want = replacement
		}
		if f.gets != want || f.stops != 1 || f.ticks != 1 {
			t.Fatalf("old loop: replacement %d gets=%d stop=%d", replacement, f.gets, f.stops)
		}
		if f.a.trafficHistoryCancel == nil {
			t.Fatal("old loop cleared current cancel")
		}
	}
	for mode := 0; mode < 4; mode++ {
		s := trafficDiffBase()
		if mode == 0 {
			s.Store = false
		}
		if mode == 1 {
			s.Running = false
		}
		if mode == 2 {
			s.GetFail = true
		}
		if mode == 3 {
			s.RecordFail = true
		}
		_, f := trafficDiffRun(t, trafficServiceRequest{Op: "collect"}, s, run)
		if mode < 2 && testTrafficError(f.a) != s.Error {
			t.Fatal("skip changed old error")
		}
		if mode == 2 && (f.records != 0 || f.nowCalls != 0) {
			t.Fatal("GET error queried clock/store")
		}
		if mode == 3 && testTrafficError(f.a) != trafficCollectMessage1() {
			t.Fatal("store error priority")
		}
	}
	s := trafficDiffBase()
	_, f := trafficDiffRun(t, trafficServiceRequest{Op: "collect"}, s, run)
	if testTrafficError(f.a) != "" || f.records != 1 || f.sessions[0] != f.a.started.Format(time.RFC3339Nano) || !reflect.DeepEqual(f.calls, []string{"snapshot", "running", "get", "now", "record", "error"}) {
		t.Fatal("collect sequence/session")
	}
	for mode := 0; mode < 3; mode++ {
		s := trafficDiffBase()
		s.Running = mode != 0
		s.GetFail = mode == 1
		r, f := trafficDiffRun(t, trafficServiceRequest{Op: "current"}, s, run)
		if mode == 0 && testTrafficBaseline(f.a).up != 10 {
			t.Fatal("stopped baseline lost")
		}
		if mode == 1 && !testTrafficBaseline(f.a).at.IsZero() {
			t.Fatal("failed GET baseline not cleared")
		}
		if mode == 2 {
			if r.Data["uploadRate"] != int64(90) || r.Data["downloadRate"] != int64(180) || !reflect.DeepEqual(f.calls, []string{"lock", "running", "get", "now", "rate", "baseline", "rows"}) {
				t.Fatalf("current order/rate %#v %v", r.Data, f.calls)
			}
		}
	}
	s = trafficDiffBase()
	r, f := trafficDiffRun(t, trafficServiceRequest{Op: "query", App: " raw "}, s, run)
	if r.Report.Date != "2026-03-08" || r.Report.Timezone != "EDT -04:00" || r.Report.Rows[0].AppKey != " raw " || r.Report.Limit != 100 || r.Report.UploadTotal != 1<<60 || f.nowCalls != 2 {
		t.Fatalf("query clocks/default/raw/int64 %#v", r.Report)
	}
	s.Times = []time.Time{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.FixedZone("NEXT", 3600))}
	r, _ = trafficDiffRun(t, trafficServiceRequest{Op: "query"}, s, run)
	if r.Report.Date != "2026-12-31" || r.Report.Timezone != "NEXT +01:00" {
		t.Fatal("two independent midnight clocks collapsed")
	}
}
func TestMigrationTrafficSafeLegacyGoldens(t *testing.T) {
	trafficLegacyGoldens(t, runSelectedTrafficService)
}
func trafficLifecycleGolden(t *testing.T, run func(trafficServiceRequest, trafficServiceEffects) trafficServiceResult) {
	f := trafficDiffNew(trafficServiceRequest{Op: "start"}, trafficDiffBase())
	f.a.mu.Lock()
	run(trafficServiceRequest{Op: "start"}, f)
	if f.spawns != 1 || f.gets != 0 {
		t.Fatal("initial start sampled")
	}
	cancel := f.a.trafficHistoryCancel
	f.a.mu.Unlock()
	f.e.borrowed = false
	f.e.loopCancel = cancel
	run(trafficServiceRequest{Op: "loop"}, f)
	if f.gets != 2 || f.records != 2 || f.stops != 1 {
		t.Fatal("loop lifecycle incomplete")
	}
	f.e.borrowed = true
	f.a.mu.Lock()
	run(trafficServiceRequest{Op: "stop"}, f)
	if f.a.trafficHistoryCancel != nil || f.records != 2 {
		t.Fatal("stop sampled/wrong handle")
	}
	select {
	case <-cancel:
	default:
		t.Fatal("cancel channel not closed")
	}
	if f.a.mu.TryLock() {
		f.a.mu.Unlock()
		t.Fatal("stop released borrowed lock")
	}
	f.a.mu.Unlock()
}
func TestMigrationTrafficSafeSharedLifecycle(t *testing.T) {
	trafficLifecycleGolden(t, runSelectedTrafficService)
}
func TestMigrationTrafficSafeQueryPriority(t *testing.T) {
	for _, tc := range []struct {
		q      trafficServiceRequest
		calls  []string
		status int
	}{{trafficServiceRequest{Op: "query", Period: "invalid", Group: "invalid", Limit: "bad"}, []string{"date"}, 400}, {trafficServiceRequest{Op: "query", Group: "invalid", Limit: "bad"}, []string{"date"}, 400}, {trafficServiceRequest{Op: "query", Limit: "bad"}, []string{"date", "pagination"}, 400}, {trafficServiceRequest{Op: "query"}, []string{"date", "pagination", "lock"}, 503}} {
		s := trafficDiffBase()
		s.Store = false
		r, f := trafficDiffRun(t, tc.q, s, runSelectedTrafficService)
		if r.Status != tc.status || !reflect.DeepEqual(f.calls, tc.calls) {
			t.Fatalf("priority result %d calls %v", r.Status, f.calls)
		}
	}
}
