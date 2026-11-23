package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type monitorDiffSpec struct {
	Running, Enabled, Busy, Cancel, Old, FetchFail, SaveFail, ReplaceFetch, Grow bool
	Events                                                                       []string
	ReplaceWait                                                                  int
	Step                                                                         time.Duration
	Traffic                                                                      kernelTraffic
	Retry                                                                        string
}
type monitorDiffFrame struct {
	Effect pathMonitorServiceEffect
	State  *pathMonitorServiceState
	Locked bool
}
type monitorDiffFX struct {
	a                             *app
	e                             *appPathMonitorEffects
	w                             *httptest.ResponseRecorder
	trace                         []monitorDiffFrame
	timers, stops, work           []string
	queue                         []func()
	waits, clocks, saves, fetches int
	q                             pathMonitorServiceRequest
}

var monitorDiffNow = time.Date(2026, 7, 8, 9, 10, 11, 123, time.FixedZone("X", 3600))

func monitorDiffBase() monitorDiffSpec {
	return monitorDiffSpec{Running: true, Enabled: true, Cancel: true, ReplaceWait: -1, Step: time.Second, Events: []string{"baseline", "demand", "retry", "cancel"}, Traffic: kernelTraffic{Connections: []kernelConnection{{ID: "new", Upload: 1, Metadata: kernelMetadata{Host: "sub.synthetic.invalid", Process: "browser"}}}}, Retry: monitorDiffNow.Format(time.RFC3339Nano)}
}
func monitorDiffRequest(op string) pathMonitorServiceRequest {
	return pathMonitorServiceRequest{Op: op, Guard: op == "loop", Interval: 120 * time.Second, Now: monitorDiffNow, Settings: pathCheckSettings{Enabled: true, IntervalSec: 45, Targets: []pathTarget{{URL: "https://synthetic.invalid", Enabled: true}}}, Previous: map[string]pathConnectionSample{"old": {upload: 7}}}
}
func monitorDiffNew(q pathMonitorServiceRequest, s monitorDiffSpec) *monitorDiffFX {
	a := &app{}
	testEditPath(a, func(p *servicePathState) {
		p.Config = pathCheckSettings{Enabled: s.Enabled, IntervalSec: 120, Targets: append(append([]pathTarget(nil), pathCheckPresets...), pathTarget{Label: "Synthetic", URL: "https://synthetic.invalid", Enabled: true})}
	})
	testEditPath(a, func(p *servicePathState) { p.Busy = s.Busy })
	testEditPath(a, func(p *servicePathState) {
		p.Status = pathStatus{Demanded: true, Alert: "old", RetryMode: "scan", RetryAt: s.Retry}
	})
	var token chan struct{}
	if s.Cancel {
		token = make(chan struct{})
		a.pathCancel = token
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
	}
	if s.Old {
		token = make(chan struct{})
	}
	w := httptest.NewRecorder()
	e := newAppPathMonitorEffects(a, q, w, token)
	f := &monitorDiffFX{a: a, e: e, w: w, q: q}
	e.hooks = map[string]func(pathMonitorServiceEffect) pathMonitorServiceReply{}
	e.now = func() time.Time { at := monitorDiffNow.Add(time.Duration(f.clocks) * s.Step); f.clocks++; return at }
	e.hooks["running"] = func(pathMonitorServiceEffect) pathMonitorServiceReply {
		return pathMonitorServiceReply{Bool: s.Running}
	}
	e.hooks["save"] = func(pathMonitorServiceEffect) pathMonitorServiceReply {
		f.saves++
		if s.SaveFail {
			return pathMonitorServiceReply{Err: errors.New("synthetic save")}
		}
		return pathMonitorServiceReply{}
	}
	e.hooks["log"] = func(pathMonitorServiceEffect) pathMonitorServiceReply { return pathMonitorServiceReply{} }
	e.hooks["wait"] = func(pathMonitorServiceEffect) pathMonitorServiceReply {
		i := f.waits
		f.waits++
		if i == s.ReplaceWait {
			a.mu.Lock()
			a.pathCancel = make(chan struct{})
			a.mu.Unlock()
		}
		if i >= len(s.Events) {
			return pathMonitorServiceReply{Text: "cancel"}
		}
		return pathMonitorServiceReply{Text: s.Events[i]}
	}
	e.fetch = func(int, string) (kernelTraffic, error) {
		if !a.mu.TryLock() {
			panic("traffic fetched with lock")
		}
		if s.ReplaceFetch {
			a.pathCancel = make(chan struct{})
			testEditPath(a, func(p *servicePathState) { p.Status.Note = "concurrent edit" })
		}
		a.mu.Unlock()
		f.fetches++
		if s.FetchFail {
			return kernelTraffic{}, errors.New("synthetic GET")
		}
		v := s.Traffic
		v.Connections = append([]kernelConnection(nil), v.Connections...)
		if s.Grow {
			for i := range v.Connections {
				v.Connections[i].Upload += int64(f.fetches)
			}
		}
		return v, nil
	}
	e.makeTicker = func(n string, d time.Duration, register func(<-chan time.Time, func())) {
		f.timers = append(f.timers, fmt.Sprintf("%s:%s", n, d))
		register(make(chan time.Time), func() { f.stops = append(f.stops, n) })
	}
	e.launch = func(fn func()) { f.work = append(f.work, "queued"); f.queue = append(f.queue, fn) }
	e.roundRunner = func(mode string, guard bool, t <-chan struct{}) { f.work = append(f.work, "round:"+mode) }
	e.loopRunner = func(t <-chan struct{}, d time.Duration) { f.work = append(f.work, "loop") }
	return f
}
func monitorCopySamples(in map[string]pathConnectionSample) map[string]pathConnectionSample {
	if in == nil {
		return nil
	}
	out := map[string]pathConnectionSample{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func (f *monitorDiffFX) Call(x pathMonitorServiceEffect) pathMonitorServiceReply {
	if f.e.hooks[x.Kind] == nil {
		switch x.Kind {
		case "lock", "unlock", "snapshot", "constants", "now", "since", "parse-time", "publish", "close", "ticker-new", "ticker-stop", "spawn-loop", "round", "capture", "traffic", "url", "demand-state", "throttle-read", "throttle-write", "samples", "settings", "set", "response":
		default:
			panic("unmocked scheduler port " + x.Kind)
		}
	}
	x.Samples = monitorCopySamples(x.Samples)
	x.Settings.Targets = append([]pathTarget(nil), x.Settings.Targets...)
	r := f.e.Call(x)
	f.trace = append(f.trace, monitorDiffFrame{x, f.e.snapshot(), f.e.locked})
	return r
}
func (f *monitorDiffFX) Release() { f.e.Release() }
func monitorDiffRun(t *testing.T, q pathMonitorServiceRequest, s monitorDiffSpec, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult) (pathMonitorServiceResult, *monitorDiffFX) {
	t.Helper()
	f := monitorDiffNew(q, s)
	borrowed := q.Op == "start" || q.Op == "stop"
	if borrowed {
		f.a.mu.Lock()
	}
	r := run(q, f)
	f.Release()
	if borrowed {
		if f.a.mu.TryLock() {
			f.a.mu.Unlock()
			t.Fatal("borrowed lock released")
		}
		f.a.mu.Unlock()
	}
	if !f.a.mu.TryLock() {
		t.Fatal("scheduler lock leaked")
	}
	f.a.mu.Unlock()
	for _, work := range f.queue {
		work()
	}
	return r, f
}
func monitorDiffCompare(t *testing.T, op string, s monitorDiffSpec, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult) {
	t.Helper()
	a, x := monitorDiffRun(t, monitorDiffRequest(op), s, runGoPathMonitorService)
	b, y := monitorDiffRun(t, monitorDiffRequest(op), s, run)
	if fmt.Sprint(a.Err) != fmt.Sprint(b.Err) {
		t.Fatal("errors", a.Err, b.Err)
	}
	a.Err = nil
	b.Err = nil
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.work, y.work) || !reflect.DeepEqual(x.stops, y.stops) || !reflect.DeepEqual(x.q.Previous, y.q.Previous) || x.w.Body.String() != y.w.Body.String() {
		t.Fatalf("op=%s\nGo=%#v Rust=%#v\nGoTrace=%#v\nRustTrace=%#v\nwork=%v/%v", op, a, b, x.trace, y.trace, x.work, y.work)
	}
}
func TestMigrationPathMonitorSafeSelectedLifecycle(t *testing.T) {
	for _, op := range []string{"start", "stop", "loop", "demand", "retry", "normalize", "update", "snapshot", "manual", "cancel"} {
		s := monitorDiffBase()
		if op == "start" {
			s.Cancel = false
		}
		monitorDiffCompare(t, op, s, runSelectedPathMonitorService)
	}
}
func monitorLegacyGoldens(t *testing.T, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult) {
	s := monitorDiffBase()
	s.Cancel = false
	_, f := monitorDiffRun(t, monitorDiffRequest("start"), s, run)
	if f.a.pathCancel == nil || !reflect.DeepEqual(f.work, []string{"queued", "loop"}) || len(f.timers) != 0 {
		t.Fatal("start publication/asynchronous loop")
	}
	s = monitorDiffBase()
	_, f = monitorDiffRun(t, monitorDiffRequest("stop"), s, run)
	if f.a.pathCancel != nil || len(f.work) != 0 || len(f.timers) != 0 || f.a.pathStatusLocked().RetryAt != "" {
		t.Fatal("stop waited/scheduled/retained retry")
	}
	s = monitorDiffBase()
	s.Events = []string{"baseline", "cancel"}
	_, f = monitorDiffRun(t, monitorDiffRequest("loop"), s, run)
	if !reflect.DeepEqual(f.timers, []string{"baseline:2m0s", "demand:1s", "retry:1s"}) || !reflect.DeepEqual(f.stops, []string{"retry", "demand", "baseline"}) || !reflect.DeepEqual(f.work, []string{"queued", "round:baseline", "round:baseline"}) {
		t.Fatal("scheduler async initial/sync tick/reverse timers", f.work, f.stops, f.timers)
	}
	s = monitorDiffBase()
	s.SaveFail = true
	q := monitorDiffRequest("update")
	_, f = monitorDiffRun(t, q, s, run)
	if f.w.Code != 500 || f.a.pathConfigLocked().IntervalSec != 45 || f.a.pathCancel == nil || len(f.work) != 0 {
		t.Fatal("save failure rollback/restart")
	}
	s = monitorDiffBase()
	s.FetchFail = true
	q = monitorDiffRequest("demand")
	_, f = monitorDiffRun(t, q, s, run)
	if !reflect.DeepEqual(f.q.Previous, map[string]pathConnectionSample{"old": {upload: 7}}) {
		t.Fatal("GET failure replaced previous")
	}
	s = monitorDiffBase()
	s.ReplaceFetch = true
	q = monitorDiffRequest("demand")
	q.Guard = true
	r, f := monitorDiffRun(t, q, s, run)
	if len(r.Labels) != 0 || !reflect.DeepEqual(f.q.Previous, map[string]pathConnectionSample{"old": {upload: 7}}) || f.a.pathStatusLocked().Note != "concurrent edit" {
		t.Fatal("late old GET mutated state")
	}
	for _, event := range []string{"baseline", "demand", "retry"} {
		s = monitorDiffBase()
		s.Events = []string{event, "cancel"}
		s.ReplaceWait = 0
		_, f = monitorDiffRun(t, monitorDiffRequest("loop"), s, run)
		if len(f.work) != 2 || f.work[0] != "queued" || f.work[1] != "round:baseline" || f.a.pathStatusLocked().RetryAt != s.Retry {
			t.Fatal("old loop scheduled/consumed replacement state", event, f.work)
		}
	}
	for _, delta := range []time.Duration{-1, 0, 1} {
		s = monitorDiffBase()
		q = monitorDiffRequest("retry")
		q.Now = monitorDiffNow.Add(delta)
		r, f = monitorDiffRun(t, q, s, run)
		if delta < 0 {
			if r.Mode != "" || f.a.pathStatusLocked().RetryAt == "" {
				t.Fatal("early retry consumed")
			}
		} else if r.Mode != "scan" || f.a.pathStatusLocked().RetryAt != "" {
			t.Fatal("exact-due retry missing")
		}
	}
}
func TestMigrationPathMonitorSafeLegacyGoldens(t *testing.T) {
	monitorLegacyGoldens(t, runSelectedPathMonitorService)
}
func monitorWorkerGolden(t *testing.T, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult) {
	monitorWorkerGoldenWithFactory(t, run, nil)
}
func monitorWorkerGoldenWithFactory(t *testing.T, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult, factory authorityFactory) {
	for _, old := range []bool{true, false} {
		s := monitorDiffBase()
		s.Events = []string{"cancel"}
		q := monitorDiffRequest("loop")
		f := monitorDiffNew(q, s)
		if factory != nil {
			pathAuthoritySeed(t, f.a, factory)
		}
		called := 0
		entered := make(chan struct{})
		finish := make(chan struct{})
		done := make(chan struct{})
		f.e.roundRunner = func(mode string, guard bool, token <-chan struct{}) {
			if !guard {
				panic("worker lost guard")
			}
			withPathMonitorRound(f.a, token, pathServiceRequest{Op: "baseline"}, func(fx *appPathEffects) {
				called++
				fx.Call(pathServiceEffect{Kind: "lock"})
				if f.a.mu.TryLock() {
					f.a.mu.Unlock()
					panic("first lock not atomically inherited")
				}
				fx.Call(pathServiceEffect{Kind: "unlock"})
				close(entered)
				<-finish
				fx.Call(pathServiceEffect{Kind: "lock"})
				testEditPath(f.a, func(p *servicePathState) { p.Status.Note = "inflight completed" })
				fx.Call(pathServiceEffect{Kind: "unlock"})
			})
		}
		run(q, f)
		f.Release()
		if len(f.queue) != 1 {
			t.Fatal("initial worker not queued")
		}
		if old {
			f.a.mu.Lock()
			f.a.pathCancel = make(chan struct{})
			f.a.mu.Unlock()
			f.queue[0]()
			if called != 0 {
				t.Fatal("old queued worker admitted")
			}
			continue
		}
		go func() { f.queue[0](); close(done) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("worker first lock deadlocked")
		}
		f.a.mu.Lock()
		f.a.pathCancel = make(chan struct{})
		f.a.mu.Unlock()
		close(finish)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("admitted worker blocked")
		}
		if called != 1 || f.a.pathStatusLocked().Note != "inflight completed" {
			t.Fatal("added inflight cancellation")
		}
	}
}
func TestMigrationPathMonitorSafeWorkerAdmission(t *testing.T) {
	monitorWorkerGolden(t, runSelectedPathMonitorService)
}
func monitorThrottleGolden(t *testing.T, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult) {
	for _, step := range []time.Duration{29 * time.Second, 30 * time.Second} {
		s := monitorDiffBase()
		s.Grow = true
		s.Step = step
		s.Events = []string{"demand", "demand", "cancel"}
		_, f := monitorDiffRun(t, monitorDiffRequest("loop"), s, run)
		n := 0
		for _, x := range f.trace {
			if x.Effect.Kind == "round" && x.Effect.Name == "demand" {
				n++
			}
		}
		want := 1
		if step == 30*time.Second {
			want = 2
		}
		if n != want {
			t.Fatalf("30s throttle boundary step=%s got=%d want=%d", step, n, want)
		}
	}
}
func TestMigrationPathMonitorSafeThrottle(t *testing.T) {
	monitorThrottleGolden(t, runSelectedPathMonitorService)
}
func monitorShapeGolden(t *testing.T, run func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult) {
	for _, empty := range []bool{false, true} {
		q := monitorDiffRequest("snapshot")
		f := monitorDiffNew(q, monitorDiffBase())
		testEditPath(f.a, func(p *servicePathState) { p.Config.Targets = nil })
		testEditPath(f.a, func(p *servicePathState) { p.Status.Results = nil })
		if empty {
			testEditPath(f.a, func(p *servicePathState) { p.Config.Targets = []pathTarget{} })
			testEditPath(f.a, func(p *servicePathState) { p.Status.Results = []siteCheckResult{} })
		}
		testEditPath(f.a, func(p *servicePathState) { p.Busy = true })
		testEditPath(f.a, func(p *servicePathState) { p.Status.Running = false })
		run(q, f)
		var body map[string]any
		if err := json.Unmarshal(f.w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		targets := body["settings"].(map[string]any)["targets"]
		results := body["status"].(map[string]any)["results"]
		if empty {
			if targets == nil || results == nil {
				t.Fatal("empty arrays changed to null")
			}
		} else if targets != nil || results != nil {
			t.Fatal("nil arrays changed")
		}
		if f.a.pathStatusLocked().Running || body["status"].(map[string]any)["running"] != true {
			t.Fatal("snapshot mutated stored Running")
		}
		if !f.a.mu.TryLock() {
			t.Fatal("snapshot lock leak")
		}
		f.a.mu.Unlock()
	}
}
func TestMigrationPathMonitorSafeSnapshotShape(t *testing.T) {
	monitorShapeGolden(t, runSelectedPathMonitorService)
}
