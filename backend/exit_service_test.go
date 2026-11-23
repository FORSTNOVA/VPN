package main

import (
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type exitIdentityRunner struct{ id int }

func (*exitIdentityRunner) Running() bool { panic("exit must not call Running") }
func (*exitIdentityRunner) Start() error  { panic("exit must not start runner") }
func (*exitIdentityRunner) Stop() error   { panic("exit must not stop runner") }

type exitTestEffects struct {
	real       *appExitEffects
	trace      []string
	panicAfter string
	beforeLock func()
}

func (e *exitTestEffects) Release() { e.real.Release() }
func (e *exitTestEffects) Call(q exitEffect) exitReply {
	e.trace = append(e.trace, q.Kind)
	if q.Kind == "lock" && e.beforeLock != nil {
		e.beforeLock()
	}
	r := e.real.Call(q)
	if q.Kind == e.panicAfter {
		panic("injected after " + q.Kind)
	}
	return r
}
func exitFixture(which kernelRunner) (*app, *exitTestEffects, *[]string) {
	a := testHealthApp(testChinaApp(&app{kernel: which, dnsRelay: &adaptiveDNS{}, patrolCancel: make(chan struct{}), pathCancel: make(chan struct{}), trafficHistoryCancel: make(chan struct{}), proxyOn: true}, true), func(p *healthDomainProjection) { p.Blocked = true; p.BlockReason = "blocked" })
	calls := []string{}
	r := newAppExitEffects(a, which)
	r.closeRelay = func(*adaptiveDNS) { calls = append(calls, "relay") }
	r.stopHistory = func() {
		calls = append(calls, "history")
		if a.trafficHistoryCancel != nil {
			close(a.trafficHistoryCancel)
			a.trafficHistoryCancel = nil
		}
	}
	r.removeConfig = func() { calls = append(calls, "config") }
	r.proxyOff = func() error { calls = append(calls, "proxy"); return errors.New("proxy rejected") }
	r.dropTunnel = func() { calls = append(calls, "tunnel") }
	r.logMessage = func(string) { calls = append(calls, "log") }
	return a, &exitTestEffects{real: r}, &calls
}
func TestExitServiceMatchedCleanupOrder(t *testing.T) {
	runner := &exitIdentityRunner{1}
	a, fx, calls := exitFixture(runner)
	patrol, path := a.patrolCancel, a.pathCancel
	r := runGoExitService(exitServiceRequest{}, fx)
	if r.Err != nil || a.kernel != nil || a.dnsRelay != nil || a.patrolCancel != nil || a.proxyOn != (runtime.GOOS == "windows") || a.healthDomainLocked().Blocked || a.healthDomainLocked().BlockReason != "" || a.chinaStateLocked().Active || a.trafficHistoryCancel != nil {
		t.Fatal(r, a)
	}
	want := []string{"lock", "close-relay", "history-stop", "clear-kernel", "remove-config", "close-patrol", "clear-block", "proxy-off", "log", "clear-proxy", "drop-tunnel"}
	if !reflect.DeepEqual(fx.trace, want) {
		t.Fatal(fx.trace)
	}
	if !reflect.DeepEqual(*calls, []string{"relay", "history", "config", "proxy", "log", "tunnel"}) {
		t.Fatal(*calls)
	}
	select {
	case <-patrol:
	default:
		t.Fatal("patrol not closed")
	}
	select {
	case <-path:
		if runtime.GOOS != "windows" {
			t.Fatal("unexpected stopPath")
		}
	default:
		if runtime.GOOS == "windows" {
			t.Fatal("path monitor survived kernel exit")
		}
	}
	if !a.mu.TryLock() {
		t.Fatal("owned lock leaked")
	}
	a.mu.Unlock()
}
func TestExitServiceStaleAndIdentityNilSemantics(t *testing.T) {
	for _, tc := range []struct {
		name           string
		current, which kernelRunner
		matched        bool
	}{
		{"different", &exitIdentityRunner{1}, &exitIdentityRunner{1}, false}, {"nil-nil", nil, nil, true}, {"nil-typednil", nil, (*exitIdentityRunner)(nil), false}, {"typednil-same", (*exitIdentityRunner)(nil), (*exitIdentityRunner)(nil), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fx, calls := exitFixture(tc.current)
			fx.real.which = tc.which
			runGoExitService(exitServiceRequest{}, fx)
			if tc.matched {
				if len(*calls) == 0 {
					t.Fatal("matching nil identity skipped")
				}
			} else if len(*calls) != 0 || !reflect.DeepEqual(fx.trace, []string{"lock"}) || a.kernel != tc.current {
				t.Fatal("stale callback mutated", fx.trace, *calls)
			}
			if !a.mu.TryLock() {
				t.Fatal("leak")
			}
			a.mu.Unlock()
		})
	}
}
func TestExitServiceRepeatedCallbacks(t *testing.T) {
	a, fx, calls := exitFixture(&exitIdentityRunner{1})
	runGoExitService(exitServiceRequest{}, fx)
	count := len(*calls)
	fx.trace = nil
	runGoExitService(exitServiceRequest{}, fx)
	if len(*calls) != count || !reflect.DeepEqual(fx.trace, []string{"lock"}) {
		t.Fatal("non-nil callback replayed", fx.trace)
	}
	fx.real.which = nil
	runGoExitService(exitServiceRequest{}, fx)
	if len(*calls) == count {
		t.Fatal("nil callback silently deduplicated")
	}
	count = len(*calls)
	runGoExitService(exitServiceRequest{}, fx)
	if len(*calls) == count {
		t.Fatal("repeat nil must match")
	}
	if (a.pathCancel == nil) != (runtime.GOOS == "windows") {
		t.Fatal("unexpected path monitor state")
	}
}
func TestExitServicePartialPanicReleasesWithoutReplay(t *testing.T) {
	for _, kind := range []string{"lock", "close-relay", "history-stop", "clear-kernel", "remove-config", "close-patrol", "clear-block", "proxy-off", "log", "clear-proxy", "drop-tunnel"} {
		t.Run(kind, func(t *testing.T) {
			a, fx, _ := exitFixture(&exitIdentityRunner{1})
			fx.panicAfter = kind
			func() {
				defer func() {
					if recover() == nil {
						t.Error("missing panic")
					}
				}()
				runGoExitService(exitServiceRequest{}, fx)
			}()
			seen := 0
			for _, v := range fx.trace {
				if v == kind {
					seen++
				}
			}
			if seen != 1 || fx.trace[len(fx.trace)-1] != kind {
				t.Fatal("cleanup replay after panic", fx.trace)
			}
			if !a.mu.TryLock() {
				t.Fatal("panic lock leaked")
			}
			a.mu.Unlock()
			fx.Release()
		})
	}
}
func TestExitServiceIdentityCheckedAfterLock(t *testing.T) {
	old, next := &exitIdentityRunner{1}, &exitIdentityRunner{2}
	a, fx, calls := exitFixture(old)
	a.mu.Lock()
	entered := make(chan struct{})
	done := make(chan struct{})
	fx.beforeLock = func() { close(entered) }
	go func() { runGoExitService(exitServiceRequest{}, fx); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		a.mu.Unlock()
		t.Fatal("callback did not reach lock")
	}
	a.kernel = next
	a.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("callback hung")
	}
	if len(*calls) != 0 || a.kernel != next {
		t.Fatal("identity compared before acquiring lock", *calls)
	}
}
