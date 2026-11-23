package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func safeTrafficFX(a *app, q trafficServiceRequest, cancel chan struct{}) *appTrafficEffects {
	e := newAppTrafficEffects(a, q, cancel)
	e.now = func() time.Time { return time.Unix(100000, 0) }
	e.running = func() bool { return true }
	e.get = func() (kernelTraffic, error) { return kernelTraffic{}, nil }
	e.record = func(string, kernelTraffic, time.Time) error { return nil }
	e.read = func(r trafficHistoryReport, _ string) (trafficHistoryReport, error) {
		r.Rows = []trafficHistoryRow{}
		return r, nil
	}
	e.spawn = func(chan struct{}) {}
	e.tickerStart = func() {}
	e.tickerStop = func() {}
	e.wait = func() bool { return false }
	return e
}
func TestTrafficServiceCurrentBaseline(t *testing.T) {
	a := testSeedTraffic(&app{}, trafficSample{at: time.Unix(99999, 0), up: 10, down: 20}, "")
	q := trafficServiceRequest{Op: "current"}
	e := safeTrafficFX(a, q, nil)
	e.running = func() bool { return false }
	runGoTrafficService(q, e)
	if testTrafficBaseline(a).up != 10 {
		t.Fatal("stopped cleared baseline")
	}
	e = safeTrafficFX(a, q, nil)
	e.get = func() (kernelTraffic, error) { return kernelTraffic{}, errors.New("fake") }
	runGoTrafficService(q, e)
	if !testTrafficBaseline(a).at.IsZero() {
		t.Fatal("GET failure retained baseline")
	}
	e = safeTrafficFX(a, q, nil)
	e.get = func() (kernelTraffic, error) {
		return kernelTraffic{UploadTotal: 100, DownloadTotal: 200, Connections: []kernelConnection{{Upload: 8}}}, nil
	}
	r := runGoTrafficService(q, e)
	if r.Data["uploadRate"] != int64(0) || testTrafficBaseline(a).up != 100 || r.Data["connectionCount"] != 1 {
		t.Fatal("current sample")
	}
	if !a.mu.TryLock() {
		t.Fatal("current lock leaked")
	}
	a.mu.Unlock()
}
func TestTrafficServiceCollectErrorsAndSession(t *testing.T) {
	for _, mode := range []string{"nostore", "stopped", "get", "record", "success"} {
		t.Run(mode, func(t *testing.T) {
			a := testSeedTraffic(&app{store: &store{}}, trafficSample{}, "prior")
			if mode == "nostore" {
				a.store = nil
			}
			q := trafficServiceRequest{Op: "collect"}
			e := safeTrafficFX(a, q, nil)
			gets, records, clocks := 0, 0, 0
			e.running = func() bool { return mode != "stopped" }
			e.now = func() time.Time { clocks++; return time.Unix(123, 0) }
			e.get = func() (kernelTraffic, error) {
				gets++
				a.started = time.Unix(456, 0)
				if mode == "get" {
					return kernelTraffic{}, errors.New("fake")
				}
				return kernelTraffic{}, nil
			}
			e.record = func(session string, _ kernelTraffic, _ time.Time) error {
				records++
				if session != a.started.Format(time.RFC3339Nano) {
					t.Fatal("stale session")
				}
				if mode == "record" {
					return errors.New("fake")
				}
				return nil
			}
			a.mu.Lock()
			runGoTrafficService(q, e)
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Fatal("borrowed released")
			}
			a.mu.Unlock()
			if mode == "nostore" || mode == "stopped" {
				if testTrafficError(a) != "prior" || gets != 0 || records != 0 || clocks != 0 {
					t.Fatal("skip mutated")
				}
			} else if mode == "get" {
				if clocks != 0 || records != 0 || testTrafficError(a) != trafficCollectMessage0() {
					t.Fatal("get failure")
				}
			} else if mode == "record" {
				if testTrafficError(a) != trafficCollectMessage1() {
					t.Fatal("record failure")
				}
			} else if testTrafficError(a) != "" || clocks != 1 || records != 1 {
				t.Fatal("success")
			}
		})
	}
}
func TestTrafficServiceStartStopIdentity(t *testing.T) {
	a := &app{store: &store{}}
	q := trafficServiceRequest{Op: "start"}
	e := safeTrafficFX(a, q, nil)
	spawns := 0
	var first chan struct{}
	e.spawn = func(c chan struct{}) {
		spawns++
		first = c
		if a.trafficHistoryCancel != c {
			t.Fatal("spawn before publish")
		}
	}
	e.get = func() (kernelTraffic, error) { t.Fatal("start collected"); return kernelTraffic{}, nil }
	a.mu.Lock()
	runGoTrafficService(q, e)
	runGoTrafficService(q, e)
	a.mu.Unlock()
	if spawns != 1 || first == nil {
		t.Fatal("start idempotence")
	}
	q.Op = "stop"
	e = safeTrafficFX(a, q, nil)
	e.get = func() (kernelTraffic, error) { t.Fatal("stop collected"); return kernelTraffic{}, nil }
	a.mu.Lock()
	runGoTrafficService(q, e)
	runGoTrafficService(q, e)
	a.mu.Unlock()
	select {
	case <-first:
	default:
		t.Fatal("not closed")
	}
	if a.trafficHistoryCancel != nil {
		t.Fatal("not cleared")
	}
}
func TestTrafficServiceLoopStaleAndCleanup(t *testing.T) {
	old, newer := make(chan struct{}), make(chan struct{})
	a := &app{store: &store{}, trafficHistoryCancel: old}
	q := trafficServiceRequest{Op: "loop"}
	e := safeTrafficFX(a, q, old)
	events, gets, stops := 0, 0, 0
	e.wait = func() bool {
		events++
		if events == 2 {
			a.mu.Lock()
			a.trafficHistoryCancel = newer
			a.mu.Unlock()
		}
		return true
	}
	e.get = func() (kernelTraffic, error) {
		gets++
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("GET outside mutex")
		}
		return kernelTraffic{}, nil
	}
	e.tickerStop = func() { stops++ }
	runGoTrafficService(q, e)
	e.Release()
	if events != 2 || gets != 1 || stops != 1 || a.trafficHistoryCancel != newer {
		t.Fatal("stale lifecycle")
	}
	select {
	case <-newer:
		t.Fatal("closed new loop")
	default:
	}
	if !a.mu.TryLock() {
		t.Fatal("loop leaked mutex")
	}
	a.mu.Unlock()
}
func TestTrafficServiceLoopPanic(t *testing.T) {
	c := make(chan struct{})
	a := &app{store: &store{}, trafficHistoryCancel: c}
	q := trafficServiceRequest{Op: "loop"}
	e := safeTrafficFX(a, q, c)
	stops := 0
	e.wait = func() bool { return true }
	e.get = func() (kernelTraffic, error) { panic("fake") }
	e.tickerStop = func() { stops++ }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		runGoTrafficService(q, e)
	}()
	e.Release()
	if stops != 1 {
		t.Fatal("ticker cleanup")
	}
	if !a.mu.TryLock() {
		t.Fatal("panic lock leak")
	}
	a.mu.Unlock()
	select {
	case <-c:
		t.Fatal("loop closed shared cancel")
	default:
	}
}
func TestTrafficServiceQueryClocks(t *testing.T) {
	a := testSeedTraffic(&app{store: &store{}}, trafficSample{}, "prior")
	q := trafficServiceRequest{Op: "query", Limit: "+1", Offset: "0", App: " raw "}
	e := safeTrafficFX(a, q, nil)
	times := []time.Time{time.Date(2026, 1, 1, 23, 59, 59, 0, time.FixedZone("A", 0)), time.Date(2026, 1, 2, 0, 0, 1, 0, time.FixedZone("B", 3600))}
	i := 0
	e.now = func() time.Time { x := times[i]; i++; return x }
	e.read = func(r trafficHistoryReport, key string) (trafficHistoryReport, error) {
		if key != " raw " || r.Date != "2026-01-01" || r.Timezone != "B +01:00" || r.SamplingError != "prior" || r.Limit != 1 {
			t.Fatal("query changed", r)
		}
		r.Rows = []trafficHistoryRow{}
		return r, nil
	}
	r := runGoTrafficService(q, e)
	if r.Err != nil || i != 2 || r.Report.Rows == nil {
		t.Fatal("query clocks")
	}
	bad := []trafficServiceRequest{{Op: "query", Period: "bad", Group: "bad"}, {Op: "query", Group: "bad", Limit: "0"}, {Op: "query", Limit: "0"}}
	want := []string{mustHistoryDateError("bad"), trafficHistoryMessage0(), trafficHistoryMessage1()}
	for j, q := range bad {
		e = safeTrafficFX(a, q, nil)
		r = runGoTrafficService(q, e)
		if !reflect.DeepEqual(r.Message, want[j]) {
			t.Fatal("validation priority")
		}
	}
}
func mustHistoryDateError(period string) string {
	_, err := historyDate(period, "", time.Unix(0, 0))
	return err.Error()
}
func TestTrafficServiceTickerCreationPanic(t *testing.T) {
	a := &app{}
	q := trafficServiceRequest{Op: "loop"}
	e := safeTrafficFX(a, q, make(chan struct{}))
	created, closed := false, 0
	e.tickerStart = func() { created = true; panic("after synthetic create") }
	e.tickerStop = func() {
		if !created {
			t.Fatal("stopping absent resource")
		}
		closed++
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected creator panic")
			}
		}()
		runGoTrafficService(q, e)
	}()
	e.Release()
	if closed != 1 || e.tickerOwned {
		t.Fatal("creator resource leaked or stopped twice")
	}
}
