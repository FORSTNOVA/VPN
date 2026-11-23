//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

type speedFaultSession struct {
	speedFlowSession
	target        byte
	afterNext     bool
	armed, failed bool
}

func (s *speedFaultSession) next() ([]byte, uint64, error) {
	if s.armed && !s.failed {
		s.failed = true
		return nil, 0, errors.New("injected next")
	}
	return s.speedFlowSession.next()
}
func (s *speedFaultSession) advance(step uint64, b []byte) error {
	e := s.speedFlowSession.advance(step, b)
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
func speedFaultInput(k byte) (speedServiceRequest, speedIndependentSpec) {
	q := speedServiceRequest{Op: "handler", MegaBytes: 31, Allowance: 10, Budget: time.Second, Remaining: 20, Length: 7}
	s := speedIndependentSpec{Temporary: true, Node: "node", Down: 100, Up: 50, Started: 1, Elapsed: time.Second, Reads: []speedServiceReply{{Bytes: 4}, {Bytes: 20, Err: io.EOF}}}
	switch k {
	case 11, 12, 13, 14, 15, 16:
		q.Op = "down-stream"
	case 17:
		q.Op = "up-stream"
	case 21, 22, 23:
		q.Op = "zero-read"
	case 25:
		q.Op = "download"
		s.Down = 0
	}
	return q, s
}
func TestSpeedFlowActualBoundaryFaults(t *testing.T) {
	if !rustSpeedFlowAvailable() {
		t.Fatal("missing native")
	}
	for k := byte(1); k <= 25; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q, s := speedFaultInput(k)
				var injected *speedFaultSession
				speedIndependentParity(t, q, s, func(t *testing.T, q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
					r, used := runRustSpeedServiceWithStart(q, fx, func(b []byte) (speedFlowSession, error) {
						n, e := startRustSpeedFlow(b)
						if e != nil {
							return nil, e
						}
						injected = &speedFaultSession{speedFlowSession: n, target: k, afterNext: next}
						return injected, nil
					})
					if !used || injected == nil || !injected.failed {
						t.Fatal("fault not exercised", used)
					}
					return r
				})
			})
		}
	}
}

type speedPanicFX struct {
	*speedIndependentFX
	kind string
}

func (f *speedPanicFX) Call(e speedServiceEffect) speedServiceReply {
	r := f.speedIndependentFX.Call(e)
	if e.Kind == f.kind {
		panic("after effect")
	}
	return r
}
func TestSpeedFlowActualPanicNoReplay(t *testing.T) {
	for _, k := range []byte{1, 2, 5, 6, 7, 10, 12, 13, 14, 16, 17, 18, 19, 20, 21, 22, 23} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			q, s := speedFaultInput(k)
			base := &speedIndependentFX{s: s}
			fx := &speedPanicFX{base, speedPortKinds[k]}
			r, used := runRustSpeedService(q, fx)
			if !used || r.Err == nil || base.owned {
				t.Fatal("panic success or lock leak", used, r)
			}
			n := 0
			for _, e := range base.trace {
				if e.Kind == fx.kind {
					n++
				}
			}
			if n != 1 {
				t.Fatal("replayed", n)
			}
		})
	}
}
func TestSpeedFlowActualCapacityCancel(t *testing.T) {
	b, _ := speedStartInput(speedServiceRequest{Op: "download"})
	var all []speedFlowSession
	defer func() {
		for _, s := range all {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustSpeedFlow(b)
		if e != nil {
			t.Fatal(i, e)
		}
		all = append(all, s)
	}
	fx := &speedIndependentFX{}
	_, used := runRustSpeedService(speedServiceRequest{Op: "download"}, fx)
	if used || len(fx.trace) != 0 {
		t.Fatal("capacity executed effect")
	}
	for _, s := range all {
		s.close()
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newSpeedMirror(speedServiceRequest{Op: "download"})
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leak")
	}
}

type speedLongFX struct {
	reads, closed, releases int
	total                   int64
}

func (f *speedLongFX) Call(e speedServiceEffect) speedServiceReply {
	switch e.Kind {
	case "request-new", "request-do":
	case "body-read":
		f.reads++
		if f.reads > 40000 {
			return speedServiceReply{Err: io.EOF}
		}
		return speedServiceReply{Bytes: 1}
	case "download-count":
		f.total += e.Bytes
	case "total-load":
		return speedServiceReply{Bytes: f.total}
	case "body-close":
		f.closed++
	default:
		panic(e.Kind)
	}
	return speedServiceReply{}
}
func (f *speedLongFX) Release() { f.releases++ }
func TestSpeedFlowActualLongStream(t *testing.T) {
	f := &speedLongFX{}
	before := speedFlowRecoveries.Load()
	r, ok := runRustSpeedService(speedServiceRequest{Op: "down-stream", Allowance: 50000}, f)
	if !ok || r.Err != nil || speedFlowRecoveries.Load() != before || f.total != 40000 || f.reads != 40001 || f.closed != 1 || f.releases != 1 {
		t.Fatal("long stream incomplete", ok, r, f)
	}
}

func TestSpeedFlowActualConcurrentBatchFaults(t *testing.T) {
	for _, op := range []string{"download", "upload"} {
		for _, kind := range []byte{6, 7, 10} {
			for _, next := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/%v", op, kind, next), func(t *testing.T) {
					var arrived, closed atomic.Int64
					barrier := make(chan struct{})
					count := int64(4)
					if op == "upload" {
						count = 2
					}
					rt, cancel := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
						if arrived.Add(1) == count {
							close(barrier)
						}
						<-barrier
						if op == "upload" {
							b, e := io.ReadAll(r.Body)
							if e != nil || len(b) != 5 {
								t.Errorf("upload %d %v", len(b), e)
							}
						}
						return &http.Response{StatusCode: 503, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func([]byte) (int, error) {
							if op == "upload" {
								return 0, io.EOF
							}
							return 3, io.EOF
						}}}, nil
					})
					var fault *speedFaultSession
					var children atomic.Int64
					rt.run = func(q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
						if q.Op == op {
							r, ok := runRustSpeedServiceWithStart(q, fx, func(b []byte) (speedFlowSession, error) {
								ss, e := startRustSpeedFlow(b)
								if e != nil {
									return nil, e
								}
								fault = &speedFaultSession{speedFlowSession: ss, target: kind, afterNext: next}
								return fault, nil
							})
							if !ok {
								panic("native parent unavailable")
							}
							return r
						}
						children.Add(1)
						r, ok := runRustSpeedService(q, fx)
						if !ok {
							panic("native child unavailable")
						}
						return r
					}
					q := speedServiceRequest{Op: op, Allowance: 11, Budget: speedTestSeconds}
					e := newSpeedEffects(nil, q, nil, rt)
					r := runSpeedServiceWithEffects(q, e)
					want := int64(12)
					if op == "upload" {
						want = 10
					}
					if r.Err != nil || r.Bytes != want || fault == nil || !fault.failed || arrived.Load() != count || closed.Load() != count || cancel.Load() != 1 || children.Load() < count {
						t.Fatalf("result%+v arrived%d close%d cancel%d child%d", r, arrived.Load(), closed.Load(), cancel.Load(), children.Load())
					}
					e.Release()
					if closed.Load() != count || cancel.Load() != 1 {
						t.Fatal("cleanup replay")
					}
				})
			}
		}
	}
}
func TestSpeedFlowActualResponsePanic(t *testing.T) {
	t.Setenv("SMARTVPN_RUST_SPEED_FLOW", "1")
	if !rustSpeedFlowAvailable() {
		t.Fatal("native unavailable")
	}
	before := speedFlowRecoveries.Load()
	TestSpeedServiceSafeResponsePanicCleanup(t)
	if speedFlowRecoveries.Load() != before {
		t.Fatal("writer panic recovered/replayed")
	}
}

func TestSpeedFlowActualBatchCapacityFallback(t *testing.T) {
	input, _ := speedStartInput(speedServiceRequest{Op: "download"})
	var held []speedFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 63; i++ {
		s, e := startRustSpeedFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, s)
	}
	var arrived, closed, fallback atomic.Int64
	barrier := make(chan struct{})
	rt, cancel := speedTestRuntime(t, func(*http.Request) (*http.Response, error) {
		if arrived.Add(1) == 4 {
			close(barrier)
		}
		<-barrier
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func([]byte) (int, error) { return 3, io.EOF }}}, nil
	})
	rt.run = func(q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
		r, ok := runRustSpeedService(q, fx)
		if ok {
			return r
		}
		if q.Op == "download" {
			panic("parent lacked reserved session")
		}
		fallback.Add(1)
		return runGoSpeedService(q, fx)
	}
	q := speedServiceRequest{Op: "download", Allowance: 1, Budget: speedTestSeconds}
	r := runSpeedServiceWithEffects(q, newSpeedEffects(nil, q, nil, rt))
	if r.Err != nil || r.Bytes != 12 || arrived.Load() != 4 || closed.Load() != 4 || cancel.Load() != 1 || fallback.Load() != 4 {
		t.Fatal("unsafe capacity fallback", r, arrived.Load(), closed.Load(), cancel.Load(), fallback.Load())
	}
}

func TestSpeedFlowActualStartedBatchPanicJoins(t *testing.T) {
	var closed, calls atomic.Int64
	rt, cancels := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func([]byte) (int, error) { <-r.Context().Done(); return 0, r.Context().Err() }}}, nil
	})
	rt.run = func(q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
		if q.Op == "download" {
			fx = speedAfterEffectPanic{fx, "batch-start"}
		}
		r, ok := runRustSpeedService(q, fx)
		if !ok {
			panic("native unavailable")
		}
		return r
	}
	q := speedServiceRequest{Op: "download", Allowance: 10, Budget: speedTestSeconds}
	e := newSpeedEffects(nil, q, nil, rt)
	r := runSpeedServiceWithEffects(q, e)
	if r.Err == nil || cancels.Load() != 1 || !e.batch.joined || calls.Load() != 4 || closed.Load() != 4 {
		t.Fatalf("result%+v cancel%d join%v calls%d closes%d", r, cancels.Load(), e.batch.joined, calls.Load(), closed.Load())
	}
	e.Release()
	if cancels.Load() != 1 || closed.Load() != 4 {
		t.Fatal("resource replay")
	}
}
