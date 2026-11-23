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

type chainProbeInjectedSession struct {
	chainProbeFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *chainProbeInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.chainProbeFlowSession.next()
	if e == nil {
		f, err := decodeChainProbeFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *chainProbeInjectedSession) advance(n uint64, b []byte) error {
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
	return s.chainProbeFlowSession.advance(n, b)
}

func TestChainProbeFlowActualFaultsNoReplay(t *testing.T) {
	if !rustChainProbeFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 38; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				op := "probe"
				if k == 3 {
					op = "measure-both"
				}
				if k >= 33 && k <= 35 {
					op = "list"
				}
				s := probeDiffBase()
				s.Records = probeDiffRecords()
				var inj *chainProbeInjectedSession
				probeDiffCompare(t, op, s, func(q chainProbeServiceRequest, fx chainProbeServiceEffects) chainProbeServiceResult {
					r, used := runRustChainProbeServiceWithStart(q, fx, func(b []byte) (chainProbeFlowSession, error) {
						ss, e := startRustChainProbeFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &chainProbeInjectedSession{chainProbeFlowSession: ss, target: k, nth: 1, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatalf("fault not reached %v", chainProbeFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type chainProbePanicFX struct {
	*probeDiffFX
	port string
}

func (f *chainProbePanicFX) Call(q chainProbeEffect) chainProbeReply {
	r := f.probeDiffFX.Call(q)
	if q.Kind == f.port {
		panic("after actual fake effect")
	}
	return r
}
func TestChainProbeFlowActualPanicCleanup(t *testing.T) {
	for _, port := range []string{"lock", "resolver", "ech-apply", "adapter", "transport", "request-new", "request-do", "body-close", "request-cancel", "idle-close", "node-close", "resolver-reset", "save", "response"} {
		t.Run(port, func(t *testing.T) {
			q := probeDiffRequest("probe")
			f := probeDiffNew(q, probeDiffBase())
			r, used := runRustChainProbeService(q, &chainProbePanicFX{f, port})
			if !used || r.Err == nil {
				t.Fatal("panic false success")
			}
			counts := map[string]int{}
			for _, k := range f.cleanup {
				counts[k]++
				max := 1
				if k == "body-close" || k == "request-cancel" {
					max = 2
				}
				if counts[k] > max {
					t.Fatalf("duplicatecleanup %v", f.cleanup)
				}
			}
			if f.e.body != nil || f.e.requestCancel != nil || f.e.idleClose != nil || f.e.resolverReset != nil || f.e.parentCancel != nil {
				t.Fatal("resource left owned")
			}
			for _, close := range f.e.nodeClosers {
				if close != nil {
					t.Fatal("node left owned")
				}
			}
			if !f.a.mu.TryLock() {
				t.Fatal("lock leaked")
			}
			f.a.mu.Unlock()
			if probeCount(f, "response") > 1 || probeCount(f, "save") > 1 || probeCount(f, "request-do") > 2 {
				t.Fatal("replayed effect")
			}
		})
	}
}

type chainProbeInspectSession struct {
	chainProbeFlowSession
	t *testing.T
}

func (s *chainProbeInspectSession) advance(n uint64, b []byte) error {
	for _, secret := range []string{"node.test", "second.test", "ech.test", "synthetic-dns", probeTarget} {
		if strings.Contains(string(b), secret) {
			s.t.Fatal("opaque target leaked into native")
		}
	}
	return s.chainProbeFlowSession.advance(n, b)
}
func TestChainProbeFlowActualOpaqueBoundary(t *testing.T) {
	before := chainProbeFlowRecoveries.Load()
	probeDiffCompare(t, "probe", probeDiffBase(), func(q chainProbeServiceRequest, fx chainProbeServiceEffects) chainProbeServiceResult {
		r, used := runRustChainProbeServiceWithStart(q, fx, func(b []byte) (chainProbeFlowSession, error) {
			if len(b) != 1 {
				t.Fatal("request unexpectedly serialized graph")
			}
			s, e := startRustChainProbeFlow(b)
			if e != nil {
				return nil, e
			}
			return &chainProbeInspectSession{s, t}, nil
		})
		if !used {
			t.Fatal("unused")
		}
		return r
	})
	if chainProbeFlowRecoveries.Load() != before {
		t.Fatal("normal recovery")
	}
}
func TestChainProbeFlowActualCapacityCancel(t *testing.T) {
	b, _ := chainProbeStartInput(probeDiffRequest("measure-both"))
	var all []chainProbeFlowSession
	defer func() {
		for _, s := range all {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustChainProbeFlow(b)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := probeDiffNew(probeDiffRequest("probe"), probeDiffBase())
	if _, used := runRustChainProbeService(probeDiffRequest("probe"), f); used || len(f.trace) != 0 {
		t.Fatal("capacity touched effects")
	}
	for _, s := range all {
		s.close()
	}
	all = nil
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newChainProbeMirror(probeDiffRequest("measure-both"))
		m.next()
		m.close()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if runtime.NumGoroutine() > before+8 {
		t.Fatal("worker leak")
	}
}
func TestChainProbeFlowActualFailureContinuation(t *testing.T) {
	for _, c := range []struct {
		port string
		at   int
		kind byte
	}{{"request-do", 1, 21}, {"request-new", 1, 20}, {"adapter", 1, 18}, {"resolver", 0, 14}, {"ech", 0, 15}, {"save", 0, 32}} {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", c.port, next), func(t *testing.T) {
				s := probeDiffBase()
				s.FailKind = c.port
				s.FailAt = c.at
				var inj *chainProbeInjectedSession
				probeDiffCompare(t, "probe", s, func(q chainProbeServiceRequest, fx chainProbeServiceEffects) chainProbeServiceResult {
					r, used := runRustChainProbeServiceWithStart(q, fx, func(b []byte) (chainProbeFlowSession, error) {
						ss, e := startRustChainProbeFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &chainProbeInjectedSession{chainProbeFlowSession: ss, target: c.kind, nth: c.at + 1, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not handled")
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
