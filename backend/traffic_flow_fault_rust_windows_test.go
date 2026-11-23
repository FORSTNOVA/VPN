//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

type trafficInjectedSession struct {
	trafficFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *trafficInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.trafficFlowSession.next()
	if e == nil {
		f, err := decodeTrafficFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *trafficInjectedSession) advance(n uint64, b []byte) error {
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
	return s.trafficFlowSession.advance(n, b)
}

func TestTrafficFlowActualFaultsNoReplay(t *testing.T) {
	if !rustTrafficFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 22; k++ {
		if k == 17 {
			continue
		}
		op := "current"
		switch k {
		case 2, 10, 11:
			op = "collect"
		case 12, 13:
			op = "start"
		case 14:
			op = "stop"
		case 15, 16, 18, 19:
			op = "loop"
		case 20, 21, 22:
			op = "query"
		}
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q := trafficServiceRequest{Op: op}
				spec := trafficDiffBase()
				spec.Cancel = op == "stop"
				spec.GetFail = k == 5
				var inj *trafficInjectedSession
				trafficDiffCompare(t, q, spec, func(q trafficServiceRequest, fx trafficServiceEffects) trafficServiceResult {
					r, used := runRustTrafficServiceWithStart(q, fx, func(b []byte) (trafficFlowSession, error) {
						s, e := startRustTrafficFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &trafficInjectedSession{trafficFlowSession: s, target: k, nth: 1, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not handled")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("failure not reached")
				}
			})
		}
	}
}

type trafficPanicFX struct {
	*trafficDiffFX
	port string
}

func (f *trafficPanicFX) Call(q trafficEffect) trafficReply {
	r := f.trafficDiffFX.Call(q)
	if q.Kind == f.port {
		panic("synthetic post-effect panic")
	}
	return r
}
func TestTrafficFlowActualPanicCleanup(t *testing.T) {
	for _, c := range []struct{ op, port string }{{"current", "lock"}, {"current", "get"}, {"current", "baseline"}, {"query", "read"}, {"start", "install"}, {"start", "spawn"}, {"stop", "close"}, {"loop", "ticker-start"}, {"loop", "wait"}, {"loop", "record"}, {"loop", "unlock"}, {"loop", "ticker-stop"}} {
		t.Run(c.port, func(t *testing.T) {
			s := trafficDiffBase()
			s.Cancel = c.op == "stop"
			q := trafficServiceRequest{Op: c.op}
			f := trafficDiffNew(q, s)
			if f.e.borrowed {
				f.a.mu.Lock()
			}
			r, used := runRustTrafficService(q, &trafficPanicFX{f, c.port})
			if !used || r.Err == nil {
				t.Fatal("panic falsely succeeded")
			}
			maxGets := 1
			if c.port == "ticker-stop" {
				maxGets = 2
			}
			if f.spawns > 1 || f.gets > maxGets || f.records > maxGets || f.stops > 1 {
				t.Fatal("repeated effect")
			}
			if c.op == "loop" && f.stops != 1 {
				t.Fatal("ticker leak")
			}
			if f.e.borrowed {
				if f.a.mu.TryLock() {
					t.Fatal("borrowed unlock")
				}
				f.a.mu.Unlock()
			}
			if !f.a.mu.TryLock() {
				t.Fatal("owned lock leaked")
			}
			f.a.mu.Unlock()
		})
	}
}
func TestTrafficFlowActualCapacityCancel(t *testing.T) {
	input, _ := trafficStartInput(trafficServiceRequest{Op: "start"})
	var all []trafficFlowSession
	defer func() {
		for _, s := range all {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustTrafficFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := trafficDiffNew(trafficServiceRequest{Op: "start"}, trafficDiffBase())
	if _, used := runRustTrafficService(trafficServiceRequest{Op: "start"}, f); used || len(f.calls) != 0 {
		t.Fatal("capacity touched effects")
	}
	for _, s := range all {
		s.close()
	}
	all = nil
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newTrafficMirror(trafficServiceRequest{Op: "loop"})
		m.next()
		m.close()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if runtime.NumGoroutine() > before+8 {
		t.Fatal("mirror leak")
	}
}
func TestTrafficFlowActualLongFiniteLoop(t *testing.T) {
	s := trafficDiffBase()
	s.Events = make([]bool, 15000)
	for i := range s.Events {
		s.Events[i] = true
	}
	before := trafficFlowRecoveries.Load()
	trafficDiffCompare(t, trafficServiceRequest{Op: "loop"}, s, func(q trafficServiceRequest, fx trafficServiceEffects) trafficServiceResult {
		r, used := runRustTrafficService(q, fx)
		if !used {
			t.Fatal("not used")
		}
		return r
	})
	if trafficFlowRecoveries.Load() != before {
		t.Fatal("normal loop recovered")
	}
}
func TestTrafficFlowActualTickerCreatorPanic(t *testing.T) {
	q := trafficServiceRequest{Op: "loop"}
	f := trafficDiffNew(q, trafficDiffBase())
	f.e.tickerStart = func() { f.ticks++; panic("fake allocation published then panic") }
	r, used := runRustTrafficService(q, f)
	if !used || r.Err == nil || f.ticks != 1 || f.stops != 1 || f.waits != 0 {
		t.Fatalf("creator cleanup mismatch %v %v ticks=%d stops=%d waits=%d", used, r.Err, f.ticks, f.stops, f.waits)
	}
	f.Release()
	if f.stops != 1 {
		t.Fatal("cleanup repeated")
	}
}
