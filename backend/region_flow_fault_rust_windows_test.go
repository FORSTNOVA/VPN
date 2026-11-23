//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

type regionInjectedSession struct {
	regionFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *regionInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.regionFlowSession.next()
	if e == nil {
		f, err := decodeRegionFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *regionInjectedSession) advance(n uint64, b []byte) error {
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
	return s.regionFlowSession.advance(n, b)
}

type regionFaultFX struct {
	mu                   sync.Mutex
	owned                bool
	s                    *regionServiceState
	trace                []regionEffect
	now                  time.Time
	panicAt              string
	saveFail             bool
	blocks, puts, events int
}

func newRegionFaultFX(block bool) *regionFaultFX {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := &regionServiceState{Regions: map[string]regionRecord{"b": {Country: "US", Status: regionVerified, VerifiedAt: now}, "a": {Country: "US", Status: regionVerified, VerifiedAt: now}}, Health: map[string]nodeHealth{"a": {LatencyMs: 10}, "b": {LatencyMs: 20}}}
	if block {
		for name, h := range s.Health {
			h.State = healthCooldown
			h.CooldownUntil = now.Add(time.Hour)
			s.Health[name] = h
		}
	}
	return &regionFaultFX{s: s, now: now}
}
func (f *regionFaultFX) Release() {
	if f.owned {
		f.owned = false
		f.mu.Unlock()
	}
}
func (f *regionFaultFX) Call(q regionEffect) (r regionReply) {
	f.trace = append(f.trace, q)
	defer func() { r.State = cloneRegionServiceState(f.s) }()
	switch q.Kind {
	case "lock":
		f.mu.Lock()
		f.owned = true
	case "now":
		r.Now = f.now
	case "set-country":
		f.s.LockedRegion = q.Country
	case "set-node":
		f.s.LockedNode = q.Name
	case "save":
		if f.saveFail {
			r.Err = errors.New("save failure")
		}
	case "running":
		r.Bool = true
	case "get":
		r.Value = "old"
	case "put":
		f.puts++
		r.Err = errors.New("pin failure")
	case "block":
		f.blocks++
		f.s.Blocked = true
		f.s.BlockReason = "partial block"
		f.s.SweepNote = "changed by subcase"
	case "event":
		f.events++
	}
	if q.Kind == f.panicAt {
		panic("synthetic region panic")
	}
	return
}
func regionAssertUnlocked(t *testing.T, f *regionFaultFX) {
	t.Helper()
	if !f.mu.TryLock() {
		t.Fatal("owned mutex leak")
	}
	f.mu.Unlock()
}
func TestRegionFlowActualFaultsNoReplay(t *testing.T) {
	if !rustRegionFlowAvailable() {
		t.Fatal("DLL required")
	}
	type scenario struct {
		op     string
		target byte
		block  bool
	}
	var cases []scenario
	for k := byte(1); k <= 13; k++ {
		cases = append(cases, scenario{"lock", k, k == 10})
	}
	cases = append(cases, scenario{"report", 1, false}, scenario{"report", 2, false})
	for _, c := range cases {
		for _, next := range []bool{false, true} {
			x, y := newRegionFaultFX(c.block), newRegionFaultFX(c.block)
			q := regionServiceRequest{Op: c.op, Country: "US"}
			want := runGoRegionService(q, x)
			var inj *regionInjectedSession
			got, used := runRustRegionServiceWithStart(q, y, func(b []byte) (regionFlowSession, error) {
				s, e := startRustRegionFlow(b)
				if e != nil {
					return nil, e
				}
				inj = &regionInjectedSession{regionFlowSession: s, target: c.target, nth: 1, nextFailure: next}
				return inj, nil
			})
			if !used || inj == nil || !inj.failed || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.s, y.s) || x.blocks != y.blocks || x.puts != y.puts || x.events != y.events {
				t.Fatalf("fault op%s kind%d next%v mismatch reason%v traces%#v/%#v", c.op, c.target, next, regionFlowRecoveryReason.Load(), x.trace, y.trace)
			}
			regionAssertUnlocked(t, y)
		}
	}
}
func TestRegionFlowActualPanicReleasesLock(t *testing.T) {
	if !rustRegionFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"lock", "now", "set-country", "set-node", "save", "running", "get", "put", "log", "block", "event"} {
		f := newRegionFaultFX(port == "block")
		f.panicAt = port
		r, used := runRustRegionService(regionServiceRequest{Op: "lock", Country: "US"}, f)
		if !used || r.Status != 500 || f.trace[len(f.trace)-1].Kind != port {
			t.Fatalf("%s false success/replay", port)
		}
		regionAssertUnlocked(t, f)
		if f.blocks > 1 || f.puts > 1 || f.events > 1 {
			t.Fatal("repeated effect")
		}
	}
}
func TestRegionFlowActualCapacityAndCancellation(t *testing.T) {
	if !rustRegionFlowAvailable() {
		t.Fatal("DLL required")
	}
	input, _ := regionStartInput(regionServiceRequest{Op: "report"})
	var held []regionFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustRegionFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, s)
	}
	f := newRegionFaultFX(false)
	q := regionServiceRequest{Op: "report"}
	_, used := runRustRegionService(q, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity side effects")
	}
	t.Setenv("SMARTVPN_RUST_REGION_FLOW", "1")
	if r := runSelectedRegionService(q, f); r.Status != 200 {
		t.Fatal("capacity fallback")
	}
	regionAssertUnlocked(t, f)
	for _, s := range held {
		s.close()
	}
	held = nil
	before := runtime.NumGoroutine()
	start := time.Now()
	for i := 0; i < 1200; i++ {
		m := newRegionMirror(q)
		if _, ok := m.next(); !ok {
			t.Fatal("missing effect")
		}
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 || time.Since(start) > 10*time.Second {
		t.Fatal("cancel leak")
	}
	f = newRegionFaultFX(false)
	if r, ok := runRustRegionService(q, f); !ok || r.Status != 200 {
		t.Fatal("capacity reuse")
	}
}
