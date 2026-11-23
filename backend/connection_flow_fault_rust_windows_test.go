//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"testing"
)

type connectionInjectedSession struct {
	connectionFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *connectionInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.connectionFlowSession.next()
	if e == nil {
		f, err := decodeConnectionFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *connectionInjectedSession) advance(n uint64, b []byte) error {
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
	return s.connectionFlowSession.advance(n, b)
}
func TestConnectionFlowActualSideEffectFaultsDoNotReplay(t *testing.T) {
	if !rustConnectionFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	type scenario struct {
		op        string
		target    byte
		nth       int
		failure   string
		connected bool
	}
	var cases []scenario
	for _, k := range []byte{1, 4, 5, 6, 8, 9, 10, 11, 12, 13, 31, 30, 21, 22, 23, 24, 25, 26, 29} {
		cases = append(cases, scenario{"connect", k, 1, "", false})
	}
	for _, k := range []byte{22, 7} {
		cases = append(cases, scenario{"connect", k, 1, "system-proxy", false})
	}
	for _, k := range []byte{2, 27, 28, 7, 22, 23} {
		cases = append(cases, scenario{"drop", k, 1, "", false})
	}
	for _, k := range []byte{3, 8, 9, 19, 11} {
		cases = append(cases, scenario{"select", k, 1, "", true})
	}
	cases = append(cases, scenario{"select", 1, 2, "", true})
	for _, c := range cases {
		for _, next := range []bool{false, true} {
			x, y := newConnectionFake(c.op == "drop"), newConnectionFake(c.op == "drop")
			x.Running = c.connected
			y.Running = c.connected
			setup := func(f *connectionFakeEffects) {
				if c.failure != "" {
					f.Failure = map[string]error{c.failure: errors.New("synthetic failure")}
				}
				if c.op == "drop" {
					f.adapter.a.mu.Lock()
					f.adapter.a.proxyOn = true
				}
				if c.op == "select" {
					f.Hook = func(q connectionEffect) {
						if q.Kind == "put" && !f.adapter.locked {
							f.adapter.a.mu.Lock()
							testEditSettings(f.adapter.a, func(s *settings) { s.ImportPolicy = true })
							f.adapter.a.settings.Chains = map[string][]string{"concurrent": {"one"}}
							f.adapter.a.mu.Unlock()
						}
					}
				}
			}
			setup(x)
			setup(y)
			q := connectionServiceRequest{Op: c.op, Name: "one"}
			want := runGoConnectionService(q, x)
			var injected *connectionInjectedSession
			got, used := runRustConnectionServiceWithStart(q, y, func(input []byte) (connectionFlowSession, error) {
				s, e := startRustConnectionFlow(input)
				if e != nil {
					return nil, e
				}
				injected = &connectionInjectedSession{connectionFlowSession: s, target: c.target, nth: c.nth, nextFailure: next}
				return injected, nil
			})
			same := reflect.DeepEqual(got, want) && reflect.DeepEqual(x.Trace, y.Trace) && reflect.DeepEqual(settingsStateFromApp(x.adapter.a), settingsStateFromApp(y.adapter.a)) && x.adapter.a.proxyOn == y.adapter.a.proxyOn
			if c.op == "drop" {
				if y.adapter.a.mu.TryLock() {
					y.adapter.a.mu.Unlock()
					t.Fatal("borrowed lock released")
				}
				x.adapter.a.mu.Unlock()
				y.adapter.a.mu.Unlock()
			} else {
				if !y.adapter.a.mu.TryLock() {
					t.Fatal("lock leaked")
				}
				y.adapter.a.mu.Unlock()
			}
			if !used || injected == nil || !injected.failed || !same {
				t.Fatal(c, next, got, want, x.Trace, y.Trace)
			}
		}
	}
}
func TestConnectionFlowActualCapacityPreEffectFallback(t *testing.T) {
	if !rustConnectionFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	input, err := connectionStartInput(connectionServiceRequest{Op: "connect"})
	if err != nil {
		t.Fatal(err)
	}
	var held []connectionFlowSession
	defer func() {
		for _, h := range held {
			h.close()
		}
	}()
	for i := 0; i < 64; i++ {
		h, e := startRustConnectionFlow(input)
		if e != nil {
			t.Fatal(i, e)
		}
		held = append(held, h)
	}
	if h, e := startRustConnectionFlow(input); e == nil {
		h.close()
		t.Fatal("unbounded capacity")
	}
	t.Setenv("SMARTVPN_RUST_CONNECTION_FLOW", "1")
	f := newConnectionFake(false)
	r := runSelectedConnectionService(connectionServiceRequest{Op: "connect"}, f)
	if r.Status != 200 {
		t.Fatal(r)
	}
	starts := 0
	for _, e := range f.Trace {
		if e.Kind == "start-kernel" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatal("start replayed", starts)
	}
	for _, h := range held {
		h.close()
	}
	held = nil
	f = newConnectionFake(false)
	r, used := runRustConnectionService(connectionServiceRequest{Op: "connect"}, f)
	if !used || r.Status != 200 {
		t.Fatal(r, used)
	}
}
