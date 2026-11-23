//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type appFaultSession struct {
	appFlowSession
	target        byte
	afterNext     bool
	armed, failed bool
}

func (s *appFaultSession) next() ([]byte, uint64, error) {
	if s.armed && !s.failed {
		s.failed = true
		return nil, 0, errors.New("injected next")
	}
	return s.appFlowSession.next()
}
func (s *appFaultSession) advance(step uint64, b []byte) error {
	e := s.appFlowSession.advance(step, b)
	if e == nil && !s.failed && b[0] == s.target {
		if s.afterNext {
			s.armed = true
		} else {
			s.failed = true
			return errors.New("injected advance")
		}
	}
	return e
}

type appFaultFX struct {
	*appIndependentFX
	panicKind string
}

func (f *appFaultFX) Call(e appServiceEffect) appServiceReply {
	r := f.appIndependentFX.Call(e)
	if e.Kind == "events-read" {
		r.Count = 2
	}
	if e.Kind == f.panicKind {
		panic("after effect")
	}
	return r
}
func appFaultInput(k byte) (appServiceRequest, appIndependentSpec) {
	q := appServiceRequest{Op: "state", Params: defaultHealthParams(), Limit: "2", Interval: time.Second}
	s := appIndependentSpec{Base: appServiceReply{ProxyOn: true, JobNil: true, WebRTC: true, DNS: true}, Choice: appServiceReply{SelectedNode: "node", SelectionMode: "manual"}, Mode: appServiceReply{Mode: modeTUN, ModeSupported: true}, Params: defaultHealthParams(), Running: true, Cancel: true, Blocked: true, Store: true, Active: autoGroup, Nested: "node", Pin: "JP", Ticks: 2, Health: []appHealthFact{{State: healthHealthy}, {State: "odd", Cooldown: true}}, Regions: []appRegionFact{{Country: "JP", Verified: true}}}
	switch {
	case k == 6:
		s.Fail = "proxy"
	case k >= 16 && k <= 24:
		q.Op = "params"
	case k >= 25 && k <= 28:
		q.Op = "once"
	case k == 29:
		q.Op = "once"
		s.Active = blockedChoice
	case k >= 30 && k <= 32:
		q.Op = "stop"
	case k >= 33 && k <= 35 || k == 41:
		q.Op = "loop"
	case k >= 36 && k <= 38:
		q.Op = "events"
	}
	return q, s
}
func TestAppFlowActualBoundaryFaults(t *testing.T) {
	if !rustAppFlowAvailable() {
		t.Fatal("native missing")
	}
	for k := byte(1); k <= 41; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q, s := appFaultInput(k)
				a, b := &appFaultFX{appIndependentFX: &appIndependentFX{s: s}}, &appFaultFX{appIndependentFX: &appIndependentFX{s: s}}
				g := runGoAppService(q, a)
				var fault *appFaultSession
				r, used := runRustAppServiceWithStart(q, b, func(input []byte) (appFlowSession, error) {
					ss, e := startRustAppFlow(input)
					if e != nil {
						return nil, e
					}
					fault = &appFaultSession{appFlowSession: ss, target: k, afterNext: next}
					return fault, nil
				})
				if !used || fault == nil || !fault.failed {
					t.Fatal("fault not exercised", used)
				}
				if !reflect.DeepEqual(g, r) || !reflect.DeepEqual(a.trace, b.trace) || !reflect.DeepEqual(a.s, b.s) || a.releases != b.releases || b.owned {
					t.Fatal("replayed or incomplete", q, g, r, a.trace, b.trace)
				}
			})
		}
	}
}
func TestAppFlowActualPanicNoReplay(t *testing.T) {
	for _, k := range []byte{1, 2, 15, 17, 19, 21, 22, 23, 24, 28, 29, 30, 31, 32, 33, 35, 37, 38, 39, 40, 41} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			q, s := appFaultInput(k)
			f := &appFaultFX{appIndependentFX: &appIndependentFX{s: s}, panicKind: appPortKinds[k]}
			r, used := runRustAppService(q, f)
			if !used || r.Err == nil || f.owned {
				t.Fatal("panic succeeded/lock leaked", used, r)
			}
			n := 0
			for _, e := range f.trace {
				if e.Kind == f.panicKind {
					n++
				}
			}
			if n != 1 {
				t.Fatal("effect replay", n)
			}
		})
	}
}
func TestAppFlowActualCapacityCancel(t *testing.T) {
	input, _ := appStartInput(appServiceRequest{Op: "state"})
	var all []appFlowSession
	defer func() {
		for _, s := range all {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustAppFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	fx := &appIndependentFX{}
	_, used := runRustAppService(appServiceRequest{Op: "state"}, fx)
	if used || len(fx.trace) != 0 {
		t.Fatal("capacity effect")
	}
	for _, s := range all {
		s.close()
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newAppMirror(appServiceRequest{Op: "state"})
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leaked")
	}
}

type appLongFX struct{ ticks, once, stop, releases int }

func (f *appLongFX) Call(e appServiceEffect) appServiceReply {
	switch e.Kind {
	case "ticker-new":
	case "wait":
		f.ticks++
		return appServiceReply{Flag: f.ticks <= 60000}
	case "once":
		f.once++
	case "ticker-stop":
		f.stop++
	default:
		panic(e.Kind)
	}
	return appServiceReply{}
}
func (f *appLongFX) Release() { f.releases++ }
func TestAppFlowActualLongLoop(t *testing.T) {
	f := &appLongFX{}
	before := appFlowRecoveries.Load()
	r, ok := runRustAppService(appServiceRequest{Op: "loop", Interval: time.Second}, f)
	if !ok || r.Err != nil || appFlowRecoveries.Load() != before || f.ticks != 60001 || f.once != 60000 || f.stop != 1 || f.releases != 1 {
		t.Fatal("loop incomplete", ok, r, f)
	}
}

type appAfterEffectPanic struct {
	appServiceEffects
	kind string
}

func (f appAfterEffectPanic) Call(e appServiceEffect) appServiceReply {
	r := f.appServiceEffects.Call(e)
	if e.Kind == f.kind {
		panic("after actual effect")
	}
	return r
}
func TestAppFlowActualCancelOwnership(t *testing.T) {
	for _, kind := range []byte{21, 22, 23} {
		for _, panicAfter := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", kind, panicAfter), func(t *testing.T) {
				op := "start"
				if kind == 21 {
					op = "stop"
				}
				a, e := appSafeEffects(t, op)
				var old chan struct{}
				if kind == 21 {
					old = make(chan struct{})
					a.patrolCancel = old
				}
				spawns := 0
				e.spawn = func(c <-chan struct{}, d time.Duration) {
					spawns++
					if c != a.patrolCancel {
						t.Error("wrong resource")
					}
				}
				a.mu.Lock()
				var fault *appFaultSession
				var r appServiceResult
				var used bool
				if panicAfter {
					r, used = runRustAppService(e.q, appAfterEffectPanic{e, appPortKinds[kind]})
				} else {
					r, used = runRustAppServiceWithStart(e.q, e, func(b []byte) (appFlowSession, error) {
						ss, x := startRustAppFlow(b)
						if x != nil {
							return nil, x
						}
						fault = &appFaultSession{appFlowSession: ss, target: kind, afterNext: true}
						return fault, nil
					})
				}
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("borrowed lock released")
				}
				a.mu.Unlock()
				if !used || (r.Err != nil) != panicAfter {
					t.Fatal("wrong outcome", used, r)
				}
				if !panicAfter && (fault == nil || !fault.failed) {
					t.Fatal("fault not hit")
				}
				if kind == 21 {
					if a.patrolCancel != nil {
						t.Fatal("cancel retained")
					}
					select {
					case <-old:
					default:
						t.Fatal("old open")
					}
				} else if panicAfter && kind == 22 {
					if a.patrolCancel != nil || spawns != 0 {
						t.Fatal("untransferred resource leaked")
					}
				} else {
					if spawns != 1 || a.patrolCancel == nil {
						t.Fatal("handoff lost")
					}
					select {
					case <-a.patrolCancel:
						t.Fatal("transferred resource closed")
					default:
					}
					close(a.patrolCancel)
					a.patrolCancel = nil
				}
				e.Release()
			})
		}
	}
}
func TestAppFlowActualTickerOwnership(t *testing.T) {
	for _, kind := range []byte{33, 35} {
		for _, panicAfter := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", kind, panicAfter), func(t *testing.T) {
				_, e := appSafeEffects(t, "loop")
				cancel := make(chan struct{})
				close(cancel)
				e.cancel = cancel
				e.q.Interval = time.Second
				stops := 0
				e.newTicker = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() { stops++ } }
				var r appServiceResult
				var used bool
				var fault *appFaultSession
				if panicAfter {
					r, used = runRustAppService(e.q, appAfterEffectPanic{e, appPortKinds[kind]})
				} else {
					r, used = runRustAppServiceWithStart(e.q, e, func(b []byte) (appFlowSession, error) {
						ss, x := startRustAppFlow(b)
						if x != nil {
							return nil, x
						}
						fault = &appFaultSession{appFlowSession: ss, target: kind, afterNext: true}
						return fault, nil
					})
				}
				if !used || (r.Err != nil) != panicAfter || stops != 1 {
					t.Fatal("ticker ownership", used, r, stops)
				}
				if !panicAfter && (fault == nil || !fault.failed) {
					t.Fatal("fault not hit")
				}
				e.Release()
				if stops != 1 {
					t.Fatal("repeated tickerstop")
				}
			})
		}
	}
}
func TestAppFlowActualNestedLoop(t *testing.T) {
	a, e := appSafeEffects(t, "loop")
	e.q.Interval = time.Second
	cancel := make(chan struct{})
	e.cancel = cancel
	ticks := make(chan time.Time, 2)
	ticks <- time.Time{}
	ticks <- time.Time{}
	stops, calls := 0, 0
	e.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() { stops++ } }
	e.once = func() {
		child := newAppControlEffects(a, appServiceRequest{Op: "once"})
		child.hooks = map[string]func(appServiceEffect) appServiceReply{"running": func(appServiceEffect) appServiceReply { return appServiceReply{} }}
		r, used := runRustAppService(child.q, child)
		if !used || r.Err != nil {
			panic("nested native unavailable")
		}
		calls++
		if calls == 2 {
			close(cancel)
		}
	}
	before := appFlowRecoveries.Load()
	r, used := runRustAppService(e.q, e)
	if !used || r.Err != nil || appFlowRecoveries.Load() != before || calls != 2 || stops != 1 {
		t.Fatal("nested loop", used, r, calls, stops)
	}
	assertAppMutexReleased(t, a)
}
func TestAppFlowActualWriterOwnership(t *testing.T) {
	t.Setenv("SMARTVPN_RUST_APP_FLOW", "1")
	if !rustAppFlowAvailable() {
		t.Fatal("native unavailable")
	}
	before := appFlowRecoveries.Load()
	TestAppServiceSafeRealStateWriterOwnership(t)
	if appFlowRecoveries.Load() != before {
		t.Fatal("writer recovery replay")
	}
}
