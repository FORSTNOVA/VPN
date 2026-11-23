package main

import (
	"errors"
	"testing"
	"time"
)

func TestPathMonitorServiceBorrowedLifecycle(t *testing.T) {
	a := &app{}
	testEditPath(a, func(p *servicePathState) { p.Config = defaultPathChecks() })
	testEditPath(a, func(p *servicePathState) { p.Config.Enabled = true })
	a.mu.Lock()
	defer a.mu.Unlock()
	q := pathMonitorServiceRequest{Op: "start"}
	e := newAppPathMonitorEffects(a, q, nil, nil)
	spawned := 0
	e.loopRunner = func(<-chan struct{}, time.Duration) { spawned++ }
	e.launch = func(f func()) { f() }
	e.hooks = map[string]func(pathMonitorServiceEffect) pathMonitorServiceReply{"log": func(pathMonitorServiceEffect) pathMonitorServiceReply { return pathMonitorServiceReply{} }, "running": func(pathMonitorServiceEffect) pathMonitorServiceReply {
		t.Fatal("start unexpectedly checks kernel")
		return pathMonitorServiceReply{}
	}}
	runGoPathMonitorService(q, e)
	if a.pathCancel == nil || spawned != 1 {
		t.Fatal("did not start")
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	c := a.pathCancel
	testEditPath(a, func(p *servicePathState) { p.Status.Demanded = true })
	testEditPath(a, func(p *servicePathState) { p.Status.RetryAt = "old" })
	runGoPathMonitorService(pathMonitorServiceRequest{Op: "stop"}, newAppPathMonitorEffects(a, pathMonitorServiceRequest{Op: "stop"}, nil, nil))
	select {
	case <-c:
	default:
		t.Fatal("cancel not closed")
	}
	if a.pathCancel != nil || a.pathStatusLocked().Demanded || a.pathStatusLocked().RetryAt != "" {
		t.Fatal("stop incomplete")
	}
}
func TestPathMonitorServiceTimerRegistrationPanic(t *testing.T) {
	a := &app{}
	c := make(chan struct{})
	a.pathCancel = c
	q := pathMonitorServiceRequest{Op: "loop", Guard: true, Interval: time.Minute}
	e := newAppPathMonitorEffects(a, q, nil, c)
	stops := map[string]int{}
	e.makeTicker = func(n string, _ time.Duration, register func(<-chan time.Time, func())) {
		register(make(chan time.Time), func() { stops[n]++ })
		if n == "demand" {
			panic("synthetic after registration")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		runGoPathMonitorService(q, e)
	}()
	e.Release()
	if stops["baseline"] != 1 || stops["demand"] != 1 || stops["retry"] != 0 {
		t.Fatal(stops)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leak")
	}
	a.mu.Unlock()
	select {
	case <-c:
		t.Fatal("loop cleanup closed app cancel")
	default:
	}
}
func TestPathMonitorServiceFakeTickOrdering(t *testing.T) {
	a := &app{}
	c := make(chan struct{})
	a.pathCancel = c
	q := pathMonitorServiceRequest{Op: "loop", Guard: true, Interval: time.Minute}
	e := newAppPathMonitorEffects(a, q, nil, c)
	stops := 0
	e.makeTicker = func(_ string, _ time.Duration, register func(<-chan time.Time, func())) {
		register(make(chan time.Time), func() { stops++ })
	}
	order := []string{}
	e.launch = func(f func()) { order = append(order, "spawn"); f() }
	e.roundRunner = func(mode string, guard bool, token <-chan struct{}) {
		if !guard || token != c {
			t.Fatal("lost guard")
		}
		order = append(order, mode)
	}
	step := 0
	e.hooks = map[string]func(pathMonitorServiceEffect) pathMonitorServiceReply{"wait": func(pathMonitorServiceEffect) pathMonitorServiceReply {
		step++
		if step == 1 {
			return pathMonitorServiceReply{Text: "baseline"}
		}
		return pathMonitorServiceReply{Text: "cancel"}
	}}
	runGoPathMonitorService(q, e)
	if len(order) != 3 || order[0] != "spawn" || order[1] != "baseline" || order[2] != "baseline" || stops != 3 {
		t.Fatal(order, stops)
	}
}
func TestPathMonitorServiceUpdateFailureRetainsSettings(t *testing.T) {
	a := &app{}
	testEditPath(a, func(p *servicePathState) { p.Config = defaultPathChecks() })
	c := make(chan struct{})
	a.pathCancel = c
	q := pathMonitorServiceRequest{Op: "update", Settings: pathCheckSettings{Enabled: true, IntervalSec: 42}}
	e := newAppPathMonitorEffects(a, q, nil, nil)
	calls := 0
	e.hooks = map[string]func(pathMonitorServiceEffect) pathMonitorServiceReply{"save": func(pathMonitorServiceEffect) pathMonitorServiceReply {
		return pathMonitorServiceReply{Err: errors.New("synthetic")}
	}, "response": func(x pathMonitorServiceEffect) pathMonitorServiceReply {
		calls++
		if x.Status != 500 {
			t.Fatal(x.Status)
		}
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("response moved outside lock")
		}
		return pathMonitorServiceReply{}
	}}
	r := runGoPathMonitorService(q, e)
	if r.Status != 500 || calls != 1 || a.pathConfigLocked().IntervalSec != 42 || a.pathCancel != c {
		t.Fatal(r, a.pathConfigLocked())
	}
	select {
	case <-c:
		t.Fatal("save failure stopped monitor")
	default:
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leak")
	}
	a.mu.Unlock()
}
func TestPathMonitorServiceClockBoundaries(t *testing.T) {
	a := &app{}
	e := newAppPathMonitorEffects(a, pathMonitorServiceRequest{}, nil, nil)
	now := time.Unix(500, 0)
	e.now = func() time.Time { return now }
	for _, d := range []time.Duration{30*time.Second - time.Nanosecond, 30 * time.Second} {
		r := e.Call(pathMonitorServiceEffect{Kind: "since", Time: now.Add(-d)})
		if r.Bool != (d < 30*time.Second) {
			t.Fatal(d)
		}
	}
	r := e.Call(pathMonitorServiceEffect{Kind: "parse-time", Time: now, Text: now.UTC().Format(time.RFC3339Nano)})
	if r.Err != nil || r.Bool {
		t.Fatal("exact due rejected")
	}
}
