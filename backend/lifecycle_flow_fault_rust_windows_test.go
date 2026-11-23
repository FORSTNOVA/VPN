//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
)

type lifecycleInjectedSession struct {
	lifecycleFlowSession
	kind, target                 byte
	nextFailure, pending, failed bool
}

func (s *lifecycleInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next")
	}
	b, n, e := s.lifecycleFlowSession.next()
	if e == nil {
		f, er := decodeLifecycleFrame(b)
		if er == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *lifecycleInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance")
		}
	}
	return s.lifecycleFlowSession.advance(n, b)
}
func lifecycleFaultOp(k byte) string {
	switch {
	case k >= 27 && k <= 31:
		return "prepare"
	case k >= 32 && k <= 35:
		return "serve"
	case k >= 36 && k <= 40:
		return "cleanup"
	case k == 41 || k == 42:
		return "shutdown"
	case k == 44:
		return "reapply"
	case k == 43 || k == 45:
		return "identify"
	}
	return "new"
}
func TestLifecycleFlowActualBoundaryFaults(t *testing.T) {
	for k := byte(1); k <= 46; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q := lifecycleServiceRequest{Op: lifecycleFaultOp(k)}
				s := lifeDiffBase()
				s.Running = true
				s.Collision = true
				s.ProxyOn = true
				if k == 10 {
					s.HashMatch = false
				}
				var inj *lifecycleInjectedSession
				lifeDiffCompare(t, q, s, func(q lifecycleServiceRequest, fx lifecycleServiceEffects) lifecycleServiceResult {
					r, used := runRustLifecycleServiceWithStart(q, fx, func(b []byte) (lifecycleFlowSession, error) {
						ss, e := startRustLifecycleFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &lifecycleInjectedSession{lifecycleFlowSession: ss, target: k, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("boundary not reached", lifecycleFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type lifePanicFX struct {
	*lifeDiffFX
	port string
}

func (f *lifePanicFX) Call(q lifecycleEffect) lifecycleReply {
	r := f.lifeDiffFX.Call(q)
	if q.Kind == f.port {
		panic("after actual fake port")
	}
	return r
}
func TestLifecycleFlowActualPanicCleanup(t *testing.T) {
	ports := map[string]string{"listen": "serve", "listener-close": "serve", "server-new": "serve", "server-serve": "serve", "set": "serve", "lock": "cleanup", "unlock": "cleanup", "stop-kernel": "cleanup", "store-close": "cleanup", "proxy": "reapply", "response": "identify", "disconnect": "shutdown", "signal": "shutdown", "decode-settings": "new", "open-secret": "new", "cache-apply": "new", "bundle-write": "new", "store-open": "new", "source-labels": "new"}
	for port, op := range ports {
		t.Run(port, func(t *testing.T) {
			q := lifecycleServiceRequest{Op: op}
			sp := lifeDiffBase()
			sp.Collision = true
			sp.Running = true
			f := lifeDiffNew(sp, q)
			r, used := runRustLifecycleService(q, &lifePanicFX{f, port})
			if !used || r.Err == nil {
				t.Fatal("panic false success")
			}
			if f.e.locked {
				t.Fatal("lock leaked")
			}
			if !f.e.a.mu.TryLock() {
				t.Fatal("mutex retained")
			}
			f.e.a.mu.Unlock()
			if len(f.e.listeners) != 0 || f.e.serverOwned {
				t.Fatal("resource ledger retained")
			}
			if f.served > 1 || f.signals > 1 {
				t.Fatal("effect replay")
			}
			n := 0
			for _, e := range f.trace {
				if e.Effect.Kind == port {
					n++
				}
			}
			if n != 1 {
				t.Fatal("replayed panic port", n)
			}
		})
	}
}
func TestLifecycleFlowActualCapacityAndCancel(t *testing.T) {
	q := lifecycleServiceRequest{Op: "identify"}
	input, _ := lifecycleStartInput(q)
	var all []lifecycleFlowSession
	for i := 0; i < 64; i++ {
		s, e := startRustLifecycleFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := lifeDiffNew(lifeDiffBase(), q)
	_, used := runRustLifecycleService(q, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity performed effects")
	}
	for _, s := range all {
		s.close()
	}
	s, e := startRustLifecycleFlow(input)
	if e != nil {
		t.Fatal(e)
	}
	s.close()
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newLifecycleMirror(q)
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leaked")
	}
}

type lifeCloseFaultSession struct {
	lifecycleFlowSession
	calls int
	fail  bool
}

func (s *lifeCloseFaultSession) close() {
	s.calls++
	s.lifecycleFlowSession.close()
	if s.fail {
		panic("synthetic close after release")
	}
}

type lifeFinalizeFault struct {
	*lifeDiffFX
	panicBefore bool
	calls       int
}

func (f *lifeFinalizeFault) FinalizeLifecycleResult(q lifecycleServiceRequest, r lifecycleServiceResult) lifecycleServiceResult {
	f.calls++
	if f.panicBefore {
		panic("synthetic pre-handoff")
	}
	return f.lifeDiffFX.FinalizeLifecycleResult(q, r)
}
func TestLifecycleFlowActualHandoffOrdering(t *testing.T) {
	for _, mode := range []string{"success", "close-panic", "finalize-panic"} {
		t.Run(mode, func(t *testing.T) {
			q := lifecycleServiceRequest{Op: "serve"}
			sp := lifeDiffBase()
			sp.Collision = true
			f := &lifeFinalizeFault{lifeDiffFX: lifeDiffNew(sp, q), panicBefore: mode == "finalize-panic"}
			var ss *lifeCloseFaultSession
			r, used := runRustLifecycleServiceWithStart(q, f, func(b []byte) (lifecycleFlowSession, error) {
				s, e := startRustLifecycleFlow(b)
				if e != nil {
					return nil, e
				}
				ss = &lifeCloseFaultSession{lifecycleFlowSession: s, fail: mode == "close-panic"}
				return ss, nil
			})
			if !used || ss.calls != 1 || f.served != 1 {
				t.Fatal("close/serve replay", used, ss.calls, f.served)
			}
			if mode == "success" {
				if r.Err != nil || r.Server != f.server || f.closed != 1 || f.calls != 1 {
					t.Fatal("handoff failed", r.Err, f.closed, f.calls)
				}
			} else {
				if r.Err == nil || r.Server != nil || f.closed != 2 {
					t.Fatal("unsafe partial handoff", r.Err, f.closed)
				}
				if mode == "close-panic" && f.calls != 0 {
					t.Fatal("handoff after close failure")
				}
			}
		})
	}
}
