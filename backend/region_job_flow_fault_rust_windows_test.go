//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type regionJobInjectedSession struct {
	regionJobFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *regionJobInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.regionJobFlowSession.next()
	if e == nil {
		f, err := decodeRegionJobFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *regionJobInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		s.seen++
		if s.seen == s.nth {
			if s.nextFailure {
				s.pending = true
			} else {
				s.failed = true
				return errors.New("synthetic advance failure")
			}
		}
	}
	return s.regionJobFlowSession.advance(n, b)
}

type regionJobFaultFX struct {
	fx                                                  *appRegionJobEffects
	trace                                               []regionJobEffect
	panicAt                                             string
	count, puts, queries, stores, spawns, starts, stops int
	large                                               bool
}

func (f *regionJobFaultFX) Release() { f.fx.Release() }
func (f *regionJobFaultFX) Call(q regionJobEffect) regionJobReply {
	f.count++
	if !f.large {
		f.trace = append(f.trace, q)
	}
	r := f.fx.Call(q)
	if q.Kind == f.panicAt {
		panic("synthetic job panic")
	}
	return r
}
func newRegionJobFaultFX(n int) *regionJobFaultFX {
	a := testHealthApp(&app{store: &store{}}, func(p *healthDomainProjection) {
		p.Regions = map[string]regionRecord{}
		p.Job = &regionJob{Running: true, LastError: "prior"}
	})
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	fx := safeRegionJobFX(a)
	f := &regionJobFaultFX{fx: fx}
	fx.running = func() bool { return false }
	fx.start = func() error { f.starts++; return nil }
	fx.stop = func() error { f.stops++; return nil }
	fx.list = func() ([]proxyNode, error) {
		nodes := make([]proxyNode, n)
		for i := range nodes {
			nodes[i].Name = fmt.Sprintf("node%06d", i)
		}
		return nodes, nil
	}
	fx.delays = func(time.Duration) (map[string]int, error) {
		m := map[string]int{}
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("node%06d", i)] = 1
		}
		return m, nil
	}
	fx.mihomo = func(method, path string, body any) (map[string]any, error) {
		if method == "GET" {
			return map[string]any{"now": "original"}, nil
		}
		f.puts++
		return nil, nil
	}
	fx.query = func(geoSource, time.Duration) (string, string, error) { f.queries++; return "1.1.1.1", "US", nil }
	fx.save = func(regionRecord) error { f.stores++; return errors.New("synthetic save failure") }
	fx.spawn = func(bool) {
		f.spawns++
		a.mu.Lock()
		testEditHealth(a, func(p *healthDomainProjection) { p.Job.Done = 99 })
		a.mu.Unlock()
	}
	a.writeRegionRecordLocked("node000000", regionRecord{Status: regionVerified, VerifiedAt: time.Unix(100000, 0).Add(-regionTTL)})
	return f
}
func regionJobAssertUnlocked(t *testing.T, f *regionJobFaultFX) {
	t.Helper()
	if !f.fx.a.mu.TryLock() {
		t.Fatal("owned mutex leak")
	}
	f.fx.a.mu.Unlock()
}
func TestRegionJobFlowActualFaultsNoReplay(t *testing.T) {
	if !rustRegionJobFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 23; k++ {
		for _, next := range []bool{false, true} {
			op := "run"
			if k == 5 || k == 6 || k == 7 {
				op = "start"
			}
			if k == 3 {
				op = "finish"
			}
			x, y := newRegionJobFaultFX(2), newRegionJobFaultFX(2)
			if op == "start" {
				testEditHealth(x.fx.a, func(p *healthDomainProjection) { p.Job = nil })
				testEditHealth(y.fx.a, func(p *healthDomainProjection) { p.Job = nil })
			}
			if op == "finish" {
				x.fx.borrowed = true
				y.fx.borrowed = true
				x.fx.a.mu.Lock()
				y.fx.a.mu.Lock()
			}
			q := regionJobServiceRequest{Op: op}
			want := runGoRegionJobService(q, x)
			var inj *regionJobInjectedSession
			got, used := runRustRegionJobServiceWithStart(q, y, func(b []byte) (regionJobFlowSession, error) {
				s, e := startRustRegionJobFlow(b)
				if e != nil {
					return nil, e
				}
				inj = &regionJobInjectedSession{regionJobFlowSession: s, target: k, nth: 1, nextFailure: next}
				return inj, nil
			})
			if !used || inj == nil || !inj.failed || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.fx.a.healthDomainLocked().Job, y.fx.a.healthDomainLocked().Job) || !reflect.DeepEqual(x.fx.a.healthDomainLocked().Regions, y.fx.a.healthDomainLocked().Regions) || x.puts != y.puts || x.queries != y.queries || x.spawns != y.spawns || x.stores != y.stores || x.starts != y.starts || x.stops != y.stops {
				t.Fatalf("kind%d next%v mismatch reason=%v traces%#v/%#v", k, next, regionJobFlowRecoveryReason.Load(), x.trace, y.trace)
			}
			if op == "finish" {
				if y.fx.a.mu.TryLock() {
					y.fx.a.mu.Unlock()
					t.Fatal("borrowed lock released")
				}
				x.fx.a.mu.Unlock()
				y.fx.a.mu.Unlock()
			} else {
				regionJobAssertUnlocked(t, y)
			}
		}
	}
}
func TestRegionJobFlowActualPanicNoReplay(t *testing.T) {
	if !rustRegionJobFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"lock", "unlock", "running", "start", "get", "list", "delays", "total", "lookup", "age", "put", "query", "now", "memory", "store", "log", "increment", "stop", "finish", "create", "response", "spawn"} {
		f := newRegionJobFaultFX(1)
		op := "run"
		if port == "create" || port == "response" || port == "spawn" {
			op = "start"
			testEditHealth(f.fx.a, func(p *healthDomainProjection) { p.Job = nil })
		}
		f.panicAt = port
		r, used := runRustRegionJobService(regionJobServiceRequest{Op: op}, f)
		if !used || r.Err == nil || f.trace[len(f.trace)-1].Kind != port {
			t.Fatalf("%s false success/replay", port)
		}
		regionJobAssertUnlocked(t, f)
	}
}
func TestRegionJobFlowActualLongJobBeyondOldLimit(t *testing.T) {
	if !rustRegionJobFlowAvailable() {
		t.Fatal("DLL required")
	}
	f := newRegionJobFaultFX(12000)
	f.large = true
	before := regionJobFlowRecoveries.Load()
	r, used := runRustRegionJobService(regionJobServiceRequest{Op: "run"}, f)
	if !used || r.Err != nil || regionJobFlowRecoveries.Load() != before || f.count <= 100000 || f.fx.a.healthDomainLocked().Job.Running || f.fx.a.healthDomainLocked().Job.Done != 12000 || f.puts != 12001 || f.queries != 24000 || f.stops != 1 {
		t.Fatalf("large incomplete steps=%d done=%d puts=%d queries=%d stops=%d recovery%v", f.count, f.fx.a.healthDomainLocked().Job.Done, f.puts, f.queries, f.stops, regionJobFlowRecoveryReason.Load())
	}
	t.Logf("completed %d effects for 12000 nodes", f.count)
	regionJobAssertUnlocked(t, f)
}
func TestRegionJobFlowActualCapacityAndCancel(t *testing.T) {
	if !rustRegionJobFlowAvailable() {
		t.Fatal("DLL required")
	}
	input, _ := regionJobStartInput(regionJobServiceRequest{Op: "run"})
	var held []regionJobFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustRegionJobFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, s)
	}
	f := newRegionJobFaultFX(1)
	_, used := runRustRegionJobService(regionJobServiceRequest{Op: "run"}, f)
	if used || f.count != 0 {
		t.Fatal("capacity caused effects")
	}
	t.Setenv("SMARTVPN_RUST_REGION_JOB_FLOW", "1")
	r := runSelectedRegionJobService(regionJobServiceRequest{Op: "run"}, f)
	if r.Err != nil || f.stops != 1 {
		t.Fatal("fallback incomplete")
	}
	for _, s := range held {
		s.close()
	}
	held = nil
	before := runtime.NumGoroutine()
	start := time.Now()
	for i := 0; i < 1200; i++ {
		m := newRegionJobMirror(regionJobServiceRequest{Op: "run"})
		if _, ok := m.next(); !ok {
			t.Fatal("no effect")
		}
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 || time.Since(start) > 10*time.Second {
		t.Fatal("cancel leak")
	}
}

func TestRegionJobFlowBorrowedFinishPanicReturnsError(t *testing.T) {
	if !rustRegionJobFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"snapshot", "now", "finish"} {
		f := newRegionJobFaultFX(0)
		f.fx.borrowed = true
		f.fx.a.mu.Lock()
		f.panicAt = port
		r, used := runRustRegionJobService(regionJobServiceRequest{Op: "finish"}, f)
		if !used || r.Err == nil || f.trace[len(f.trace)-1].Kind != port {
			t.Fatalf("%s false success/replay", port)
		}
		if f.fx.a.mu.TryLock() {
			f.fx.a.mu.Unlock()
			t.Fatal("released borrowed mutex")
		}
		f.fx.a.mu.Unlock()
	}
}
