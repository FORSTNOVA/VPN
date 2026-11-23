package main

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

type stateCloseSpy struct {
	serviceStateBackend
	reads, closes int
	cancelled     chan struct{}
	order         bool
}

func (b *stateCloseSpy) Read() (serviceStateSnapshot, error) {
	b.reads++
	return b.serviceStateBackend.Read()
}
func (b *stateCloseSpy) Close() error {
	b.closes++
	if b.cancelled != nil {
		select {
		case <-b.cancelled:
			b.order = true
		default:
		}
	}
	return b.serviceStateBackend.Close()
}
func TestServiceStateClosedOldLoop(t *testing.T) {
	old := make(chan struct{})
	b := &stateCloseSpy{serviceStateBackend: newGoServiceStateBackend(), cancelled: old}
	a := &app{service: newServiceAuthority(b), trafficHistoryCancel: old}
	a.mu.Lock()
	if e := a.closeServiceStateLocked(); e != nil {
		t.Fatal(e)
	}
	a.mu.Unlock()
	e := newAppTrafficEffects(a, trafficServiceRequest{Op: "loop"}, old)
	starts, stops := 0, 0
	e.tickerStart = func() { starts++ }
	e.tickerStop = func() { stops++ }
	waits := 0
	e.wait = func() bool { waits++; return waits == 1 }
	e.running = func() bool { t.Fatal("old loop ran"); return true }
	e.get = func() (kernelTraffic, error) { t.Fatal("old loop GET"); return kernelTraffic{}, nil }
	r := runGoTrafficService(trafficServiceRequest{Op: "loop"}, e)
	if r.Err != nil || b.reads != 0 || b.closes != 1 || !b.order || starts != 1 || stops != 1 {
		t.Fatalf("r=%+v reads=%d close=%d order=%v timers=%d/%d", r, b.reads, b.closes, b.order, starts, stops)
	}
	if !a.mu.TryLock() {
		t.Fatal("mutex leaked")
	}
	a.mu.Unlock()
	if a.serviceStateLocked() != a.service || !a.service.closed {
		t.Fatal("closed state replaced")
	}
}
func TestServiceStateClosedHTTPFailure(t *testing.T) {
	a := &app{}
	a.mu.Lock()
	_ = a.closeServiceStateLocked()
	a.mu.Unlock()
	w := httptest.NewRecorder()
	a.traffic(w, httptest.NewRequest("GET", "/api/traffic", nil))
	if w.Code != 500 {
		t.Fatalf("code %d", w.Code)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}

type stateAckFail struct {
	serviceStateBackend
	fail bool
}

func (b *stateAckFail) Acknowledge(a serviceStateAck) error {
	if b.fail {
		return errors.New("ack lost")
	}
	return b.serviceStateBackend.Acknowledge(a)
}
func TestServiceStatePendingClockBound(t *testing.T) {
	b := &stateAckFail{serviceStateBackend: newGoServiceStateBackend()}
	s := newServiceAuthority(b)
	base := time.Now()
	if e := s.baseline(trafficSample{at: base, up: 1}); e != nil {
		t.Fatal(e)
	}
	b.fail = true
	if e := s.baseline(trafficSample{at: base.Add(time.Second), up: 2}); e == nil {
		t.Fatal("expected pending")
	}
	for i := 0; i < 100; i++ {
		if e := s.baseline(trafficSample{at: base.Add(time.Minute), up: 3}); e == nil {
			t.Fatal("unresolved write allowed")
		}
	}
	if len(s.clocks) != 2 {
		t.Fatalf("clock ledger=%d", len(s.clocks))
	}
	if e := s.transferToGo(); e == nil {
		t.Fatal("pending transfer allowed")
	}
	_ = s.Close()
}
func TestServiceStateLifecycleInitOwnership(t *testing.T) {
	for _, success := range []bool{false, true} {
		a := &app{}
		e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "new"})
		e.Call(lifecycleEffect{Kind: "init"})
		if success {
			e.FinalizeLifecycleResult(e.q, lifecycleServiceResult{})
		}
		e.Release()
		e.Release()
		_, err := a.service.read()
		if success && err != nil {
			t.Fatal(err)
		}
		if !success && err == nil {
			t.Fatal("aborted init leaked")
		}
		_ = a.service.Close()
	}
}
func (b *stateCloseSpy) Meta() (serviceStateSnapshot, error) {
	b.reads++
	return b.serviceStateBackend.Meta()
}
