//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

type diagnosticInjectedSession struct {
	diagnosticFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *diagnosticInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.diagnosticFlowSession.next()
	if e == nil {
		f, err := decodeDiagnosticFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *diagnosticInjectedSession) advance(n uint64, b []byte) error {
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
	return s.diagnosticFlowSession.advance(n, b)
}

func TestDiagnosticFlowActualFaultsNoReplay(t *testing.T) {
	if !rustDiagnosticFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 25; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q := diagnoseServiceRequest{Op: "diagnose"}
				s := diagnosticDiffBase()
				if k == 5 {
					s.Chain = true
				}
				if k == 10 {
					s.IPv4 = ""
				}
				var inj *diagnosticInjectedSession
				diagnosticDiffCompare(t, q, s, func(q diagnoseServiceRequest, fx diagnoseServiceEffects) diagnoseServiceResult {
					r, used := runRustDiagnoseServiceWithStart(q, fx, func(b []byte) (diagnosticFlowSession, error) {
						ss, e := startRustDiagnosticFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &diagnosticInjectedSession{diagnosticFlowSession: ss, target: k, nth: 1, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatalf("target not reached reason=%v", diagnosticFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type diagnosticPanicFX struct {
	*diagnosticDiffFX
	port string
}

func (f *diagnosticPanicFX) Call(q diagnoseEffect) diagnoseReply {
	r := f.diagnosticDiffFX.Call(q)
	if q.Kind == f.port {
		panic("post effect fault")
	}
	return r
}
func TestDiagnosticFlowActualPanicCleanup(t *testing.T) {
	for _, op := range []string{"diagnose", "ensure"} {
		for _, port := range []string{"start", "list", "put", "response", "sites-start", "sites-join"} {
			if op == "ensure" && (port == "response" || port == "sites-start" || port == "sites-join") {
				continue
			}
			t.Run(op+"/"+port, func(t *testing.T) {
				q := diagnoseServiceRequest{Op: op}
				f := diagnosticDiffNew(q, diagnosticDiffBase())
				if f.e.borrowed {
					f.a.mu.Lock()
				}
				r, used := runRustDiagnoseService(q, &diagnosticPanicFX{f, port})
				if !used || r.Err == nil {
					t.Fatal("panic falsely succeeded")
				}
				stops := 0
				for _, h := range f.hooks {
					if h == "stop" {
						stops++
					}
				}
				if stops != 1 {
					t.Fatalf("cleanup count %d", stops)
				}
				if diagnosticCount(f, "response") > 1 || diagnosticCount(f, "start") > 1 || diagnosticCount(f, "sites-start") > 1 {
					t.Fatal("replayed effects")
				}
				if f.e.borrowed {
					if f.a.mu.TryLock() {
						t.Fatal("borrowed unlock")
					}
					f.a.mu.Unlock()
				}
				if !f.a.mu.TryLock() {
					t.Fatal("owned lock leak")
				}
				f.a.mu.Unlock()
			})
		}
	}
}
func TestDiagnosticFlowActualCapacityCancel(t *testing.T) {
	input, _ := diagnosticStartInput(diagnoseServiceRequest{Op: "details"})
	var ss []diagnosticFlowSession
	defer func() {
		for _, s := range ss {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustDiagnosticFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		ss = append(ss, s)
	}
	f := diagnosticDiffNew(diagnoseServiceRequest{Op: "details"}, diagnosticDiffBase())
	if _, ok := runRustDiagnoseService(diagnoseServiceRequest{Op: "details"}, f); ok || len(f.trace) != 0 {
		t.Fatal("capacity effected")
	}
	for _, s := range ss {
		s.close()
	}
	ss = nil
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newDiagnosticMirror(diagnoseServiceRequest{Op: "details"})
		m.next()
		m.close()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if runtime.NumGoroutine() > before+8 {
		t.Fatal("mirror leak")
	}
}
func TestDiagnosticFlowActualEnsureTransferFaults(t *testing.T) {
	for _, k := range []byte{2, 3, 4, 6, 7} {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				var inj *diagnosticInjectedSession
				q := diagnoseServiceRequest{Op: "ensure"}
				diagnosticDiffCompare(t, q, diagnosticDiffBase(), func(q diagnoseServiceRequest, fx diagnoseServiceEffects) diagnoseServiceResult {
					r, used := runRustDiagnoseServiceWithStart(q, fx, func(b []byte) (diagnosticFlowSession, error) {
						s, e := startRustDiagnosticFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &diagnosticInjectedSession{diagnosticFlowSession: s, target: k, nth: 1, nextFailure: next}
						return inj, nil
					})
					if !used || r.Err != nil || !r.Temporary {
						t.Fatal("ensure transfer failed")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("fault not reached")
				}
			})
		}
	}
}
