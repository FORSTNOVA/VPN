package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type queryDiffSpec struct {
	Running, ProxyEnabled, ReadFail, GroupFail, SubFail, ManualFail, RequestFail, Edit bool
	Selected, Nested, AfterDelay, Body                                                 string
	Status                                                                             int
	Parsed, Cached                                                                     []proxyNode
	Providers                                                                          map[string]map[string]any
	Manual                                                                             []manualNode
	ClockStep                                                                          time.Duration
	WaitEmpty                                                                          bool
}
type queryDiffFrame struct {
	Effect                 nodeQueryEffect
	Locked                 bool
	Selected, Subscription string
}
type queryDiffBody struct {
	io.Reader
	close func()
}

func (b queryDiffBody) Close() error { b.close(); return nil }

type queryDiffFX struct {
	a                        *app
	e                        *appNodeQueryEffects
	w                        *httptest.ResponseRecorder
	trace                    []queryDiffFrame
	io                       []string
	closed, sleeps, requests int
	clock                    time.Time
	selected                 string
	timeout                  time.Duration
	requestURL               string
}

var queryDiffNow = time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)

func queryDiffBase() queryDiffSpec {
	return queryDiffSpec{Running: true, ProxyEnabled: true, Selected: "a", Nested: "b", Body: `{"a":10,"b":20}`, Status: 200, ClockStep: time.Millisecond, Parsed: []proxyNode{{Name: "a", Network: "profile", TLS: false}, {Name: "b", Network: "ws", TLS: true}}, Cached: []proxyNode{{Name: "a", Type: "cached"}, {Name: "cached", Type: "ss"}}, Manual: []manualNode{{Name: "a", Type: "vless", Proxy: map[string]any{"network": "manual", "tls": true, "ws-opts": map[string]any{"path": "/x", "headers": map[string]any{}}}}}, Providers: map[string]map[string]any{"SmartVPNSubscription": {"proxies": []any{map[string]any{"name": "a", "type": "kernel-a", "network": "raw", "alive": true}, map[string]any{"name": "b", "type": "kernel-b", "network": "raw", "alive": false}}}, manualProviderName: {"proxies": []any{map[string]any{"name": "a", "type": "later", "alive": false}, map[string]any{"name": "manual-only", "type": "ss", "network": "tcp", "alive": true}}}}}
}
func queryDiffRequest(op string) nodeQueryServiceRequest {
	return nodeQueryServiceRequest{Op: op, Timeout: 120 * time.Millisecond, Target: "https://synthetic.invalid/check?x=1"}
}
func queryDiffNew(q nodeQueryServiceRequest, s queryDiffSpec) *queryDiffFX {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/sub" })
	testEditSettings(a, func(v *settings) { v.SelectedNode = s.Selected })
	a.proxyOn = s.ProxyEnabled
	a.ctrlPort = 12345
	a.ctrlSecret = "synthetic-token"
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.ConnectTimeoutMs = 4321 })
	a.cachedNodes = append([]proxyNode(nil), s.Cached...)
	a.manualNodes = append([]manualNode(nil), s.Manual...)
	testCatalogEdit(a, func(entries *[]subscriptionEntry) {
		(*entries) = []subscriptionEntry{{URL: a.settingsLocked().SubscriptionURL, Label: "original-label"}}
	})
	w := httptest.NewRecorder()
	e := newAppNodeQueryEffects(a, q, w)
	f := &queryDiffFX{a: a, e: e, w: w, clock: queryDiffNow, selected: s.Selected}
	e.hooks = map[string]func(nodeQueryEffect) nodeQueryReply{"running": func(nodeQueryEffect) nodeQueryReply { return nodeQueryReply{Bool: s.Running} }}
	checkLock := func(where string) {
		unlocked := a.mu.TryLock()
		if unlocked {
			a.mu.Unlock()
		}
		wantHeld := q.Borrowed || q.Op == "node-latency"
		if unlocked == wantHeld {
			panic("unexpected lock state at " + where)
		}
	}
	e.controller = func(route string) (map[string]any, error) {
		checkLock("controller")
		f.io = append(f.io, route)
		if strings.HasPrefix(route, "/proxies/") {
			if s.GroupFail {
				return nil, errors.New("synthetic group")
			}
			if s.Edit && !q.Borrowed {
				a.mu.Lock()
				testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/changed" })
				testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "changed" })
				a.manualNodes = nil
				a.mu.Unlock()
			}
			if route == "/proxies/SmartVPN" {
				return map[string]any{"now": f.selected}, nil
			}
			return map[string]any{"now": s.Nested}, nil
		}
		name := strings.TrimPrefix(route, "/providers/proxies/")
		if name == "SmartVPNSubscription" && s.SubFail || name == manualProviderName && s.ManualFail {
			return nil, errors.New("synthetic provider")
		}
		if s.WaitEmpty {
			return map[string]any{}, nil
		}
		return s.Providers[name], nil
	}
	e.readFile = func(string) ([]byte, error) {
		checkLock("file")
		f.io = append(f.io, "read-profile")
		if s.ReadFail {
			return nil, errors.New("synthetic read")
		}
		return []byte("synthetic"), nil
	}
	e.parse = func([]byte) []proxyNode { return append([]proxyNode(nil), s.Parsed...) }
	e.do = func(req *http.Request, d time.Duration) (*http.Response, error) {
		checkLock("delay")
		f.requests++
		f.timeout = d
		f.requestURL = req.URL.String()
		if q.Op == "delay" && (req.URL.Path != "/group/SmartVPN/delay" || req.URL.Query().Get("url") != q.Target || req.URL.Query().Get("timeout") != fmt.Sprint(q.Timeout.Milliseconds()) || d != q.Timeout+12*time.Second) {
			panic("delay URL/query/timeout changed")
		}
		if req.Method != "GET" || req.Header.Get("Authorization") != "Bearer synthetic-token" {
			panic("delay method/auth changed")
		}
		if s.RequestFail {
			return nil, errors.New("synthetic request")
		}
		if s.AfterDelay != "" {
			f.selected = s.AfterDelay
		}
		return &http.Response{StatusCode: s.Status, Header: make(http.Header), Body: queryDiffBody{Reader: strings.NewReader(s.Body), close: func() { f.closed++; f.io = append(f.io, "body-close") }}}, nil
	}
	e.now = func() time.Time { v := f.clock; f.clock = f.clock.Add(s.ClockStep); return v }
	e.sleep = func(d time.Duration) {
		if d != 50*time.Millisecond {
			panic("poll interval changed")
		}
		f.sleeps++
		f.clock = f.clock.Add(d)
	}
	return f
}
func (f *queryDiffFX) Call(x nodeQueryEffect) nodeQueryReply {
	if f.e.hooks[x.Kind] == nil {
		switch x.Kind {
		case "lock", "unlock", "endpoint", "metadata", "group", "read-profile", "parse-profile", "provider", "delay-request", "delay-decode", "delay-value", "body-close", "deadline", "before", "sleep", "response":
		default:
			panic("unmocked node query port " + x.Kind)
		}
	}
	r := f.e.Call(x)
	x.Result.Nodes = append([]proxyNode(nil), x.Result.Nodes...)
	if x.Result.Delays != nil {
		v := map[string]int{}
		for k, n := range x.Result.Delays {
			v[k] = n
		}
		x.Result.Delays = v
	}
	f.trace = append(f.trace, queryDiffFrame{x, f.e.locked, f.a.settingsLocked().SelectedNode, f.a.settingsLocked().SubscriptionURL})
	return r
}
func (f *queryDiffFX) Release() { f.e.Release() }
func queryDiffRun(t *testing.T, q nodeQueryServiceRequest, s queryDiffSpec, run func(nodeQueryServiceRequest, nodeQueryServiceEffects) nodeQueryServiceResult) (nodeQueryServiceResult, *queryDiffFX) {
	t.Helper()
	f := queryDiffNew(q, s)
	if q.Borrowed {
		f.a.mu.Lock()
	}
	r := run(q, f)
	f.Release()
	if q.Borrowed {
		if f.a.mu.TryLock() {
			f.a.mu.Unlock()
			t.Fatal("released borrowed query lock")
		}
		f.a.mu.Unlock()
	}
	if !f.a.mu.TryLock() {
		t.Fatal("query lock leaked")
	}
	f.a.mu.Unlock()
	return r, f
}
func queryDiffCompare(t *testing.T, q nodeQueryServiceRequest, s queryDiffSpec, run func(nodeQueryServiceRequest, nodeQueryServiceEffects) nodeQueryServiceResult) {
	t.Helper()
	a, x := queryDiffRun(t, q, s, runGoNodeQueryService)
	b, y := queryDiffRun(t, q, s, run)
	if fmt.Sprint(a.Err) != fmt.Sprint(b.Err) {
		t.Fatal("error mismatch", a.Err, b.Err)
	}
	a.Err = nil
	b.Err = nil
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.io, y.io) || x.closed != y.closed || x.sleeps != y.sleeps || x.requests != y.requests || x.w.Body.String() != y.w.Body.String() {
		t.Fatalf("q=%#v\nGo=%#v Rust=%#v\nGoTrace=%#v\nRustTrace=%#v", q, a, b, x.trace, y.trace)
	}
}
func TestMigrationNodeQuerySafeSelectedLifecycle(t *testing.T) {
	for _, op := range []string{"nodes", "list", "latency", "node-latency", "wait", "delay"} {
		queryDiffCompare(t, queryDiffRequest(op), queryDiffBase(), runSelectedNodeQueryService)
	}
}
func queryLegacyGoldens(t *testing.T, run func(nodeQueryServiceRequest, nodeQueryServiceEffects) nodeQueryServiceResult) {
	s := queryDiffBase()
	q := queryDiffRequest("list")
	r, f := queryDiffRun(t, q, s, run)
	if r.Err != nil || len(r.Nodes) != 3 || r.Nodes[0].Name != "a" || r.Nodes[1].Name != "b" || r.Nodes[2].Name != "manual-only" {
		t.Fatal("provider order/dedup", r)
	}
	if r.Nodes[0].Type != "kernel-a" || r.Nodes[0].Network != "manual" || !r.Nodes[0].Manual || !r.Nodes[0].TLS || !r.Nodes[0].Selected || r.Nodes[1].SourceLabel != "original-label" {
		t.Fatal("provider/detail/manual precedence", r.Nodes)
	}
	if !reflect.DeepEqual(f.io, []string{"/proxies/SmartVPN", "read-profile", "/providers/proxies/SmartVPNSubscription", "/providers/proxies/" + manualProviderName}) {
		t.Fatal("provider request order", f.io)
	}
	s = queryDiffBase()
	s.SubFail = true
	s.ManualFail = true
	r, _ = queryDiffRun(t, q, s, run)
	if r.Err != nil || r.Nodes == nil || len(r.Nodes) != 0 {
		t.Fatal("provider errors not ignored/empty shape")
	}
	s = queryDiffBase()
	s.Running = false
	s.Cached = nil
	s.Manual = nil
	r, f = queryDiffRun(t, queryDiffRequest("nodes"), s, run)
	if r.Nodes != nil || r.ProxyEnabled || len(f.io) != 0 {
		t.Fatal("offline nil/proxy/no-network")
	}
	s = queryDiffBase()
	s.Running = false
	s.Cached = []proxyNode{{Name: "a", Type: "first"}, {Name: "a", Type: "second"}}
	r, _ = queryDiffRun(t, queryDiffRequest("nodes"), s, run)
	if len(r.Nodes) != 2 || r.Nodes[0].Type != "first" || r.Nodes[1].Type != "second" {
		t.Fatal("offline cached duplicates removed/manual overwrote")
	}
	for _, op := range []string{"latency", "node-latency"} {
		s = queryDiffBase()
		s.AfterDelay = autoGroup
		r, f = queryDiffRun(t, queryDiffRequest(op), s, run)
		if r.Status != 0 || f.closed != 1 {
			t.Fatal("latency failed")
		}
		if op == "latency" {
			if r.Selected != "b" || r.Latency != 20 || !reflect.DeepEqual(f.io, []string{"body-close", "/proxies/SmartVPN", "/proxies/" + autoGroup}) || f.timeout != 20*time.Second {
				t.Fatal("active measured-before nested read", r, f.io, f.timeout)
			}
		} else if len(f.io) != 1 || f.timeout != 4321*time.Millisecond+12*time.Second {
			t.Fatal("node latency timeout/extra active lookup")
		}
	}
	for _, tc := range []struct {
		status int
		body   string
		fail   bool
	}{{200, `{"x":7}`, false}, {200, "null", false}, {200, "{", true}, {204, "", true}, {500, `{}`, true}, {200, strings.Repeat(" ", 256*1024) + `{}`, true}} {
		s = queryDiffBase()
		s.Status = tc.status
		s.Body = tc.body
		r, f = queryDiffRun(t, queryDiffRequest("delay"), s, run)
		if (r.Err != nil) != tc.fail || f.closed != 1 || f.requests != 1 {
			t.Fatal("HTTP200/decode/close", tc, r.Err, f.closed)
		}
		if tc.body == "null" && r.Delays != nil {
			t.Fatal("null delays normalized")
		}
	}
	for _, timeout := range []time.Duration{-1, 0, time.Millisecond} {
		s = queryDiffBase()
		s.WaitEmpty = true
		s.ClockStep = time.Millisecond
		q = queryDiffRequest("wait")
		q.Timeout = timeout
		r, f = queryDiffRun(t, q, s, run)
		if r.Err == nil || len(f.io) != 0 || f.sleeps != 0 {
			t.Fatal("deadline<=0/equality queried nodes")
		}
	}
	s = queryDiffBase()
	s.WaitEmpty = true
	s.ClockStep = 0
	q = queryDiffRequest("wait")
	q.Timeout = time.Millisecond
	r, f = queryDiffRun(t, q, s, run)
	if r.Err == nil || f.sleeps != 1 || !f.clock.Equal(queryDiffNow.Add(50*time.Millisecond)) {
		t.Fatal("50ms poll overshoot changed", f.sleeps, f.clock)
	}
	for _, op := range []string{"list", "delay", "wait"} {
		q = queryDiffRequest(op)
		q.Borrowed = true
		queryDiffRun(t, q, queryDiffBase(), run)
	}
	q = queryDiffRequest("list")
	s = queryDiffBase()
	f = queryDiffNew(q, s)
	read := f.e.readFile
	f.e.readFile = func(path string) ([]byte, error) {
		b, err := read(path)
		f.a.mu.Lock()
		testEditSettings(f.a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/replaced" })
		testCatalogEdit(f.a, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "replaced" })
		f.a.manualNodes = nil
		f.a.mu.Unlock()
		return b, err
	}
	r = run(q, f)
	f.Release()
	if r.Nodes[0].Network != "manual" || r.Nodes[1].SourceLabel != "original-label" || len(f.a.manualNodes) != 0 {
		t.Fatal("metadata not immutable across unlocked read", r.Nodes)
	}
}
func TestMigrationNodeQuerySafeLegacyGoldens(t *testing.T) {
	queryLegacyGoldens(t, runSelectedNodeQueryService)
}
