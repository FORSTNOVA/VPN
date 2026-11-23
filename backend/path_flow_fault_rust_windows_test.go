//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

type pathInjectedSession struct {
	pathFlowSession
	name, currentName            string
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *pathInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.pathFlowSession.next()
	if e == nil {
		f, err := decodePathFrame(b)
		if err == nil {
			s.kind = f.kind
			s.currentName = f.name
		}
	}
	return b, n, e
}
func (s *pathInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target && (s.name == "" || s.currentName == s.name) {
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
	return s.pathFlowSession.advance(n, b)
}

func TestPathFlowActualFaultsNoReplay(t *testing.T) {
	if !rustPathFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 14; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q := pathServiceRequest{Op: "round"}
				if k == 5 || k == 13 {
					q = pathServiceRequest{Op: "switch", Target: "b", Region: "US", Trigger: "synthetic", Evidence: "synthetic"}
				}
				s := pathDiffBase()
				s.Pass["a|Extra"] = false
				var inj *pathInjectedSession
				pathDiffCompare(t, q, s, func(q pathServiceRequest, fx pathServiceEffects) pathServiceResult {
					r, used := runRustPathServiceWithStart(q, fx, func(b []byte) (pathFlowSession, error) {
						ss, e := startRustPathFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &pathInjectedSession{pathFlowSession: ss, target: k, nth: 1, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatalf("target not reached %v", pathFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type pathPanicFX struct {
	*pathDiffFX
	port string
}

func (f *pathPanicFX) Call(q pathServiceEffect) pathServiceReply {
	r := f.pathDiffFX.Call(q)
	if q.Kind == f.port {
		panic("after actual path effect")
	}
	return r
}
func TestPathFlowActualPanicCleanup(t *testing.T) {
	for _, port := range []string{"lock", "unlock", "running", "get", "proxy", "check", "set", "now", "candidates", "put", "save", "event"} {
		t.Run(port, func(t *testing.T) {
			q := pathServiceRequest{Op: "round"}
			s := pathDiffBase()
			s.Pass["a|Extra"] = false
			f := pathDiffNew(q, s)
			r, used := runRustPathService(q, &pathPanicFX{f, port})
			if !used || r.Err == nil {
				t.Fatal("panic false success")
			}
			if f.a.pathStateLocked().Busy {
				t.Fatal("busy leaked")
			}
			if !f.a.mu.TryLock() {
				t.Fatal("mutex leaked")
			}
			f.a.mu.Unlock()
			if len(f.puts) > 1 || f.saves > 1 || len(f.events) > 1 || len(f.checks) > 1 {
				t.Fatal("replayed effect")
			}
		})
	}
}
func TestPathFlowActualCapacityCancel(t *testing.T) {
	b, _ := pathStartInput(pathServiceRequest{Op: "baseline"})
	var all []pathFlowSession
	defer func() {
		for _, s := range all {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustPathFlow(b)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := pathDiffNew(pathServiceRequest{Op: "baseline"}, pathDiffBase())
	if _, used := runRustPathService(pathServiceRequest{Op: "baseline"}, f); used || len(f.trace) != 0 {
		t.Fatal("capacity effected")
	}
	for _, s := range all {
		s.close()
	}
	all = nil
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newPathMirror(pathServiceRequest{Op: "baseline"})
		m.next()
		m.close()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if runtime.NumGoroutine() > before+8 {
		t.Fatal("mirror leak")
	}
}

func TestPathFlowActualMutationFaults(t *testing.T) {
	type scenario struct {
		op, name string
		nth      int
	}
	var cases []scenario
	for _, n := range []string{"at", "results", "failures", "active", "running", "note", "switched", "pin", "last-switch", "append-switch"} {
		cases = append(cases, scenario{"round", n, 1})
	}
	cases = append(cases, scenario{"round", "busy", 2})
	for _, n := range []string{"alert-increment", "alert", "retry-mode", "retry-at", "phase"} {
		cases = append(cases, scenario{"baseline", n, 1})
	}
	cases = append(cases, scenario{"recovery", "demanded", 1}, scenario{"recovery", "running", 2})
	for _, c := range cases {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/%v", c.op, c.name, next), func(t *testing.T) {
				q := pathServiceRequest{Op: c.op, Mode: "manual"}
				s := pathDiffBase()
				if c.op == "round" {
					s.Pass["a|Extra"] = false
				}
				if c.op == "baseline" {
					s.Pass["a|Google"] = false
				}
				var inj *pathInjectedSession
				pathDiffCompare(t, q, s, func(q pathServiceRequest, fx pathServiceEffects) pathServiceResult {
					r, used := runRustPathServiceWithStart(q, fx, func(b []byte) (pathFlowSession, error) {
						ss, e := startRustPathFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &pathInjectedSession{pathFlowSession: ss, target: 9, name: c.name, nth: c.nth, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("mutation fault not reached")
				}
			})
		}
	}
}

type pathInspectSession struct {
	pathFlowSession
	t *testing.T
}

func (s *pathInspectSession) advance(n uint64, b []byte) error {
	for _, x := range []string{"https://synthetic.invalid", pathCheckPresets[0].URL, pathCheckPresets[1].URL, "synthetic PUT"} {
		if strings.Contains(string(b), x) {
			s.t.Fatal("raw endpoint or error entered native")
		}
	}
	return s.pathFlowSession.advance(n, b)
}
func TestPathFlowActualOpaqueBoundary(t *testing.T) {
	before := pathFlowRecoveries.Load()
	s := pathDiffBase()
	s.Pass["a|Extra"] = false
	s.FailPut = "b"
	pathDiffCompare(t, pathServiceRequest{Op: "round"}, s, func(q pathServiceRequest, fx pathServiceEffects) pathServiceResult {
		r, used := runRustPathServiceWithStart(q, fx, func(b []byte) (pathFlowSession, error) {
			ss, e := startRustPathFlow(b)
			if e != nil {
				return nil, e
			}
			return &pathInspectSession{ss, t}, nil
		})
		if !used {
			t.Fatal("not used")
		}
		return r
	})
	if pathFlowRecoveries.Load() != before {
		t.Fatal("normal failure recovered")
	}
}
