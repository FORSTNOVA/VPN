//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

type nodeQueryInjectedSession struct {
	nodeQueryFlowSession
	kind, target                 byte
	nextFailure, pending, failed bool
}

func (s *nodeQueryInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next")
	}
	b, n, e := s.nodeQueryFlowSession.next()
	if e == nil {
		f, er := decodeNodeQueryFrame(b)
		if er == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *nodeQueryInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance")
		}
	}
	return s.nodeQueryFlowSession.advance(n, b)
}
func queryFaultOp(k byte) string {
	switch k {
	case 3, 16:
		return "nodes"
	case 10, 11, 12:
		return "delay"
	case 13, 14, 15:
		return "wait"
	case 17:
		return "latency"
	}
	return "list"
}
func TestNodeQueryFlowActualBoundaryFaults(t *testing.T) {
	for k := byte(1); k <= 17; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q := queryDiffRequest(queryFaultOp(k))
				s := queryDiffBase()
				if k == 15 {
					s.WaitEmpty = true
				}
				var inj *nodeQueryInjectedSession
				queryDiffCompare(t, q, s, func(q nodeQueryServiceRequest, fx nodeQueryServiceEffects) nodeQueryServiceResult {
					r, used := runRustNodeQueryServiceWithStart(q, fx, func(b []byte) (nodeQueryFlowSession, error) {
						ss, e := startRustNodeQueryFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &nodeQueryInjectedSession{nodeQueryFlowSession: ss, target: k, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("boundary not reached", nodeQueryFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type queryPanicFX struct {
	*queryDiffFX
	port string
}

func (f *queryPanicFX) Call(q nodeQueryEffect) nodeQueryReply {
	r := f.queryDiffFX.Call(q)
	if q.Kind == f.port {
		panic("after actual fake port")
	}
	return r
}
func TestNodeQueryFlowActualPanicCleanup(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		for _, port := range []string{"endpoint", "metadata", "group", "read-profile", "parse-profile", "provider", "delay-request", "delay-decode", "body-close", "deadline", "before", "sleep"} {
			t.Run(fmt.Sprintf("%s/%v", port, borrowed), func(t *testing.T) {
				op := "list"
				switch port {
				case "delay-request", "delay-decode", "body-close":
					op = "delay"
				case "deadline", "before", "sleep":
					op = "wait"
				}
				q := queryDiffRequest(op)
				q.Borrowed = borrowed
				s := queryDiffBase()
				s.WaitEmpty = port == "sleep"
				f := queryDiffNew(q, s)
				if borrowed {
					f.a.mu.Lock()
				}
				r, used := runRustNodeQueryService(q, &queryPanicFX{f, port})
				if !used || r.Err == nil {
					t.Fatal("panic false success")
				}
				if borrowed {
					if f.a.mu.TryLock() {
						f.a.mu.Unlock()
						t.Fatal("borrowed mutex released")
					}
					f.a.mu.Unlock()
				}
				if !f.a.mu.TryLock() {
					t.Fatal("owned mutex leaked")
				}
				f.a.mu.Unlock()
				if f.requests > 1 || f.closed != f.requests {
					t.Fatal("HTTP replay/body leak", f.requests, f.closed)
				}
				count := 0
				for _, e := range f.trace {
					if e.Effect.Kind == port {
						count++
					}
				}
				if count != 1 {
					t.Fatal("effect replayed", count)
				}
			})
		}
	}
}
func TestNodeQueryFlowActualResponsePanic(t *testing.T) {
	for _, op := range []string{"nodes", "latency", "node-latency"} {
		t.Run(op, func(t *testing.T) {
			f := queryDiffNew(queryDiffRequest(op), queryDiffBase())
			r, used := runRustNodeQueryService(queryDiffRequest(op), &queryPanicFX{f, "response"})
			if !used || r.Err == nil {
				t.Fatal("response panic false success")
			}
			if !f.a.mu.TryLock() {
				t.Fatal("response mutex leaked")
			}
			f.a.mu.Unlock()
			if f.closed != f.requests || f.requests > 1 {
				t.Fatal("body leak/replay")
			}
		})
	}
}
func TestNodeQueryFlowActualCapacityAndCancel(t *testing.T) {
	q := queryDiffRequest("list")
	input, _ := nodeQueryStartInput(q)
	var sessions []nodeQueryFlowSession
	for i := 0; i < 64; i++ {
		s, e := startRustNodeQueryFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, s)
	}
	f := queryDiffNew(q, queryDiffBase())
	_, used := runRustNodeQueryService(q, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity fallback effects")
	}
	for _, s := range sessions {
		s.close()
	}
	s, e := startRustNodeQueryFlow(input)
	if e != nil {
		t.Fatal(e)
	}
	s.close()
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newNodeQueryMirror(q)
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leak")
	}
}

type queryLeanFX struct{ f *queryDiffFX }

func (e queryLeanFX) Call(q nodeQueryEffect) nodeQueryReply {
	r := e.f.e.Call(q)
	e.f.io = nil
	return r
}
func (e queryLeanFX) Release() { e.f.Release() }

type queryOpaqueSession struct {
	nodeQueryFlowSession
	t        *testing.T
	maxToken int64
}

func (s *queryOpaqueSession) advance(n uint64, b []byte) error {
	if bytes.Contains(b, []byte("synthetic-token")) || bytes.Contains(b, []byte("secret-path")) {
		s.t.Fatal("secret entered native")
	}
	return s.nodeQueryFlowSession.advance(n, b)
}
func TestNodeQueryFlowActualLongPollBoundedArena(t *testing.T) {
	q := queryDiffRequest("wait")
	q.Timeout = 15000 * 50 * time.Millisecond
	sp := queryDiffBase()
	sp.WaitEmpty = true
	sp.ClockStep = 0
	f := queryDiffNew(q, sp)
	before := nodeQueryFlowRecoveries.Load()
	r, used := runRustNodeQueryServiceWithStart(q, queryLeanFX{f}, func(b []byte) (nodeQueryFlowSession, error) {
		s, e := startRustNodeQueryFlow(b)
		if e != nil {
			return nil, e
		}
		return &queryOpaqueSession{s, t, 0}, nil
	})
	if !used || r.Err == nil || r.Err.Error() != "the kernel reported no usable nodes" || nodeQueryFlowRecoveries.Load() != before {
		t.Fatal("long poll not native", r.Err, nodeQueryFlowRecoveryReason.Load())
	}
	if f.sleeps != 15000 {
		t.Fatal("poll truncated", f.sleeps)
	}
	a := newNodeQueryArena(q)
	for i := 0; i < 15000; i++ {
		a.replyBytes(nodeQueryFrame{kind: 5}, nodeQueryReply{Metadata: &nodeQueryMetadata{Cached: []proxyNode{{Name: "cached"}}, Manual: []proxyNode{{Name: "manual"}}}})
		a.replyBytes(nodeQueryFrame{kind: 8}, nodeQueryReply{Nodes: []proxyNode{{Name: fmt.Sprint(i)}}})
		a.replyBytes(nodeQueryFrame{kind: 9, name: "SmartVPNSubscription"}, nodeQueryReply{Provider: []nodeQueryProvider{{Name: fmt.Sprint(i)}}})
		a.replyBytes(nodeQueryFrame{kind: 6}, nodeQueryReply{Err: errors.New("failed poll")})
	}
	if len(a.cached) != 1 || len(a.manual) != 1 || len(a.profile) != 1 || len(a.providers[0]) != 1 || a.lastErr == nil {
		t.Fatal("arena retained past polls")
	}
}

func TestNodeQueryFlowActualOpaqueTarget(t *testing.T) {
	q := queryDiffRequest("delay")
	q.Target = "https://user:synthetic-token@synthetic.invalid/secret-path"
	f := queryDiffNew(q, queryDiffBase())
	before := nodeQueryFlowRecoveries.Load()
	r, used := runRustNodeQueryServiceWithStart(q, f, func(b []byte) (nodeQueryFlowSession, error) {
		if bytes.Contains(b, []byte("synthetic-token")) || bytes.Contains(b, []byte("secret-path")) {
			t.Fatal("target crossed native")
		}
		s, e := startRustNodeQueryFlow(b)
		if e != nil {
			return nil, e
		}
		return &queryOpaqueSession{s, t, 0}, nil
	})
	if !used || r.Err != nil || nodeQueryFlowRecoveries.Load() != before || f.closed != 1 || f.requests != 1 {
		t.Fatal("opaque delay failed", r, nodeQueryFlowRecoveryReason.Load())
	}
}

func TestNodeQueryFlowActualFailureBranchFaults(t *testing.T) {
	for _, c := range []struct {
		op   string
		kind byte
		fail string
	}{{"nodes", 6, "group"}, {"delay", 10, "status"}, {"delay", 12, "status"}, {"delay", 11, "decode"}, {"delay", 12, "decode"}, {"delay", 10, "request"}} {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%d/%s/%v", c.op, c.kind, c.fail, next), func(t *testing.T) {
				sp := queryDiffBase()
				switch c.fail {
				case "group":
					sp.GroupFail = true
				case "status":
					sp.Status = 503
				case "decode":
					sp.Body = "not JSON"
				case "request":
					sp.RequestFail = true
				}
				var inj *nodeQueryInjectedSession
				queryDiffCompare(t, queryDiffRequest(c.op), sp, func(q nodeQueryServiceRequest, fx nodeQueryServiceEffects) nodeQueryServiceResult {
					r, used := runRustNodeQueryServiceWithStart(q, fx, func(b []byte) (nodeQueryFlowSession, error) {
						s, e := startRustNodeQueryFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &nodeQueryInjectedSession{nodeQueryFlowSession: s, target: c.kind, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("failure boundary not reached")
				}
			})
		}
	}
}

type queryPanicCloseBody struct{ io.ReadCloser }

func (b queryPanicCloseBody) Close() error { _ = b.ReadCloser.Close(); panic("body close callback") }
func TestNodeQueryFlowActualCloseCallbackPanic(t *testing.T) {
	for _, op := range []string{"delay", "node-latency"} {
		t.Run(op, func(t *testing.T) {
			q := queryDiffRequest(op)
			f := queryDiffNew(q, queryDiffBase())
			old := f.e.do
			f.e.do = func(r *http.Request, d time.Duration) (*http.Response, error) {
				v, e := old(r, d)
				if e == nil {
					v.Body = queryPanicCloseBody{v.Body}
				}
				return v, e
			}
			r, used := runRustNodeQueryService(q, f)
			if !used || r.Err == nil || f.closed != 1 || f.requests != 1 {
				t.Fatal("close panic replay/false success", r, f.closed, f.requests)
			}
			f.Release()
			if f.closed != 1 {
				t.Fatal("release reclosed body")
			}
			if !f.a.mu.TryLock() {
				t.Fatal("close panic retained mutex")
			}
			f.a.mu.Unlock()
		})
	}
}

type queryWritePanic struct {
	r      *httptest.ResponseRecorder
	writes int
}

func (w *queryWritePanic) Header() http.Header { return w.r.Header() }
func (w *queryWritePanic) WriteHeader(n int)   { w.r.WriteHeader(n) }
func (w *queryWritePanic) Write(b []byte) (int, error) {
	w.writes++
	n, e := w.r.Write(b)
	if e == nil {
		panic("after HTTP response write")
	}
	return n, e
}
func TestNodeQueryFlowActualFacadeInterruptedResponse(t *testing.T) {
	for _, afterResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(afterResponse), func(t *testing.T) {
			q := queryDiffRequest("node-latency")
			f := queryDiffNew(q, queryDiffBase())
			var writer *queryWritePanic
			if afterResponse {
				writer = &queryWritePanic{r: f.w}
				f.e.w = writer
			} else {
				old := f.e.do
				f.e.do = func(r *http.Request, d time.Duration) (*http.Response, error) {
					v, e := old(r, d)
					if e == nil {
						v.Body = queryPanicCloseBody{v.Body}
					}
					return v, e
				}
			}
			r := runNodeQueryServiceWithEffects(q, f.e, func(q nodeQueryServiceRequest, fx nodeQueryServiceEffects) nodeQueryServiceResult {
				r, used := runRustNodeQueryService(q, fx)
				if !used {
					t.Fatal("actual DLL not used")
				}
				return r
			})
			if r.Err == nil || !f.e.responded || f.requests != 1 || f.closed != 1 {
				t.Fatal("facade false success/resource leak", r, f.closed)
			}
			if afterResponse {
				if writer.writes != 1 || f.w.Code != 200 {
					t.Fatal("rewrote committed response")
				}
			} else {
				if f.w.Code != 500 || bytes.Contains(f.w.Body.Bytes(), []byte("body close callback")) {
					t.Fatal("missing safe500 or exposed internal error", f.w.Code, f.w.Body.String())
				}
			}
			if !f.a.mu.TryLock() {
				t.Fatal("facade mutex leak")
			}
			f.a.mu.Unlock()
		})
	}
}
