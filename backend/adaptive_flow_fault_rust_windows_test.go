//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/miekg/dns"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var adaptiveFaultErr = errors.New("injected adaptive ABI fault")

type adaptiveFaultSession struct {
	adaptiveFlowSession
	kind     byte
	after    bool
	hit      bool
	failNext bool
	current  byte
}

func (s *adaptiveFaultSession) next() ([]byte, uint64, error) {
	if s.failNext {
		s.failNext = false
		return nil, 0, adaptiveFaultErr
	}
	b, n, e := s.adaptiveFlowSession.next()
	if e == nil {
		f, x := decodeAdaptiveFrame(b)
		if x == nil {
			s.current = f.kind
		}
	}
	return b, n, e
}
func (s *adaptiveFaultSession) advance(n uint64, b []byte) error {
	if e := s.adaptiveFlowSession.advance(n, b); e != nil {
		return e
	}
	if !s.hit && s.current == s.kind {
		s.hit = true
		if s.after {
			s.failNext = true
		} else {
			return adaptiveFaultErr
		}
	}
	return nil
}

type adaptiveFaultCase struct {
	q    adaptiveServiceRequest
	seed int
	fail string
}

func adaptiveFaultCases(t *testing.T) map[byte]adaptiveFaultCase {
	t.Helper()
	out := map[byte]adaptiveFaultCase{}
	ops := []string{"exchange", "resolve", "switch", "serve", "monitor", "check", "check-worker", "confirm", "note", "block", "create", "close"}
	for _, op := range ops {
		for seed := 0; seed < 64; seed++ {
			for _, fail := range []string{"", "pack", "request", "do", "body-read", "exchange-child", "listen", "reject"} {
				q := adaptiveServiceRequest{Op: op, Index: seed % 2, From: seed % 2, To: 1 - seed%2}
				fx := &adaptiveIndependentFX{Seed: seed, Fail: fail}
				runGoAdaptiveService(q, fx)
				for _, e := range fx.Trace {
					for k, name := range adaptivePortKinds {
						if name == e.Kind {
							if _, ok := out[k]; !ok {
								out[k] = adaptiveFaultCase{q, seed, fail}
							}
						}
					}
				}
			}
		}
	}
	for k, name := range adaptivePortKinds {
		if _, ok := out[k]; !ok {
			t.Errorf("uncovered kind %d %s", k, name)
		}
	}
	return out
}
func TestAdaptiveFlowActualBoundaryFaults(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	for k, c := range adaptiveFaultCases(t) {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, after), func(t *testing.T) {
				a := &adaptiveIndependentFX{Seed: c.seed, Fail: c.fail}
				b := &adaptiveIndependentFX{Seed: c.seed, Fail: c.fail}
				want := runGoAdaptiveService(c.q, a)
				var fault *adaptiveFaultSession
				got, used := runRustAdaptiveServiceWithStart(c.q, b, func(in []byte) (adaptiveFlowSession, error) {
					s, e := startRustAdaptiveFlow(in)
					if e != nil {
						return nil, e
					}
					fault = &adaptiveFaultSession{adaptiveFlowSession: s, kind: k, after: after}
					return fault, nil
				})
				if !used || fault == nil || !fault.hit || fmt.Sprint(want.Err) != fmt.Sprint(got.Err) || want.Bool != got.Bool || want.Value != got.Value || !reflect.DeepEqual(a, b) {
					t.Fatalf("kind%d hit%v used%v want%+v got%+v\n%+v\n%+v", k, fault, used, want, got, a, b)
				}
			})
		}
	}
}

type adaptivePanicFX struct {
	*adaptiveIndependentFX
	kind string
	hit  bool
}

func (f *adaptivePanicFX) Call(e adaptiveServiceEffect) adaptiveServiceReply {
	r := f.adaptiveIndependentFX.Call(e)
	if !f.hit && e.Kind == f.kind {
		f.hit = true
		panic("after actual adaptive effect")
	}
	return r
}
func TestAdaptiveFlowActualPanicNoReplay(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	for k, c := range adaptiveFaultCases(t) {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			fx := &adaptivePanicFX{adaptiveIndependentFX: &adaptiveIndependentFX{Seed: c.seed, Fail: c.fail}, kind: adaptivePortKinds[k]}
			got, used := runRustAdaptiveService(c.q, fx)
			if !used || got.Err == nil || !fx.hit || fx.Released != 1 || fx.Finalized != 0 {
				t.Fatalf("panic result used%v got%+v fx%+v", used, got, fx)
			}
			n := 0
			for _, e := range fx.Trace {
				if e.Kind == fx.kind {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("repeated failed effect %s %d", fx.kind, n)
			}
		})
	}
}
func TestAdaptiveFlowActualCapacityCancel(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	in, _ := adaptiveStartInput(adaptiveServiceRequest{Op: "exchange"})
	var sessions []adaptiveFlowSession
	defer func() {
		for _, s := range sessions {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustAdaptiveFlow(in)
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, s)
	}
	fx := &adaptiveIndependentFX{}
	_, used := runRustAdaptiveService(adaptiveServiceRequest{Op: "exchange"}, fx)
	if used || len(fx.Trace) != 0 || fx.Released != 0 {
		t.Fatal("capacity touched effect")
	}
	for _, s := range sessions {
		s.close()
	}
	sessions = nil
	base := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newAdaptiveMirror(adaptiveServiceRequest{Op: "exchange"})
		m.close()
	}
	if runtime.NumGoroutine() > base+4 {
		t.Fatal("mirror goroutine leak")
	}
}

type adaptiveTrackedBody struct {
	io.Reader
	closed *atomic.Int32
}

func (b *adaptiveTrackedBody) Close() error { b.closed.Add(1); return nil }

type adaptiveActualPanicFX struct {
	e    *appAdaptiveEffects
	kind string
	hit  bool
}

func (f *adaptiveActualPanicFX) Call(q adaptiveServiceEffect) adaptiveServiceReply {
	r := f.e.Call(q)
	if q.Kind == f.kind && !f.hit {
		f.hit = true
		panic("after adaptive actual effect")
	}
	return r
}
func (f *adaptiveActualPanicFX) Release() { f.e.Release() }
func (f *adaptiveActualPanicFX) FinalizeAdaptiveResult(q adaptiveServiceRequest, r adaptiveServiceResult) adaptiveServiceResult {
	return f.e.FinalizeAdaptiveResult(q, r)
}
func TestAdaptiveFlowActualExchangeResources(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	for _, kind := range []byte{3, 6, 7, 8, 4} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", kind, panics), func(t *testing.T) {
				var calls, closed atomic.Int32
				q := new(dns.Msg).SetQuestion("fake.example.", dns.TypeTXT)
				packet, _ := new(dns.Msg).SetReply(q).Pack()
				relay := &adaptiveDNS{ctx: context.Background(), client: &http.Client{Transport: dohRoundTrip(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: 200, Body: &adaptiveTrackedBody{bytes.NewReader(packet), &closed}}, nil
				})}}
				request := adaptiveServiceRequest{Op: "exchange"}
				e := newAdaptiveEffects(relay, nil, request)
				e.query = q
				var fx adaptiveServiceEffects = e
				var panicFX *adaptiveActualPanicFX
				if panics {
					panicFX = &adaptiveActualPanicFX{e: e, kind: adaptivePortKinds[kind]}
					fx = panicFX
				}
				var f *adaptiveFaultSession
				got, used := runRustAdaptiveServiceWithStart(request, fx, func(in []byte) (adaptiveFlowSession, error) {
					s, x := startRustAdaptiveFlow(in)
					if x != nil {
						return nil, x
					}
					f = &adaptiveFaultSession{adaptiveFlowSession: s, kind: kind, after: true}
					return f, nil
				})
				e.Release()
				if !used || calls.Load() != 1 || closed.Load() != 1 {
					t.Fatalf("resources used%v calls%d close%d", used, calls.Load(), closed.Load())
				}
				if panics {
					if !panicFX.hit || got.Err == nil {
						t.Fatal("panic success")
					}
				} else if !f.hit || got.Err != nil || !got.Bool || e.answer == nil {
					t.Fatalf("continuation %+v", got)
				}
			})
		}
	}
}
func TestAdaptiveFlowActualLateWorkerNonJoining(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	for _, target := range []byte{15, 16, 18, 19, 9, 17, 12} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			var workers sync.WaitGroup
			var calls, closed, cancels, stops atomic.Int32
			rt := defaultAdaptiveRuntime()
			rt.timeout = func(p context.Context, d time.Duration) (context.Context, context.CancelFunc) {
				if d != 5*time.Second {
					t.Error("timeout changed")
				}
				ctx, c := context.WithCancel(p)
				return ctx, func() { cancels.Add(1); c() }
			}
			rt.timer = func(d time.Duration) (<-chan time.Time, func()) {
				if d != 500*time.Millisecond {
					t.Error("headstart changed")
				}
				<-firstStarted
				c := make(chan time.Time, 1)
				c <- time.Time{}
				return c, func() { stops.Add(1) }
			}
			rt.spawn = func(f func()) { workers.Add(1); go func() { defer workers.Done(); f() }() }
			var fault *adaptiveFaultSession
			rt.run = func(q adaptiveServiceRequest, fx adaptiveServiceEffects) adaptiveServiceResult {
				start := adaptiveFlowStart(startRustAdaptiveFlow)
				if q.Op == "resolve" {
					start = func(in []byte) (adaptiveFlowSession, error) {
						s, x := startRustAdaptiveFlow(in)
						if x != nil {
							return nil, x
						}
						fault = &adaptiveFaultSession{adaptiveFlowSession: s, kind: target, after: true}
						return fault, nil
					}
				}
				r, used := runRustAdaptiveServiceWithStart(q, fx, start)
				if !used {
					t.Error("nested fallback")
				}
				return r
			}
			relay := &adaptiveDNS{ctx: context.Background(), runtime: rt}
			relay.client = &http.Client{Transport: dohRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				var q dns.Msg
				if x := q.Unpack(body); x != nil {
					return nil, x
				}
				if r.URL.Host == dohIPs[0] {
					close(firstStarted)
					<-releaseFirst
				}
				packet, _ := new(dns.Msg).SetReply(&q).Pack()
				return &http.Response{StatusCode: 200, Body: &adaptiveTrackedBody{bytes.NewReader(packet), &closed}}, nil
			})}
			q := adaptiveServiceRequest{Op: "resolve"}
			e := newAdaptiveEffects(relay, nil, q)
			e.query = new(dns.Msg).SetQuestion("fake.example.", dns.TypeTXT)
			out := make(chan adaptiveServiceResult, 1)
			go func() { out <- runAdaptiveWithEffects(q, e) }()
			var r adaptiveServiceResult
			select {
			case r = <-out:
			case <-time.After(5 * time.Second):
				close(releaseFirst)
				t.Fatal("resolve joined blocked old worker")
			}
			if r.Err != nil || !r.Bool || fault == nil || !fault.hit || relay.preferred.Load() != 1 || cancels.Load() != 1 || stops.Load() != 1 {
				t.Errorf("result %+v cancels%d stops%d", r, cancels.Load(), stops.Load())
			}
			close(releaseFirst)
			workers.Wait()
			if calls.Load() != 2 || closed.Load() != 2 {
				t.Fatalf("replayed/lost requests %d/%d", calls.Load(), closed.Load())
			}
			e.Release()
			if cancels.Load() != 1 || stops.Load() != 1 {
				t.Fatal("resource cleanup repeated")
			}
		})
	}
}

type adaptiveLongFX struct{ left, waits, checks, stops, releases int }

func (f *adaptiveLongFX) Release() { f.releases++ }
func (f *adaptiveLongFX) Call(q adaptiveServiceEffect) adaptiveServiceReply {
	switch q.Kind {
	case "ticker-new":
	case "monitor-wait":
		f.waits++
		f.left--
		return adaptiveServiceReply{Flag: f.left >= 0}
	case "check-child":
		f.checks++
	case "ticker-stop":
		f.stops++
	default:
		panic("unexpected long monitor " + q.Kind)
	}
	return adaptiveServiceReply{}
}
func TestAdaptiveFlowActualLongMonitor(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	n := adaptiveFlowRecoveries.Load()
	fx := &adaptiveLongFX{left: 60000}
	r, used := runRustAdaptiveService(adaptiveServiceRequest{Op: "monitor"}, fx)
	if !used || r.Err != nil || fx.checks != 60000 || fx.waits != 60001 || fx.stops != 1 || fx.releases != 1 || adaptiveFlowRecoveries.Load() != n {
		t.Fatalf("long monitor %+v %+v", fx, r)
	}
}

type adaptiveCorruptSession struct {
	adaptiveFlowSession
	kind byte
	mode int
	hit  bool
}

func (s *adaptiveCorruptSession) next() ([]byte, uint64, error) {
	b, n, e := s.adaptiveFlowSession.next()
	if e != nil {
		return b, n, e
	}
	f, x := decodeAdaptiveFrame(b)
	if x == nil && f.kind == s.kind && !s.hit {
		s.hit = true
		switch s.mode {
		case 0:
			b = b[:1]
		case 1:
			b = append(b, 0xff)
		case 2:
			n++
		case 3:
			b[0] = 253
		}
	}
	return b, n, nil
}
func TestAdaptiveFlowActualMalformedFrames(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	cases := adaptiveFaultCases(t)
	for _, kind := range []byte{6, 16, 10, 44, 48, 54} {
		for mode := 0; mode < 4; mode++ {
			t.Run(fmt.Sprintf("%d/%d", kind, mode), func(t *testing.T) {
				c := cases[kind]
				a := &adaptiveIndependentFX{Seed: c.seed, Fail: c.fail}
				b := &adaptiveIndependentFX{Seed: c.seed, Fail: c.fail}
				want := runGoAdaptiveService(c.q, a)
				var s *adaptiveCorruptSession
				got, used := runRustAdaptiveServiceWithStart(c.q, b, func(in []byte) (adaptiveFlowSession, error) {
					x, e := startRustAdaptiveFlow(in)
					if e != nil {
						return nil, e
					}
					s = &adaptiveCorruptSession{adaptiveFlowSession: x, kind: kind, mode: mode}
					return s, nil
				})
				if !used || s == nil || !s.hit || fmt.Sprint(want.Err) != fmt.Sprint(got.Err) || want.Bool != got.Bool || want.Value != got.Value || !reflect.DeepEqual(a, b) {
					t.Fatalf("malformed frame not continued once %+v %+v", want, got)
				}
			})
		}
	}
}

func adaptiveOwnedCreate(q adaptiveServiceRequest) (*appAdaptiveEffects, *[]string) {
	order := []string{}
	e := newAdaptiveEffects(&adaptiveDNS{}, nil, q)
	e.relayCancel = func() {}
	e.listenerClose = func() {}
	e.clientClose = func() {}
	e.shutdown = func() error { return nil }
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{}
	e.hooks["listen"] = func(adaptiveServiceEffect) adaptiveServiceReply {
		e.newRelay = true
		e.listenerClose = func() { order = append(order, "listener") }
		return adaptiveServiceReply{}
	}
	e.hooks["relay-context"] = func(adaptiveServiceEffect) adaptiveServiceReply {
		e.relayCancel = func() { order = append(order, "cancel") }
		return adaptiveServiceReply{}
	}
	e.hooks["client"] = func(adaptiveServiceEffect) adaptiveServiceReply {
		e.clientClose = func() { order = append(order, "idle") }
		return adaptiveServiceReply{}
	}
	e.hooks["server"] = func(adaptiveServiceEffect) adaptiveServiceReply {
		e.shutdown = func() error { order = append(order, "shutdown"); return nil }
		return adaptiveServiceReply{}
	}
	e.hooks["server-start"] = func(adaptiveServiceEffect) adaptiveServiceReply {
		e.serverStarted = true
		return adaptiveServiceReply{}
	}
	e.hooks["server-ready"] = func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }
	return e, &order
}

type adaptiveClosePanicSession struct {
	adaptiveFlowSession
	count *int
}

func (s *adaptiveClosePanicSession) close() {
	*s.count++
	s.adaptiveFlowSession.close()
	panic("uncertain native close")
}
func TestAdaptiveFlowActualCreateOwnership(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	for k := byte(49); k <= 54; k++ {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, panics), func(t *testing.T) {
				q := adaptiveServiceRequest{Op: "create"}
				e, order := adaptiveOwnedCreate(q)
				var fx adaptiveServiceEffects = e
				var p *adaptiveActualPanicFX
				if panics {
					p = &adaptiveActualPanicFX{e: e, kind: adaptivePortKinds[k]}
					fx = p
				}
				var fault *adaptiveFaultSession
				r, used := runRustAdaptiveServiceWithStart(q, fx, func(in []byte) (adaptiveFlowSession, error) {
					s, x := startRustAdaptiveFlow(in)
					if x != nil {
						return nil, x
					}
					fault = &adaptiveFaultSession{adaptiveFlowSession: s, kind: k, after: true}
					return fault, nil
				})
				e.Release()
				if !used {
					t.Fatal("fallback")
				}
				if panics {
					want := []string{}
					if k >= 50 {
						want = append(want, "cancel")
					}
					if k >= 53 {
						want = append(want, "shutdown")
					}
					if k >= 51 {
						want = append(want, "idle")
					}
					want = append(want, "listener")
					if r.Err == nil || !p.hit || !reflect.DeepEqual(*order, want) {
						t.Fatalf("partial cleanup %+v %v != %v", r, *order, want)
					}
				} else if r.Err != nil || !fault.hit || len(*order) != 0 || e.newRelay {
					t.Fatalf("handoff lost resource %+v %v", r, *order)
				}
			})
		}
	}
	t.Run("session-close-before-handoff", func(t *testing.T) {
		q := adaptiveServiceRequest{Op: "create"}
		e, order := adaptiveOwnedCreate(q)
		n := 0
		r, used := runRustAdaptiveServiceWithStart(q, e, func(in []byte) (adaptiveFlowSession, error) {
			s, x := startRustAdaptiveFlow(in)
			if x != nil {
				return nil, x
			}
			return &adaptiveClosePanicSession{s, &n}, nil
		})
		e.Release()
		if !used || r.Err == nil || n != 1 || !reflect.DeepEqual(*order, []string{"cancel", "shutdown", "idle", "listener"}) {
			t.Fatalf("close error cleanup %+v %d %v", r, n, *order)
		}
	})
}
func TestAdaptiveFlowActualWriterPanic(t *testing.T) {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("actual native required")
	}
	q := adaptiveServiceRequest{Op: "serve"}
	r := &adaptiveDNS{ctx: context.Background()}
	e := newAdaptiveEffects(r, nil, q)
	fx := &adaptiveIndependentFX{Seed: 2}
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{}
	for _, name := range adaptivePortKinds {
		e.hooks[name] = func(x adaptiveServiceEffect) adaptiveServiceReply {
			if x.Kind == "answer-write" {
				panic("writer-original")
			}
			return fx.Call(x)
		}
	}
	e.rt.run = func(q adaptiveServiceRequest, f adaptiveServiceEffects) adaptiveServiceResult {
		v, used := runRustAdaptiveService(q, f)
		if !used {
			t.Error("native writer fallback")
		}
		return v
	}
	defer func() {
		if p := recover(); p != "writer-original" {
			t.Fatalf("writer panic changed %v", p)
		}
	}()
	runAdaptiveWithEffects(q, e)
	t.Fatal("writer panic swallowed")
}
