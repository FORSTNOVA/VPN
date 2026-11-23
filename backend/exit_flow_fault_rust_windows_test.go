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

type exitInjectedSession struct {
	exitFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *exitInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.exitFlowSession.next()
	if e == nil {
		f, err := decodeExitFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *exitInjectedSession) advance(n uint64, b []byte) error {
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
	return s.exitFlowSession.advance(n, b)
}

type exitFaultFX struct {
	mu                                 sync.Mutex
	owned                              bool
	state                              exitServiceState
	trace                              []exitEffect
	panicAt                            string
	closeRelay, closePatrol, off, drop int
	patrol                             chan struct{}
}

func newExitFaultFX() *exitFaultFX {
	return &exitFaultFX{state: exitServiceState{Matches: true, RelayPresent: true, PatrolPresent: true, ProxyOn: true}, patrol: make(chan struct{})}
}
func (f *exitFaultFX) Release() {
	if f.owned {
		f.owned = false
		f.mu.Unlock()
	}
}
func (f *exitFaultFX) Call(q exitEffect) (r exitReply) {
	f.trace = append(f.trace, q)
	defer func() { s := f.state; r.State = &s }()
	switch q.Kind {
	case "lock":
		f.mu.Lock()
		f.owned = true
	case "close-relay":
		f.closeRelay++
		f.state.RelayPresent = false
	case "close-patrol":
		close(f.patrol)
		f.closePatrol++
		f.state.PatrolPresent = false
	case "proxy-off":
		f.off++
		r.Err = errors.New("synthetic proxy failure")
	case "clear-proxy":
		f.state.ProxyOn = false
	case "drop-tunnel":
		f.drop++
	}
	if q.Kind == f.panicAt {
		panic("synthetic exit panic")
	}
	return
}
func exitAssertUnlocked(t *testing.T, f *exitFaultFX) {
	t.Helper()
	if !f.mu.TryLock() {
		t.Fatal("owned mutex leaked")
	}
	f.mu.Unlock()
}
func TestExitFlowActualFaultsNoReplay(t *testing.T) {
	if !rustExitFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 11; k++ {
		for _, next := range []bool{false, true} {
			x, y := newExitFaultFX(), newExitFaultFX()
			want := runGoExitService(exitServiceRequest{}, x)
			var inj *exitInjectedSession
			got, used := runRustExitServiceWithStart(exitServiceRequest{}, y, func(b []byte) (exitFlowSession, error) {
				s, e := startRustExitFlow(b)
				if e != nil {
					return nil, e
				}
				inj = &exitInjectedSession{exitFlowSession: s, target: k, nth: 1, nextFailure: next}
				return inj, nil
			})
			if !used || inj == nil || !inj.failed || got.Err != nil || want.Err != nil || !reflect.DeepEqual(x.trace, y.trace) || x.state != y.state || x.closeRelay != y.closeRelay || x.closePatrol != y.closePatrol || x.off != y.off || x.drop != y.drop {
				t.Fatalf("fault %d next%v mismatch reason%v", k, next, exitFlowRecoveryReason.Load())
			}
			exitAssertUnlocked(t, y)
		}
	}
}
func TestExitFlowActualPanicReleasesLock(t *testing.T) {
	if !rustExitFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"lock", "close-relay", "history-stop", "clear-kernel", "remove-config", "close-patrol", "clear-block", "proxy-off", "log", "clear-proxy", "drop-tunnel"} {
		f := newExitFaultFX()
		f.panicAt = port
		r, used := runRustExitService(exitServiceRequest{}, f)
		if !used || r.Err == nil || f.trace[len(f.trace)-1].Kind != port {
			t.Fatalf("%s false success or replay", port)
		}
		exitAssertUnlocked(t, f)
		if f.closeRelay > 1 || f.closePatrol > 1 || f.off > 1 || f.drop > 1 {
			t.Fatal("duplicate effect")
		}
	}
}
func TestExitFlowActualCapacityAndCancellation(t *testing.T) {
	if !rustExitFlowAvailable() {
		t.Fatal("DLL required")
	}
	var held []exitFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustExitFlow([]byte{0})
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, s)
	}
	f := newExitFaultFX()
	_, used := runRustExitService(exitServiceRequest{}, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity side effect")
	}
	t.Setenv("SMARTVPN_RUST_EXIT_FLOW", "1")
	if r := runSelectedExitService(exitServiceRequest{}, f); r.Err != nil || f.drop != 1 {
		t.Fatal("safe fallback failed")
	}
	exitAssertUnlocked(t, f)
	for _, s := range held {
		s.close()
	}
	held = nil
	before := runtime.NumGoroutine()
	start := time.Now()
	for i := 0; i < 1200; i++ {
		m := newExitMirror(exitServiceRequest{})
		if _, ok := m.next(); !ok {
			t.Fatal("missing first effect")
		}
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 || time.Since(start) > 10*time.Second {
		t.Fatal("cancel resource leak")
	}
	f = newExitFaultFX()
	if r, ok := runRustExitService(exitServiceRequest{}, f); !ok || r.Err != nil {
		t.Fatal("capacity not reusable")
	}
}
