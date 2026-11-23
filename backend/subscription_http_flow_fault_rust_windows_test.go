//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type subHTTPFaultSession struct {
	subscriptionHTTPFlowSession
	target        byte
	afterNext     bool
	armed, failed bool
}

func (s *subHTTPFaultSession) next() ([]byte, uint64, error) {
	if s.armed && !s.failed {
		s.failed = true
		return nil, 0, errors.New("injected next")
	}
	return s.subscriptionHTTPFlowSession.next()
}
func (s *subHTTPFaultSession) advance(step uint64, b []byte) error {
	e := s.subscriptionHTTPFlowSession.advance(step, b)
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
func subHTTPFaultInput(k byte) (subscriptionHTTPServiceRequest, *subHTTPIndependentFX) {
	q := subscriptionHTTPServiceRequest{Op: "diagnostics"}
	f := &subHTTPIndependentFX{Seed: 3}
	switch {
	case k <= 5:
		q.Op = "fetch"
		f.Seed = 1
	case k >= 6 && k <= 10:
		q.Op = "refresh"
		f.Seed = 2
	case k == 22:
		f.Seed = 2
	case k >= 34 && k <= 36:
		q.Op = "start"
	case k == 37:
		q.Op = "stop"
		f.Cancel = true
	case k >= 38 && k <= 41:
		q.Op = "loop"
	case k == 42:
		q.Op = "tick"
	}
	return q, f
}
func TestSubscriptionHTTPFlowActualBoundaryFaults(t *testing.T) {
	if !rustSubscriptionHTTPFlowAvailable() {
		t.Fatal("native missing")
	}
	for k := byte(1); k <= 42; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q, a := subHTTPFaultInput(k)
				_, b := subHTTPFaultInput(k)
				g := runGoSubscriptionHTTPService(q, a)
				var fault *subHTTPFaultSession
				r, used := runRustSubscriptionHTTPServiceWithStart(q, b, func(input []byte) (subscriptionHTTPFlowSession, error) {
					ss, e := startRustSubscriptionHTTPFlow(input)
					if e != nil {
						return nil, e
					}
					fault = &subHTTPFaultSession{subscriptionHTTPFlowSession: ss, target: k, afterNext: next}
					return fault, nil
				})
				if !used || fault == nil || !fault.failed {
					t.Fatal("fault not exercised", used)
				}
				if !reflect.DeepEqual(g, r) || !reflect.DeepEqual(a, b) {
					t.Fatal("replay/partial result", q, g, r, a, b)
				}
			})
		}
	}
}

type subHTTPAfterPanic struct {
	subscriptionHTTPServiceEffects
	kind string
}

func (f subHTTPAfterPanic) Call(e subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
	r := f.subscriptionHTTPServiceEffects.Call(e)
	if e.Kind == f.kind {
		panic("after actual effect")
	}
	return r
}
func TestSubscriptionHTTPFlowActualPanicNoReplay(t *testing.T) {
	for _, k := range []byte{1, 2, 4, 6, 7, 9, 10, 14, 15, 16, 17, 20, 29, 30, 31, 32, 33, 35, 36, 37, 38, 40, 41, 42} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			q, f := subHTTPFaultInput(k)
			r, used := runRustSubscriptionHTTPService(q, subHTTPAfterPanic{f, subscriptionHTTPPortKinds[k]})
			if !used || r.Err == nil || f.Locked {
				t.Fatal("panic success/lock leak", used, r)
			}
			n := 0
			for _, e := range f.Trace {
				if e.Kind == subscriptionHTTPPortKinds[k] {
					n++
				}
			}
			if n != 1 {
				t.Fatal("replayed", n)
			}
		})
	}
}
func TestSubscriptionHTTPFlowActualCapacityCancel(t *testing.T) {
	b, _ := subscriptionHTTPStartInput(subscriptionHTTPServiceRequest{Op: "fetch"})
	var all []subscriptionHTTPFlowSession
	defer func() {
		for _, s := range all {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustSubscriptionHTTPFlow(b)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := &subHTTPIndependentFX{}
	_, used := runRustSubscriptionHTTPService(subscriptionHTTPServiceRequest{Op: "fetch"}, f)
	if used || len(f.Trace) != 0 {
		t.Fatal("capacity effect")
	}
	for _, s := range all {
		s.close()
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newSubscriptionHTTPMirror(subscriptionHTTPServiceRequest{Op: "fetch"})
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leak")
	}
}

type subHTTPLongFX struct{ ticks, children, stops, releases int }

func (f *subHTTPLongFX) Call(e subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
	switch e.Kind {
	case "ticker-new":
	case "wait":
		f.ticks++
		return subscriptionHTTPServiceReply{Flag: f.ticks <= 60000}
	case "tick-child":
		f.children++
	case "ticker-stop":
		f.stops++
	default:
		panic(e.Kind)
	}
	return subscriptionHTTPServiceReply{}
}
func (f *subHTTPLongFX) Release() { f.releases++ }
func TestSubscriptionHTTPFlowActualLongLoop(t *testing.T) {
	f := &subHTTPLongFX{}
	before := subscriptionHTTPFlowRecoveries.Load()
	r, ok := runRustSubscriptionHTTPService(subscriptionHTTPServiceRequest{Op: "loop"}, f)
	if !ok || r.Err != nil || subscriptionHTTPFlowRecoveries.Load() != before || f.ticks != 60001 || f.children != 60000 || f.stops != 1 || f.releases != 1 {
		t.Fatal("long loop incomplete", ok, r, f)
	}
}

type subHTTPTrackedBody struct {
	io.Reader
	closes *int
}

func (b *subHTTPTrackedBody) Close() error { *b.closes++; return nil }

type subHTTPUnlockedWriter struct {
	*httptest.ResponseRecorder
	a      *app
	t      *testing.T
	closes *int
	writes int
	crash  bool
}

func (w *subHTTPUnlockedWriter) Write(b []byte) (int, error) {
	w.writes++
	if !w.a.mu.TryLock() {
		w.t.Fatal("diagnostics writer retained mutex")
	}
	w.a.mu.Unlock()
	if *w.closes != 0 {
		w.t.Fatal("body closed before response")
	}
	if w.crash {
		panic("writer failed")
	}
	return w.ResponseRecorder.Write(b)
}
func subHTTPActualFixture(t *testing.T) (*app, *appSubscriptionHTTPEffects, *int, *int, *subHTTPUnlockedWriter) {
	a := &app{}
	e := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "diagnostics"})
	calls, closes := new(int), new(int)
	e.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{"source-snapshot": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
		e.subscriptionURL = "https://unit.invalid/sub?z=private&a=redacted"
		e.home = t.TempDir()
		return subscriptionHTTPServiceReply{Flag: true}
	}}
	e.do = func(r *http.Request) (*http.Response, error) {
		*calls++
		if !a.mu.TryLock() {
			t.Fatal("network under lock")
		}
		a.mu.Unlock()
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: &subHTTPTrackedBody{strings.NewReader("vmess://invalid"), closes}}, nil
	}
	e.readFile = func(string) ([]byte, error) { return nil, errors.New("no fake cache") }
	e.parseNodes = func([]byte) []proxyNode { return nil }
	w := &subHTTPUnlockedWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t, closes: closes}
	e.w = w
	return a, e, calls, closes, w
}
func TestSubscriptionHTTPFlowActualBodyAndWriter(t *testing.T) {
	for _, k := range []byte{15, 16, 31, 32, 33} {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				a, e, calls, closes, w := subHTTPActualFixture(t)
				var fault *subHTTPFaultSession
				r, used := runRustSubscriptionHTTPServiceWithStart(e.q, e, func(b []byte) (subscriptionHTTPFlowSession, error) {
					ss, x := startRustSubscriptionHTTPFlow(b)
					if x != nil {
						return nil, x
					}
					fault = &subHTTPFaultSession{subscriptionHTTPFlowSession: ss, target: k, afterNext: next}
					return fault, nil
				})
				if !used || r.Err != nil || fault == nil || !fault.failed || *calls != 1 || *closes != 1 || w.writes != 1 {
					t.Fatal("body/request replay", used, r, *calls, *closes, w.writes)
				}
				assertAppMutexReleased(t, a)
				e.Release()
				if *closes != 1 {
					t.Fatal("reclose")
				}
			})
		}
	}
	t.Run("public-writer-panic", func(t *testing.T) {
		t.Setenv("SMARTVPN_RUST_SUBSCRIPTION_HTTP_FLOW", "1")
		a, e, calls, closes, w := subHTTPActualFixture(t)
		w.crash = true
		var p any
		func() { defer func() { p = recover() }(); runSubscriptionHTTPWithEffects(e.q, e) }()
		if p != "writer failed" || *calls != 1 || *closes != 1 || w.writes != 1 {
			t.Fatal("writer swallowed/replayed", p, *calls, *closes, w.writes)
		}
		assertAppMutexReleased(t, a)
	})
}
func TestSubscriptionHTTPFlowActualSchedulerOwnership(t *testing.T) {
	for _, k := range []byte{35, 36, 37, 38, 40} {
		for _, crash := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, crash), func(t *testing.T) {
				a := &app{}
				op := "start"
				if k == 37 {
					op = "stop"
				}
				if k >= 38 {
					op = "loop"
				}
				e := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: op})
				var old chan struct{}
				spawns, stops := 0, 0
				if k == 37 {
					old = make(chan struct{})
					a.autoMergeCancel = old
				}
				e.spawn = func(c <-chan struct{}) {
					spawns++
					if c != a.autoMergeCancel {
						t.Fatal("wrong published channel")
					}
				}
				if k >= 38 {
					c := make(chan struct{})
					close(c)
					e.cancel = c
					e.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
						if d != 10*time.Minute {
							t.Fatal("interval", d)
						}
						return make(chan time.Time), func() { stops++ }
					}
				} else {
					a.mu.Lock()
				}
				var r subscriptionHTTPServiceResult
				var used bool
				var fault *subHTTPFaultSession
				if crash {
					r, used = runRustSubscriptionHTTPService(e.q, subHTTPAfterPanic{e, subscriptionHTTPPortKinds[k]})
				} else {
					r, used = runRustSubscriptionHTTPServiceWithStart(e.q, e, func(b []byte) (subscriptionHTTPFlowSession, error) {
						ss, x := startRustSubscriptionHTTPFlow(b)
						if x != nil {
							return nil, x
						}
						fault = &subHTTPFaultSession{subscriptionHTTPFlowSession: ss, target: k, afterNext: true}
						return fault, nil
					})
				}
				if k < 38 {
					if a.mu.TryLock() {
						a.mu.Unlock()
						t.Fatal("borrowed unlocked")
					}
					a.mu.Unlock()
				}
				if !used || (r.Err != nil) != crash {
					t.Fatal("outcome", used, r)
				}
				if !crash && (fault == nil || !fault.failed) {
					t.Fatal("fault not hit")
				}
				switch k {
				case 35:
					if crash {
						if a.autoMergeCancel != nil || spawns != 0 {
							t.Fatal("untransferred resource leaked")
						}
					} else if spawns != 1 {
						t.Fatal("missing spawn")
					}
				case 36:
					if spawns != 1 || a.autoMergeCancel == nil {
						t.Fatal("handoff lost")
					}
				case 37:
					if a.autoMergeCancel != nil {
						t.Fatal("cancel retained")
					}
					select {
					case <-old:
					default:
						t.Fatal("old channel open")
					}
				case 38, 40:
					if stops != 1 {
						t.Fatal("ticker stop", stops)
					}
				}
				if a.autoMergeCancel != nil {
					select {
					case <-a.autoMergeCancel:
						t.Fatal("transferred channel closed")
					default:
					}
					close(a.autoMergeCancel)
					a.autoMergeCancel = nil
				}
				e.Release()
				if k >= 38 && stops != 1 {
					t.Fatal("ticker replay")
				}
			})
		}
	}
}
func TestSubscriptionHTTPFlowActualNestedTick(t *testing.T) {
	a := &app{}
	e := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "loop"})
	cancel := make(chan struct{})
	e.cancel = cancel
	ticks := make(chan time.Time, 2)
	ticks <- time.Time{}
	ticks <- time.Time{}
	calls, stops := 0, 0
	e.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() { stops++ } }
	e.tick = func() {
		child := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "tick"})
		child.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{"auto-tick": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			calls++
			return subscriptionHTTPServiceReply{}
		}}
		r, ok := runRustSubscriptionHTTPService(child.q, child)
		if !ok || r.Err != nil {
			panic("native child unavailable")
		}
		if calls == 2 {
			close(cancel)
		}
	}
	before := subscriptionHTTPFlowRecoveries.Load()
	r, ok := runRustSubscriptionHTTPService(e.q, e)
	if !ok || r.Err != nil || calls != 2 || stops != 1 || subscriptionHTTPFlowRecoveries.Load() != before {
		t.Fatal("nested loop", ok, r, calls, stops)
	}
	assertAppMutexReleased(t, a)
}
