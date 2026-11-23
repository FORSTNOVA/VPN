package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestServiceStatePathClosedScheduler(t *testing.T) {
	old := make(chan struct{})
	a := &app{service: newServiceAuthority(newGoServiceStateBackend()), pathCancel: old}
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
	a.mu.Lock()
	if e := a.closeServiceStateLocked(); e != nil {
		t.Fatal(e)
	}
	a.mu.Unlock()
	e := newAppPathMonitorEffects(a, pathMonitorServiceRequest{Op: "loop", Guard: true}, nil, old)
	a.mu.Lock()
	s := e.snapshot()
	a.mu.Unlock()
	if s.Identity || a.pathCancel != nil {
		t.Fatal("old scheduler revived")
	}
	w := httptest.NewRecorder()
	a.pathChecks(w, httptest.NewRequest("GET", "/api/path-checks", nil))
	if w.Code != 500 {
		t.Fatalf("closed response %d", w.Code)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}
func TestServiceStatePathSharedClockLedger(t *testing.T) {
	a := &app{}
	s := a.serviceStateLocked()
	defer s.Close()
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
	g := a.pathStateLocked().Generation
	base := time.Now()
	for i := 0; i < 100; i++ {
		now := base.Add(time.Duration(i) * time.Second)
		if e := s.baseline(trafficSample{at: now, up: int64(i)}); e != nil {
			t.Fatal(e)
		}
		a.pathClockCommandLocked(pathStateAt, now, "", 0)
		a.pathClockCommandLocked(pathStateThrottle, now, "target", g)
		if len(s.clocks) != 3 {
			t.Fatalf("ledger %d", len(s.clocks))
		}
		if a.pathStatusLocked().At != now {
			t.Fatal("path timestamp changed")
		}
	}
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRevokeMonitor})
	if len(s.clocks) != 2 {
		t.Fatalf("revoked clocks %d", len(s.clocks))
	}
	if e := s.transferToGo(); e != nil {
		t.Fatal(e)
	}
}
func TestServiceStatePathAdmittedLeaseSurvivesGeneration(t *testing.T) {
	a := &app{}
	defer a.serviceStateLocked().Close()
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
	old := a.pathStateLocked().Generation
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateBeginRound})
	lease := a.pathStateLocked().LeaseID
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRevokeMonitor})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
	if a.pathStateLocked().Generation == old || a.pathStateLocked().LeaseID != lease {
		t.Fatal("admitted lease cancelled")
	}
	if e := a.service.transferToGo(); e == nil {
		t.Fatal("busy transfer allowed")
	}
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateFinishRound, LeaseID: lease})
	if a.pathStateLocked().Busy {
		t.Fatal("lease not released")
	}
}
func TestServiceStatePathProjectionDetached(t *testing.T) {
	a := &app{}
	defer a.serviceStateLocked().Close()
	a.replacePathConfigLocked(pathCheckSettings{Targets: []pathTarget{{Label: "one", URL: "https://synthetic.invalid"}}})
	c := a.pathConfigLocked()
	c.Targets[0].Label = "edited"
	if a.pathConfigLocked().Targets[0].Label != "one" {
		t.Fatal("config alias")
	}
}
