package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func nodeQuerySafeFX(a *app, q nodeQueryServiceRequest) *appNodeQueryEffects {
	e := newAppNodeQueryEffects(a, q, nil)
	e.hooks = map[string]func(nodeQueryEffect) nodeQueryReply{"running": func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{Bool: true} }, "group": func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{Text: "a"} }, "read-profile": func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{Err: errors.New("synthetic absent")} }, "provider": func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{} }}
	return e
}
func TestNodeQueryServiceBorrowedWait(t *testing.T) {
	a := &app{}
	a.mu.Lock()
	defer a.mu.Unlock()
	q := nodeQueryServiceRequest{Op: "wait", Borrowed: true, Timeout: 50 * time.Millisecond}
	e := nodeQuerySafeFX(a, q)
	now := time.Unix(100, 0)
	e.now = func() time.Time { return now }
	sleeps := 0
	e.sleep = func(d time.Duration) {
		if d != 50*time.Millisecond {
			t.Fatal(d)
		}
		sleeps++
		now = now.Add(60 * time.Millisecond)
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("borrowed lock released")
		}
	}
	r := runGoNodeQueryService(q, e)
	if r.Err == nil || sleeps != 1 {
		t.Fatal(r, sleeps)
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed released")
	}
}
func TestNodeQueryServiceNoWaitForZeroTimeout(t *testing.T) {
	a := &app{}
	q := nodeQueryServiceRequest{Op: "wait"}
	e := nodeQuerySafeFX(a, q)
	e.now = func() time.Time { return time.Unix(1, 0) }
	e.hooks["group"] = func(nodeQueryEffect) nodeQueryReply { t.Fatal("list at zero timeout"); return nodeQueryReply{} }
	if r := runGoNodeQueryService(q, e); r.Err == nil {
		t.Fatal("zero timeout succeeded")
	}
}
func TestNodeQueryServiceProviderPrecedence(t *testing.T) {
	a := &app{}
	q := nodeQueryServiceRequest{Op: "list"}
	e := nodeQuerySafeFX(a, q)
	e.hooks["metadata"] = func(nodeQueryEffect) nodeQueryReply {
		return nodeQueryReply{Metadata: &nodeQueryMetadata{SubscriptionURL: "synthetic", Sources: []nodeQuerySource{{URL: "synthetic", Label: "source"}}, Manual: []proxyNode{{Name: "a", Network: "manual-net", Manual: true}}}}
	}
	e.hooks["read-profile"] = func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{} }
	e.hooks["parse-profile"] = func(nodeQueryEffect) nodeQueryReply {
		return nodeQueryReply{Nodes: []proxyNode{{Name: "a", Network: "old"}, {Name: "b", TLS: true}}}
	}
	e.hooks["provider"] = func(x nodeQueryEffect) nodeQueryReply {
		if x.Name == "SmartVPNSubscription" {
			return nodeQueryReply{Provider: []nodeQueryProvider{{Valid: true, Name: "a", Type: "first", Network: "provider", Alive: true}, {Valid: true, Name: "b"}, {Valid: true, Name: "a", Type: "duplicate"}}}
		}
		return nodeQueryReply{Provider: []nodeQueryProvider{{Valid: true, Name: "a", Type: "manual-provider"}, {Valid: true, Name: "c"}}}
	}
	r := runGoNodeQueryService(q, e)
	if r.Err != nil || len(r.Nodes) != 3 || r.Nodes[0].Type != "first" || r.Nodes[0].Network != "manual-net" || !r.Nodes[0].Manual || !r.Nodes[0].Selected || r.Nodes[1].SourceLabel != "source" || !r.Nodes[1].TLS || r.Nodes[2].Name != "c" {
		t.Fatalf("%+v", r)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}

type nodeQueryCountingBody struct {
	io.Reader
	closes int
}

func (b *nodeQueryCountingBody) Close() error { b.closes++; return nil }
func TestNodeQueryServiceDelayCodecAndClose(t *testing.T) {
	for _, status := range []int{200, 503} {
		a := &app{ctrlPort: 1234, ctrlSecret: "synthetic"}
		q := nodeQueryServiceRequest{Op: "delay", Target: "https://example.invalid/?a=1&b=2", Timeout: 8 * time.Second}
		e := newAppNodeQueryEffects(a, q, nil)
		body := &nodeQueryCountingBody{Reader: strings.NewReader(`{"a":23}`)}
		e.do = func(req *http.Request, d time.Duration) (*http.Response, error) {
			if req.URL.Query().Get("url") != q.Target || req.URL.Query().Get("timeout") != "8000" || req.Header.Get("Authorization") != "Bearer synthetic" || d != 20*time.Second {
				t.Fatal("request changed")
			}
			if !a.mu.TryLock() {
				t.Fatal("unlocked delay held lock")
			}
			a.mu.Unlock()
			return &http.Response{StatusCode: status, Body: body}, nil
		}
		r := runGoNodeQueryService(q, e)
		e.Release()
		if body.closes != 1 {
			t.Fatal(body.closes)
		}
		if status == 200 && (r.Err != nil || r.Delays["a"] != 23) {
			t.Fatal(r)
		}
		if status != 200 && r.Err == nil {
			t.Fatal("status accepted")
		}
	}
}
func TestNodeQueryServiceLatencyLockDifference(t *testing.T) {
	for _, op := range []string{"latency", "node-latency"} {
		a := &app{}
		testEditHealth(a, func(p *healthDomainProjection) { p.Params.ConnectTimeoutMs = 3000 })
		q := nodeQueryServiceRequest{Op: op}
		e := nodeQuerySafeFX(a, q)
		e.hooks["delay-request"] = func(x nodeQueryEffect) nodeQueryReply {
			got := a.mu.TryLock()
			if got {
				a.mu.Unlock()
			}
			if got != (op == "latency") {
				t.Fatal("lock scope", op)
			}
			return nodeQueryReply{Status: 200}
		}
		e.hooks["delay-decode"] = func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{Delays: map[string]int{"a": 8}} }
		e.hooks["body-close"] = func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{} }
		e.hooks["response"] = func(x nodeQueryEffect) nodeQueryReply {
			if x.Result.Status != 0 {
				t.Fatal(x.Result)
			}
			return nodeQueryReply{}
		}
		r := runGoNodeQueryService(q, e)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if !a.mu.TryLock() {
			t.Fatal("lock leak")
		}
		a.mu.Unlock()
	}
}

func TestNodeQueryServiceMetadataSnapshotIsolation(t *testing.T) {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	testCatalogEdit(a, func(entries *[]subscriptionEntry) {
		(*entries) = []subscriptionEntry{{URL: "synthetic", Label: "before"}}
	})
	a.manualNodes = []manualNode{{Name: "manual", Proxy: map[string]any{"network": "before"}}}
	q := nodeQueryServiceRequest{Op: "list"}
	e := newAppNodeQueryEffects(a, q, nil)
	e.controller = func(route string) (map[string]any, error) {
		if !a.mu.TryLock() {
			t.Fatal("controller held snapshot lock")
		}
		a.mu.Unlock()
		if route == "/proxies/SmartVPN" {
			return map[string]any{"now": "profile"}, nil
		}
		return map[string]any{"proxies": []any{map[string]any{"name": "profile"}, map[string]any{"name": "manual"}}}, nil
	}
	e.readFile = func(string) ([]byte, error) {
		a.mu.Lock()
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "after" })
		a.manualNodes[0].Proxy["network"] = "after"
		a.mu.Unlock()
		return []byte("synthetic"), nil
	}
	e.parse = func([]byte) []proxyNode { return []proxyNode{{Name: "profile"}} }
	r := runGoNodeQueryService(q, e)
	if r.Err != nil || len(r.Nodes) != 2 || r.Nodes[0].SourceLabel != "before" || r.Nodes[1].Network != "before" {
		t.Fatalf("snapshot aliased: %+v", r)
	}
}

type nodeQueryRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f nodeQueryRoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the real older adapters and their default method-value bindings.
// Only the transport is fake; no list/delay method is replaced.
func TestNodeQueryServiceNestedBorrowedBindings(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	for _, kind := range []string{"region-borrowed", "region-owned", "diagnose-borrowed", "diagnose-owned"} {
		t.Run(kind, func(t *testing.T) {
			a := &app{home: t.TempDir(), ctrlPort: 1234, ctrlSecret: "synthetic"}
			calls := 0
			http.DefaultTransport = nodeQueryRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("nested operation released caller lock")
				}
				body := `{"proxies":[{"name":"n","type":"ss"}]}`
				if strings.HasPrefix(r.URL.Path, "/group/") {
					body = `{"n":7}`
				} else if r.URL.Path == "/proxies/SmartVPN" {
					body = `{"now":"n"}`
				}
				return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			borrowed := strings.HasSuffix(kind, "borrowed")
			if borrowed {
				a.mu.Lock()
			}
			if strings.HasPrefix(kind, "region-") {
				e := newAppRegionJobEffects(a, borrowed)
				if !borrowed {
					e.Call(regionJobEffect{Kind: "lock"})
				}
				list := e.Call(regionJobEffect{Kind: "list"})
				delays := e.Call(regionJobEffect{Kind: "delays"})
				if list.Err != nil || len(list.Names) != 1 || list.Names[0] != "n" || delays.Err != nil || delays.Delays["n"] != 7 {
					t.Fatal(list, delays)
				}
				e.Release()
			} else {
				op := "diagnose"
				if borrowed {
					op = "ensure"
				}
				e := newAppDiagnoseEffects(a, diagnoseServiceRequest{Op: op}, nil)
				if !borrowed {
					e.Call(diagnoseEffect{Kind: "lock"})
				}
				list := e.Call(diagnoseEffect{Kind: "list"})
				if list.Err != nil || len(list.Names) != 1 || list.Names[0] != "n" {
					t.Fatal(list)
				}
				e.Release()
			}
			if calls < 3 {
				t.Fatal("did not reach default adapter transport", calls)
			}
			if borrowed {
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("borrowed release")
				}
				a.mu.Unlock()
			} else {
				if !a.mu.TryLock() {
					t.Fatal("owned release failed")
				}
				a.mu.Unlock()
			}
		})
	}
}

func TestNodeQueryServiceInterruptedResponseOnce(t *testing.T) {
	q := nodeQueryServiceRequest{Op: "nodes"}
	w := httptest.NewRecorder()
	e := newAppNodeQueryEffects(&app{}, q, w)
	e.complete(nodeQueryServiceResult{Err: errors.New("synthetic sensitive detail")})
	if w.Code != 500 || strings.Contains(w.Body.String(), "sensitive") {
		t.Fatal(w.Code, w.Body.String())
	}
	before := w.Body.String()
	e.complete(nodeQueryServiceResult{Err: errors.New("second")})
	if w.Body.String() != before {
		t.Fatal("repeated response")
	}
	w = httptest.NewRecorder()
	e = newAppNodeQueryEffects(&app{}, q, w)
	e.Call(nodeQueryEffect{Kind: "response", Name: "nodes", Result: nodeQueryServiceResult{Nodes: []proxyNode{}}})
	before = w.Body.String()
	e.complete(nodeQueryServiceResult{Err: errors.New("after response")})
	if w.Code != 200 || w.Body.String() != before {
		t.Fatal("overwrote successful response")
	}
}

type nodeQueryPanicBody struct {
	io.Reader
	closed int
}

func (b *nodeQueryPanicBody) Close() error { b.closed++; panic("synthetic close panic") }
func TestNodeQueryServiceFacadePanicBeforeResponse(t *testing.T) {
	a := &app{}
	q := nodeQueryServiceRequest{Op: "delay", Borrowed: true, Target: "https://example.invalid", Timeout: time.Second}
	w := httptest.NewRecorder()
	e := newAppNodeQueryEffects(a, q, w)
	body := &nodeQueryPanicBody{Reader: strings.NewReader(`{"a":1}`)}
	e.do = func(*http.Request, time.Duration) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	runNodeQueryServiceWithEffects(q, e, func(q nodeQueryServiceRequest, fx nodeQueryServiceEffects) (r nodeQueryServiceResult) {
		defer func() {
			if recover() != nil {
				r = nodeQueryServiceResult{Err: errors.New("interrupted")}
			}
		}()
		return runGoNodeQueryService(q, fx)
	})
	if w.Code != 500 || body.closed != 1 {
		t.Fatal(w.Code, body.closed)
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed mutex released")
	}
}
