package main

import (
	"errors"
	"testing"
)

type pathMonitorGuardSession struct {
	mode string
	step uint64
}

func (s *pathMonitorGuardSession) next() ([]byte, uint64, error) {
	if s.mode == "next-error" {
		return nil, 0, errors.New("first frame")
	}
	b := []byte{1}
	b = registryInt(b, 0)
	b = registryInt(b, 0)
	b = registryBytes(b, "")
	b = registryBytes(b, "")
	b = registryInt(b, 0)
	b = registryInt(b, 0)
	return b, 1, nil
}
func (s *pathMonitorGuardSession) advance(uint64, []byte) error {
	return errors.New("post-lock failure")
}
func (s *pathMonitorGuardSession) close() {}

type pathMonitorGuardFX struct {
	real      *appPathEffects
	panicLock bool
	calls     int
}

func (e *pathMonitorGuardFX) Release() { e.real.Release() }
func (e *pathMonitorGuardFX) Call(q pathServiceEffect) pathServiceReply {
	e.calls++
	if q.Kind == "running" {
		return pathServiceReply{Bool: false}
	}
	if q.Kind != "lock" && q.Kind != "unlock" {
		panic("unexpected guard port")
	}
	r := e.real.Call(q)
	if e.panicLock && q.Kind == "lock" {
		panic("after admission lock")
	}
	return r
}
func TestPathMonitorFlowGuardFirstFrameFallback(t *testing.T) {
	for _, mode := range []string{"open-error", "next-error", "post-lock", "panic-lock"} {
		t.Run(mode, func(t *testing.T) {
			a := &app{}
			token := make(chan struct{})
			a.pathCancel = token
			q := pathServiceRequest{Op: "baseline"}
			used := withPathMonitorRound(a, token, q, func(real *appPathEffects) {
				fx := &pathMonitorGuardFX{real: real, panicLock: mode == "panic-lock"}
				start := func([]byte) (pathFlowSession, error) {
					if mode == "open-error" {
						return nil, errors.New("unavailable")
					}
					return &pathMonitorGuardSession{mode: mode}, nil
				}
				r, handled := runRustPathServiceWithStart(q, fx, start)
				if mode == "open-error" || mode == "next-error" {
					if handled || fx.calls != 0 {
						t.Fatal("pre-effect fallback performed work")
					}
					if a.mu.TryLock() {
						a.mu.Unlock()
						t.Fatal("admission lock released before fallback")
					}
					r = runGoPathService(q, fx)
				} else if !handled {
					t.Fatal("post-effect replay requested")
				}
				if mode == "panic-lock" && r.Err == nil {
					t.Fatal("panic reported success")
				}
			})
			if !used {
				t.Fatal("valid token rejected")
			}
			if !a.mu.TryLock() {
				t.Fatal("owned lock leaked")
			}
			a.mu.Unlock()
		})
	}
}
