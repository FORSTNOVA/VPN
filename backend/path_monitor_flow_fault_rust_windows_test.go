//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type pathMonitorInjectedSession struct {
	pathMonitorFlowSession
	kind, target                 byte
	nextFailure, pending, failed bool
}

func (s *pathMonitorInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next")
	}
	b, n, e := s.pathMonitorFlowSession.next()
	if e == nil {
		f, er := decodePathMonitorFrame(b)
		if er == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *pathMonitorInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance")
		}
	}
	return s.pathMonitorFlowSession.advance(n, b)
}
func monitorFaultOp(k byte) string {
	switch k {
	case 3, 8, 13, 22:
		return "start"
	case 4:
		return "normalize"
	case 9:
		return "stop"
	case 19, 20:
		return "update"
	case 23:
		return "snapshot"
	}
	return "loop"
}
func TestPathMonitorFlowActualBoundaryFaults(t *testing.T) {
	for k := byte(1); k <= 24; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				op := monitorFaultOp(k)
				s := monitorDiffBase()
				if op == "start" {
					s.Cancel = false
				}
				var inj *pathMonitorInjectedSession
				monitorDiffCompare(t, op, s, func(q pathMonitorServiceRequest, fx pathMonitorServiceEffects) pathMonitorServiceResult {
					r, used := runRustPathMonitorServiceWithStart(q, fx, func(b []byte) (pathMonitorFlowSession, error) {
						ss, e := startRustPathMonitorFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &pathMonitorInjectedSession{pathMonitorFlowSession: ss, target: k, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatalf("boundary not reached: %v", pathMonitorFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type monitorPanicFX struct {
	*monitorDiffFX
	port string
}

func (f *monitorPanicFX) Call(q pathMonitorServiceEffect) pathMonitorServiceReply {
	r := f.monitorDiffFX.Call(q)
	if q.Kind == f.port {
		panic("after real fake effect")
	}
	return r
}
func TestPathMonitorFlowActualPanicCleanup(t *testing.T) {
	ports := map[string]string{"lock": "loop", "unlock": "loop", "ticker-new": "loop", "wait": "loop", "traffic": "loop", "samples": "loop", "round": "loop", "save": "update", "settings": "update", "response": "snapshot", "publish": "start", "spawn-loop": "start", "close": "stop", "ticker-stop": "loop"}
	for port, op := range ports {
		t.Run(port, func(t *testing.T) {
			s := monitorDiffBase()
			if op == "start" {
				s.Cancel = false
			}
			q := monitorDiffRequest(op)
			f := monitorDiffNew(q, s)
			borrowed := op == "start" || op == "stop"
			if borrowed {
				f.a.mu.Lock()
			}
			r, used := runRustPathMonitorService(q, &monitorPanicFX{f, port})
			if !used || r.Err == nil {
				t.Fatal("panic false success")
			}
			if borrowed {
				if f.a.mu.TryLock() {
					f.a.mu.Unlock()
					t.Fatal("borrowed released")
				}
				f.a.mu.Unlock()
			}
			if !f.a.mu.TryLock() {
				t.Fatal("owned lock leaked")
			}
			f.a.mu.Unlock()
			seen := map[string]bool{}
			for _, n := range f.stops {
				if seen[n] {
					t.Fatal("ticker closed twice")
				}
				seen[n] = true
			}
			if len(f.stops) != len(f.timers) {
				t.Fatal("ticker leaked", f.timers, f.stops)
			}
			if f.fetches > 1 || f.saves > 1 {
				t.Fatal("replayed effect")
			}
		})
	}
}
func TestPathMonitorFlowActualPartialTickerCreation(t *testing.T) {
	for _, at := range []string{"baseline", "demand", "retry"} {
		t.Run(at, func(t *testing.T) {
			q := monitorDiffRequest("loop")
			f := monitorDiffNew(q, monitorDiffBase())
			old := f.e.makeTicker
			f.e.makeTicker = func(n string, d time.Duration, register func(<-chan time.Time, func())) {
				old(n, d, register)
				if n == at {
					panic("after ticker registered")
				}
			}
			r, used := runRustPathMonitorService(q, f)
			if !used || r.Err == nil {
				t.Fatal("panic false success")
			}
			expected := []string{}
			for _, n := range []string{"baseline", "demand", "retry"} {
				expected = append([]string{n}, expected...)
				if n == at {
					break
				}
			}
			if !reflect.DeepEqual(f.stops, expected) {
				t.Fatal("partial cleanup order", f.stops, expected)
			}
			if len(f.work) != 0 {
				t.Fatal("spawned after create panic")
			}
		})
	}
}
func TestPathMonitorFlowActualCapacityAndCancel(t *testing.T) {
	input, e := pathMonitorStartInput(monitorDiffRequest("snapshot"))
	if e != nil {
		t.Fatal(e)
	}
	var sessions []pathMonitorFlowSession
	for i := 0; i < 64; i++ {
		s, e := startRustPathMonitorFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, s)
	}
	q := monitorDiffRequest("snapshot")
	f := monitorDiffNew(q, monitorDiffBase())
	_, used := runRustPathMonitorService(q, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity fallback performed effects")
	}
	for _, s := range sessions {
		s.close()
	}
	s, e := startRustPathMonitorFlow(input)
	if e != nil {
		t.Fatal(e)
	}
	s.close()
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newPathMonitorMirror(q)
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror worker leak")
	}
}

type monitorLeanFX struct{ f *monitorDiffFX }

func (e monitorLeanFX) Call(q pathMonitorServiceEffect) pathMonitorServiceReply { return e.f.e.Call(q) }
func (e monitorLeanFX) Release()                                                { e.f.Release() }

type monitorTokenSession struct {
	pathMonitorFlowSession
	timeTokens  map[int64]bool
	errorTokens map[int64]bool
	kind        byte
}

func (s *monitorTokenSession) next() ([]byte, uint64, error) {
	b, n, e := s.pathMonitorFlowSession.next()
	if e == nil {
		f, x := decodePathMonitorFrame(b)
		if x == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *monitorTokenSession) advance(n uint64, b []byte) error {
	r := quotaReader{b: b}
	r.byte()
	r.byte()
	r.byte()
	r.integer()
	tok := r.integer()
	if s.kind == 6 {
		s.timeTokens[tok] = true
	}
	if s.kind == 16 && tok != 0 {
		s.errorTokens[tok] = true
	}
	return s.pathMonitorFlowSession.advance(n, b)
}
func TestPathMonitorFlowActualLongLoopBoundedArena(t *testing.T) {
	q := monitorDiffRequest("loop")
	sp := monitorDiffBase()
	sp.FetchFail = true
	sp.Events = make([]string, 0, 30001)
	for i := 0; i < 15000; i++ {
		sp.Events = append(sp.Events, "retry", "demand")
	}
	sp.Events = append(sp.Events, "cancel")
	f := monitorDiffNew(q, sp)
	var ss *monitorTokenSession
	before := pathMonitorFlowRecoveries.Load()
	r, used := runRustPathMonitorServiceWithStart(q, monitorLeanFX{f}, func(b []byte) (pathMonitorFlowSession, error) {
		s, e := startRustPathMonitorFlow(b)
		if e != nil {
			return nil, e
		}
		ss = &monitorTokenSession{pathMonitorFlowSession: s, timeTokens: map[int64]bool{}, errorTokens: map[int64]bool{}}
		return ss, nil
	})
	if !used || r.Err != nil || pathMonitorFlowRecoveries.Load() != before {
		t.Fatal("long loop not native", r.Err, pathMonitorFlowRecoveryReason.Load())
	}
	if f.fetches != 15000 || f.clocks != 15000 || len(f.stops) != 3 {
		t.Fatal("incomplete loop", f.fetches, f.clocks, f.stops)
	}
	if len(ss.timeTokens) != 1 || len(ss.errorTokens) != 0 {
		t.Fatal("unbounded token growth", ss.timeTokens, ss.errorTokens)
	}
	a := newPathMonitorArena(q)
	for i := 0; i < 15000; i++ {
		for _, label := range []string{"a", "b"} {
			a.replyBytes(pathMonitorFrame{kind: 6, name: "demand", text: label}, pathMonitorServiceReply{Now: monitorDiffNow.Add(time.Duration(i))})
		}
		a.replyBytes(pathMonitorFrame{kind: 6, name: "retry"}, pathMonitorServiceReply{Now: monitorDiffNow})
		a.replyBytes(pathMonitorFrame{kind: 16}, pathMonitorServiceReply{Err: errors.New("repeated")})
		a.replyBytes(pathMonitorFrame{kind: 20}, pathMonitorServiceReply{Err: errors.New("save")})
	}
	if len(a.times) != 3 || len(a.timeSlots) != 3 || len(a.errs) != 1 {
		t.Fatal("arena grew", len(a.times), len(a.timeSlots), len(a.errs))
	}
}

type monitorOpaqueSession struct {
	pathMonitorFlowSession
	t *testing.T
}

func (s *monitorOpaqueSession) advance(n uint64, b []byte) error {
	if bytes.Contains(b, []byte("synthetic-secret")) {
		s.t.Fatal("credential entered native reply")
	}
	return s.pathMonitorFlowSession.advance(n, b)
}
func TestPathMonitorFlowActualOpaqueURL(t *testing.T) {
	q := monitorDiffRequest("normalize")
	q.Settings.Targets = []pathTarget{{URL: "https://user:synthetic-secret@synthetic.invalid/path", Enabled: true}}
	f := monitorDiffNew(q, monitorDiffBase())
	before := pathMonitorFlowRecoveries.Load()
	r, used := runRustPathMonitorServiceWithStart(q, f, func(b []byte) (pathMonitorFlowSession, error) {
		if bytes.Contains(b, []byte("synthetic-secret")) {
			t.Fatal("credential entered native request")
		}
		s, e := startRustPathMonitorFlow(b)
		if e != nil {
			return nil, e
		}
		return &monitorOpaqueSession{s, t}, nil
	})
	if !used || r.Err != nil || pathMonitorFlowRecoveries.Load() != before {
		t.Fatal("not native", r.Err, pathMonitorFlowRecoveryReason.Load())
	}
	if r.Settings.Targets[0].URL != q.Settings.Targets[0].URL {
		t.Fatal("opaque URL changed")
	}
}

func TestPathMonitorFlowActualClockSlotIsolation(t *testing.T) {
	sp := monitorDiffBase()
	sp.Grow = true
	sp.Events = []string{"demand", "retry", "demand", "retry", "demand", "cancel"}
	run := func(native bool) (pathMonitorServiceResult, *monitorDiffFX) {
		q := monitorDiffRequest("loop")
		f := monitorDiffNew(q, sp)
		testEditPath(f.a, func(p *servicePathState) {
			p.Config.Targets = append(p.Config.Targets, pathTarget{Label: "Another", URL: "https://synthetic.invalid", Enabled: true})
		})
		if !native {
			return runGoPathMonitorService(q, f), f
		}
		before := pathMonitorFlowRecoveries.Load()
		r, used := runRustPathMonitorService(q, f)
		if !used || pathMonitorFlowRecoveries.Load() != before {
			t.Fatal("clock slot recovered", pathMonitorFlowRecoveryReason.Load())
		}
		return r, f
	}
	a, x := run(false)
	b, y := run(true)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) {
		t.Fatal("throttle/retry clock slots overwritten")
	}
}
