//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

var bootFaultErr = errors.New("injected bootstrap ABI fault")

type bootFaultSession struct {
	bootstrapFlowSession
	kind     byte
	after    bool
	hit      bool
	failNext bool
	current  byte
}

func (s *bootFaultSession) next() ([]byte, uint64, error) {
	if s.failNext {
		s.failNext = false
		return nil, 0, bootFaultErr
	}
	b, n, e := s.bootstrapFlowSession.next()
	if e == nil {
		f, x := decodeBootstrapFrame(b)
		if x == nil {
			s.current = f.kind
		}
	}
	return b, n, e
}
func (s *bootFaultSession) advance(n uint64, b []byte) error {
	if e := s.bootstrapFlowSession.advance(n, b); e != nil {
		return e
	}
	if !s.hit && s.current == s.kind {
		s.hit = true
		if s.after {
			s.failNext = true
		} else {
			return bootFaultErr
		}
	}
	return nil
}

type bootFaultCase struct {
	q    bootstrapServiceRequest
	seed int
	fail string
}

func bootFaultCases(t *testing.T) map[byte]bootFaultCase {
	t.Helper()
	out := map[byte]bootFaultCase{}
	ops := []string{"join", "lock", "running", "probe", "handover", "identity", "owner", "identify", "proxy", "clear-owner", "portable", "write-record", "remove-record", "windows", "android-start", "android-stop", "jni-start", "tunnel"}
	for _, op := range ops {
		for seed := 0; seed < 64; seed++ {
			for _, fail := range []string{"", "lock-try", "owner-write", "tunnel-decode", "drop", "record-rename", "proxy-script"} {
				q := bootstrapServiceRequest{Op: op, Elevated: seed%2 == 0, Enable: seed%3 == 0, Authorized: seed%4 == 0, Wait: time.Second, ShutdownWait: time.Second}
				fx := &bootIndependentFX{Seed: seed, Fail: fail}
				runGoBootstrapService(q, fx)
				for _, e := range fx.Trace {
					for k, name := range bootstrapPortKinds {
						if name == e.Kind {
							if _, ok := out[k]; !ok {
								out[k] = bootFaultCase{q, seed, fail}
							}
						}
					}
				}
			}
		}
	}
	for k, name := range bootstrapPortKinds {
		if _, ok := out[k]; !ok {
			t.Errorf("uncovered kind %d %s", k, name)
		}
	}
	return out
}
func TestBootstrapFlowActualBoundaryFaults(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	for k, c := range bootFaultCases(t) {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, after), func(t *testing.T) {
				a := &bootIndependentFX{Seed: c.seed, Fail: c.fail}
				b := &bootIndependentFX{Seed: c.seed, Fail: c.fail}
				want := runGoBootstrapService(c.q, a)
				var fault *bootFaultSession
				got, used := runRustBootstrapServiceWithStart(c.q, b, func(in []byte) (bootstrapFlowSession, error) {
					s, e := startRustBootstrapFlow(in)
					if e != nil {
						return nil, e
					}
					fault = &bootFaultSession{bootstrapFlowSession: s, kind: k, after: after}
					return fault, nil
				})
				if !used || fault == nil || !fault.hit || fmt.Sprint(want.Err) != fmt.Sprint(got.Err) || want.Bool != got.Bool || want.Reuse != got.Reuse || want.Lock != got.Lock || !reflect.DeepEqual(a, b) {
					t.Fatalf("kind%d hit%v used%v want%+v got%+v\n%+v\n%+v", k, fault, used, want, got, a, b)
				}
			})
		}
	}
}

type bootPanicFX struct {
	*bootIndependentFX
	kind string
	hit  bool
}

func (f *bootPanicFX) Call(e bootstrapServiceEffect) bootstrapServiceReply {
	r := f.bootIndependentFX.Call(e)
	if !f.hit && e.Kind == f.kind {
		f.hit = true
		panic("after actual bootstrap effect")
	}
	return r
}
func TestBootstrapFlowActualPanicNoReplay(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	for k, c := range bootFaultCases(t) {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			fx := &bootPanicFX{bootIndependentFX: &bootIndependentFX{Seed: c.seed, Fail: c.fail}, kind: bootstrapPortKinds[k]}
			got, used := runRustBootstrapService(c.q, fx)
			if !used || got.Err == nil || !fx.hit || fx.Held || fx.Releases != 1 || fx.Finalized != 0 {
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
func TestBootstrapFlowActualCapacityCancel(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	in, _ := bootstrapStartInput(bootstrapServiceRequest{Op: "lock"})
	var sessions []bootstrapFlowSession
	defer func() {
		for _, s := range sessions {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustBootstrapFlow(in)
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, s)
	}
	fx := &bootIndependentFX{}
	_, used := runRustBootstrapService(bootstrapServiceRequest{Op: "lock"}, fx)
	if used || len(fx.Trace) != 0 || fx.Releases != 0 {
		t.Fatal("capacity touched effect")
	}
	for _, s := range sessions {
		s.close()
	}
	sessions = nil
	base := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newBootstrapMirror(bootstrapServiceRequest{Op: "lock"})
		m.close()
	}
	if runtime.NumGoroutine() > base+4 {
		t.Fatal("mirror goroutine leak")
	}
}

// All platform and clock ports are hooks. The actual executor only owns fake closures.
func bootOwnedLockFX(q bootstrapServiceRequest) (*appBootstrapEffects, *int, *bool) {
	closes := 0
	held := false
	e := &appBootstrapEffects{q: q, hooks: map[string]func(bootstrapServiceEffect) bootstrapServiceReply{}}
	e.hooks["lock-open"] = func(bootstrapServiceEffect) bootstrapServiceReply {
		e.lockClose = func() { closes++; held = false }
		return bootstrapServiceReply{}
	}
	e.hooks["deadline"] = func(bootstrapServiceEffect) bootstrapServiceReply { return bootstrapServiceReply{} }
	e.hooks["lock-try"] = func(bootstrapServiceEffect) bootstrapServiceReply { return bootstrapServiceReply{} }
	e.hooks["lock-acquired"] = func(bootstrapServiceEffect) bootstrapServiceReply {
		held = true
		e.lockRelease = func() { closes++; held = false }
		return bootstrapServiceReply{}
	}
	return e, &closes, &held
}

type bootClosePanicSession struct {
	bootstrapFlowSession
	closes *int
}

func (s *bootClosePanicSession) close() {
	*s.closes++
	s.bootstrapFlowSession.close()
	panic("native close uncertain")
}
func TestBootstrapFlowActualHandoffAfterSessionClose(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	q := bootstrapServiceRequest{Op: "lock"}
	for _, failClose := range []bool{false, true} {
		t.Run(fmt.Sprint(failClose), func(t *testing.T) {
			fx, closes, held := bootOwnedLockFX(q)
			nativeCloses := 0
			got, used := runRustBootstrapServiceWithStart(q, fx, func(in []byte) (bootstrapFlowSession, error) {
				s, e := startRustBootstrapFlow(in)
				if e != nil {
					return nil, e
				}
				if failClose {
					return &bootClosePanicSession{s, &nativeCloses}, nil
				}
				return s, nil
			})
			fx.Release()
			if !used {
				t.Fatal("fallback")
			}
			if failClose {
				if got.Err == nil || *held || *closes != 1 || nativeCloses != 1 {
					t.Fatalf("failed close handoff: %+v %v %d %d", got, *held, *closes, nativeCloses)
				}
			} else {
				if got.Err != nil || !got.Lock || !*held || *closes != 0 {
					t.Fatalf("lost returned lock %+v", got)
				}
			}
		})
	}
}

type bootLongLockFX struct{ remaining, tries, sleeps, closed, releases int }

func (f *bootLongLockFX) Release() { f.releases++ }
func (f *bootLongLockFX) Call(e bootstrapServiceEffect) bootstrapServiceReply {
	switch e.Kind {
	case "lock-open", "deadline":
		return bootstrapServiceReply{}
	case "lock-try":
		f.tries++
		return bootstrapServiceReply{Err: bootFaultErr, Flag: true}
	case "before":
		f.remaining--
		return bootstrapServiceReply{Flag: f.remaining >= 0}
	case "sleep":
		f.sleeps++
	case "lock-close":
		f.closed++
	case "failure":
		return bootstrapServiceReply{Err: bootFaultErr}
	default:
		panic("unsafe long lock port " + e.Kind)
	}
	return bootstrapServiceReply{}
}
func TestBootstrapFlowActualLongLockBounded(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	before := bootstrapFlowRecoveries.Load()
	fx := &bootLongLockFX{remaining: 60000}
	r, used := runRustBootstrapService(bootstrapServiceRequest{Op: "lock"}, fx)
	if !used || !errors.Is(r.Err, bootFaultErr) || fx.tries != 60001 || fx.sleeps != 60000 || fx.closed != 1 || fx.releases != 1 || bootstrapFlowRecoveries.Load() != before {
		t.Fatalf("long lock %+v result%+v", fx, r)
	}
	// The reply arena has a fixed 256-slot error ledger regardless of retries.
	a := newBootstrapArena(bootstrapServiceRequest{})
	for i := 0; i < 15000; i++ {
		a.replyBytes(bootstrapFrame{kind: 14}, bootstrapServiceReply{Err: bootFaultErr})
	}
	n := 0
	for _, e := range a.errs {
		if e != nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("error slot growth %d", n)
	}
}

func TestBootstrapFlowActualRequestResources(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	for _, op := range []string{"probe", "handover", "identify"} {
		for _, target := range []string{"service-request", "service-read", "service-decode", "body-close", "identify-request", "identify-decode", "idle-close"} {
			if op == "identify" && strings.HasPrefix(target, "service-") {
				continue
			}
			if op != "identify" && strings.HasPrefix(target, "identify-") {
				continue
			}
			if op != "identify" && target == "idle-close" {
				continue
			}
			for _, panics := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%v", op, target, panics), func(t *testing.T) {
					q := bootstrapServiceRequest{Op: op, ShutdownWait: 0}
					e := newBootstrapEffects(q)
					e.record = serviceRecord{Port: 1234, Token: "synthetic"}
					e.clock = func() time.Time { return time.Time{} }
					e.sleep = func(time.Duration) { t.Fatal("unexpected sleep") }
					e.platform = func(x bootstrapServiceEffect) bootstrapServiceReply { panic("unexpected platform " + x.Kind) }
					requests, closed, idle := 0, 0, 0
					var order []string
					response := func() *http.Response {
						return &http.Response{StatusCode: 200, Body: &bootCloseCallback{Reader: strings.NewReader(`{"app":"` + appName + `"}`), close: func() { closed++; order = append(order, "body") }}}
					}
					e.request = func(_ serviceRecord, _, _ string) (*http.Response, error) { requests++; return response(), nil }
					e.identifyDo = func(string) (*http.Response, error) { requests++; return response(), nil }
					e.hooks = map[string]func(bootstrapServiceEffect) bootstrapServiceReply{"identify-client": func(bootstrapServiceEffect) bootstrapServiceReply {
						e.idleClose = func() { idle++; order = append(order, "idle") }
						return bootstrapServiceReply{}
					}}
					fx := &bootActualPanicFX{e: e, target: target, panics: panics}
					var sess *bootFaultSession
					got, used := runRustBootstrapServiceWithStart(q, fx, func(in []byte) (bootstrapFlowSession, error) {
						s, x := startRustBootstrapFlow(in)
						if x != nil {
							return nil, x
						}
						k := byte(0)
						for kind, name := range bootstrapPortKinds {
							if name == target {
								k = kind
							}
						}
						sess = &bootFaultSession{bootstrapFlowSession: s, kind: k, after: true}
						return sess, nil
					})
					e.Release()
					if !used || !fx.hit {
						t.Fatalf("not reached %+v", got)
					}
					if panics && got.Err == nil {
						t.Fatal("panic swallowed as success")
					}
					if !panics && !sess.hit {
						t.Fatal("fault not injected")
					}
					if closed != requests || requests < 1 || requests > 2 {
						t.Fatalf("requests%d closes%d", requests, closed)
					}
					if op == "identify" && (idle != 1 || len(order) != 2 || order[0] != "body" || order[1] != "idle") {
						t.Fatalf("cleanup order %v", order)
					}
				})
			}
		}
	}
}

type bootActualPanicFX struct {
	e           *appBootstrapEffects
	target      string
	panics, hit bool
}

func (f *bootActualPanicFX) Call(q bootstrapServiceEffect) bootstrapServiceReply {
	r := f.e.Call(q)
	if q.Kind == f.target && !f.hit {
		f.hit = true
		if f.panics {
			panic("after actual resource effect")
		}
	}
	return r
}
func (f *bootActualPanicFX) Release() { f.e.Release() }
func (f *bootActualPanicFX) FinalizeBootstrapResult(q bootstrapServiceRequest, r bootstrapServiceResult) bootstrapServiceResult {
	return f.e.FinalizeBootstrapResult(q, r)
}

var _ io.ReadCloser = (*bootCloseCallback)(nil)

func TestBootstrapFlowActualRegistryLedger(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	for _, target := range []string{"owner-key", "owner-delete", "owner-key-close"} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", target, panics), func(t *testing.T) {
				q := bootstrapServiceRequest{Op: "clear-owner"}
				e := &appBootstrapEffects{q: q}
				opened, deleted, closed := 0, 0, 0
				e.platform = func(x bootstrapServiceEffect) bootstrapServiceReply {
					switch x.Kind {
					case "owner-read":
						return bootstrapServiceReply{Flag: true, Other: true}
					case "owner-key":
						opened++
						e.closeKey = func() { closed++ }
					case "owner-delete":
						deleted++
					case "owner-key-close":
						if f := e.closeKey; f != nil {
							e.closeKey = nil
							f()
						}
					default:
						panic("unexpected registry port " + x.Kind)
					}
					return bootstrapServiceReply{}
				}
				fx := &bootActualPanicFX{e: e, target: target, panics: panics}
				var sess *bootFaultSession
				got, used := runRustBootstrapServiceWithStart(q, fx, func(in []byte) (bootstrapFlowSession, error) {
					s, x := startRustBootstrapFlow(in)
					if x != nil {
						return nil, x
					}
					var k byte
					for id, name := range bootstrapPortKinds {
						if name == target {
							k = id
						}
					}
					sess = &bootFaultSession{bootstrapFlowSession: s, kind: k, after: true}
					return sess, nil
				})
				e.Release()
				if !used || !fx.hit || opened != 1 || closed != 1 || deleted > 1 {
					t.Fatalf("registry %+v %d/%d/%d", got, opened, deleted, closed)
				}
				if panics {
					if got.Err == nil {
						t.Fatal("panic no error")
					}
					if target == "owner-key" && deleted != 0 {
						t.Fatal("delete after unknown effect")
					}
				} else if got.Err != nil || !sess.hit || deleted != 1 {
					t.Fatal("continuation did not complete")
				}
			})
		}
	}
}

type bootFailFinalizer struct{ *appBootstrapEffects }

func (f *bootFailFinalizer) FinalizeBootstrapResult(bootstrapServiceRequest, bootstrapServiceResult) bootstrapServiceResult {
	panic("before ownership handoff")
}
func TestBootstrapFlowActualFinalizerFailure(t *testing.T) {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("actual native required")
	}
	q := bootstrapServiceRequest{Op: "lock"}
	e, closed, held := bootOwnedLockFX(q)
	r, used := runRustBootstrapService(q, &bootFailFinalizer{e})
	e.Release()
	if !used || r.Err == nil || *held || *closed != 1 {
		t.Fatalf("finalizer lost resource %+v held%v closed%d", r, *held, *closed)
	}
}

type bootBroadError struct{ label string }

func (e *bootBroadError) Error() string { return e.label }
func (e *bootBroadError) Is(error) bool { return true }
func TestBootstrapFlowErrorTokenIdentity(t *testing.T) {
	a := newBootstrapArena(bootstrapServiceRequest{})
	x, y := &bootBroadError{"same"}, &bootBroadError{"same"}
	a.errs[1] = x
	f := bootstrapFrame{errorCode: 1, token: 1}
	if a.resultMatches(f, bootstrapServiceResult{Err: y}) {
		t.Fatal("broad Is accepted different error resource")
	}
	if !a.resultMatches(f, bootstrapServiceResult{Err: x}) {
		t.Fatal("same resource rejected")
	}
}
