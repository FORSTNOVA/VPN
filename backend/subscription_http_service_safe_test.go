package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type subscriptionHTTPTestBody struct {
	left, closed int
	err          error
	a            *app
	t            *testing.T
}

func (b *subscriptionHTTPTestBody) Read(p []byte) (int, error) {
	if b.a != nil {
		if !b.a.mu.TryLock() {
			b.t.Fatal("diagnostic read held app mutex")
		}
		b.a.mu.Unlock()
	}
	if b.left == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > b.left {
		n = b.left
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	b.left -= n
	if b.left == 0 {
		return n, b.err
	}
	return n, nil
}
func (b *subscriptionHTTPTestBody) Close() error { b.closed++; return nil }

type subscriptionHTTPTestWriter struct {
	*httptest.ResponseRecorder
	a             *app
	t             *testing.T
	locked, crash bool
	calls         int
}

func (w *subscriptionHTTPTestWriter) check() {
	got := w.a.mu.TryLock()
	if got {
		w.a.mu.Unlock()
	}
	if got == w.locked {
		w.t.Fatal("unexpected response lock scope")
	}
}
func (w *subscriptionHTTPTestWriter) Write(b []byte) (int, error) {
	w.check()
	w.calls++
	if w.crash {
		panic("synthetic subscription writer")
	}
	return w.ResponseRecorder.Write(b)
}
func (w *subscriptionHTTPTestWriter) WriteHeader(n int) { w.check(); w.ResponseRecorder.WriteHeader(n) }
func subscriptionHTTPSafeFX(t *testing.T, op string) (*app, *appSubscriptionHTTPEffects) {
	t.Helper()
	a := settingsDocumentTestApp(t, t.TempDir())
	q := subscriptionHTTPServiceRequest{Op: op}
	return a, newAppSubscriptionHTTPEffects(a, q)
}
func TestSubscriptionHTTPServiceSafeRefreshPartialSave(t *testing.T) {
	a, e := subscriptionHTTPSafeFX(t, "refresh")
	source := []proxyNode{{Name: "fresh"}}
	saved := 0
	e.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{
		"running": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			return subscriptionHTTPServiceReply{Flag: true}
		},
		"fetch-profile": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			return subscriptionHTTPServiceReply{}
		},
		"provider-put": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			return subscriptionHTTPServiceReply{}
		},
		"wait-nodes": func(q subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			if q.Interval != 15*time.Second {
				t.Fatal(q.Interval)
			}
			return subscriptionHTTPServiceReply{}
		},
		"list-nodes": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			e.nodes = source
			return subscriptionHTTPServiceReply{}
		},
		"save-nodes": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			saved++
			if len(a.cachedNodes) != 1 || a.cachedNodes[0].Name != "fresh" {
				t.Fatal("save before memory commit")
			}
			return subscriptionHTTPServiceReply{Err: errors.New("save")}
		},
	}
	w := &subscriptionHTTPTestWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t, locked: true}
	e.w = w
	r := runSubscriptionHTTPWithEffects(e.q, e)
	if r.Err != nil || w.Code != 500 || saved != 1 || len(a.cachedNodes) != 1 {
		t.Fatal(r, w.Code, saved)
	}
	source[0].Name = "external"
	if a.cachedNodes[0].Name != "fresh" {
		t.Fatal("cached slice must append-copy")
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}
func TestSubscriptionHTTPServiceSafeDiagnosticBoundaries(t *testing.T) {
	for _, which := range []string{"large", "writer", "network"} {
		t.Run(which, func(t *testing.T) {
			a, e := subscriptionHTTPSafeFX(t, "diagnostics")
			a.patchControlLocked(controlSubscriptionURL, serviceControlState{SubscriptionURL: "https://example.invalid/sub?z=x&a=y"})
			b := &subscriptionHTTPTestBody{left: 10*1024*1024 + 1, err: errors.New("read"), a: a, t: t}
			if which == "writer" {
				b.left = 0
			}
			e.do = func(req *http.Request) (*http.Response, error) {
				if !a.mu.TryLock() {
					t.Fatal("diagnostic HTTP held mutex")
				}
				a.mu.Unlock()
				if req.Header.Get("User-Agent") != subscriptionUserAgent {
					t.Fatal("UA missing")
				}
				if which == "network" {
					return nil, errors.New("network")
				}
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"text/custom"}}, Body: b}, nil
			}
			calls := 0
			e.parseNodes = func([]byte) []proxyNode { calls++; return nil }
			e.readFile = func(path string) ([]byte, error) {
				if !a.mu.TryLock() {
					t.Fatal("diagnostic file held mutex")
				}
				a.mu.Unlock()
				if strings.HasSuffix(path, ".source") {
					return []byte(subscriptionHash(e.subscriptionURL)), nil
				}
				if strings.HasSuffix(path, ".format") {
					return []byte(" format \n"), nil
				}
				return []byte{}, nil
			}
			w := &subscriptionHTTPTestWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t, crash: which == "writer"}
			e.w = w
			var p any
			func() { defer func() { p = recover() }(); runSubscriptionHTTPWithEffects(e.q, e) }()
			if (p != nil) != (which == "writer") {
				t.Fatalf("panic %v", p)
			}
			if which != "network" && b.closed != 1 {
				t.Fatal("body not closed once")
			}
			e.Release()
			if which != "network" && b.closed != 1 {
				t.Fatal("body replay")
			}
			if w.calls != 1 {
				t.Fatal("writer replay")
			}
			if which == "large" {
				if e.report.Fetch.Error != subscriptionHTTPLargeError || e.report.Fetch.Bytes != 10*1024*1024 || e.report.Fetch.StatusCode != 503 || calls != 2 {
					t.Fatal(e.report.Fetch.Error, e.report.Fetch.Bytes, calls)
				}
			}
			if e.report.AppliedFormat != "format" || len(e.report.RequestQueryKeys) != 2 || e.report.RequestQueryKeys[0] != "a" {
				t.Fatal("cache/hash/query projection")
			}
		})
	}
}
func TestSubscriptionHTTPServiceSafeDirectClientPolicy(t *testing.T) {
	c := newSubscriptionHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr == http.DefaultTransport || tr.Proxy != nil || c.Timeout != 20*time.Second {
		t.Fatal("direct client policy changed")
	}
	tr.CloseIdleConnections()
}
func TestSubscriptionHTTPServiceSafeSchedulerResources(t *testing.T) {
	a, e := subscriptionHTTPSafeFX(t, "start")
	spawn := 0
	var published <-chan struct{}
	e.spawn = func(c <-chan struct{}) { spawn++; published = c }
	a.mu.Lock()
	r := runSubscriptionHTTPWithEffects(e.q, e)
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	a.mu.Unlock()
	if r.Err != nil || spawn != 1 || published != a.autoMergeCancel {
		t.Fatal("publish/spawn")
	}
	e.Release()
	_, loop := subscriptionHTTPSafeFX(t, "loop")
	loop.a = a
	loop.cancel = published
	ch := make(chan time.Time, 2)
	ch <- time.Time{}
	ch <- time.Time{}
	ticks, stops := 0, 0
	loop.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != 10*time.Minute || ticks != 0 {
			t.Fatal("schedule changed")
		}
		return ch, func() { stops++ }
	}
	loop.tick = func() {
		ticks++
		if ticks == 2 {
			stop := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "stop"})
			a.mu.Lock()
			runSubscriptionHTTPWithEffects(stop.q, stop)
			a.mu.Unlock()
		}
	}
	if r = runSubscriptionHTTPWithEffects(loop.q, loop); r.Err != nil {
		t.Fatal(r.Err)
	}
	loop.Release()
	if ticks != 2 || stops != 1 || a.autoMergeCancel != nil {
		t.Fatal("scheduler lifecycle")
	}
}
func TestSubscriptionHTTPServiceSafePayloadCounts(t *testing.T) {
	body := []byte("vmess://eyJwcyI6Im5vZGUiLCJuZXQiOiJ3cyIsImhvc3QiOiJoIiwicGF0aCI6Ii8ifQ\nhy2://not-counted\n")
	e := newAppSubscriptionHTTPEffects(nil, subscriptionHTTPServiceRequest{Op: "analyze"})
	e.payload = body
	e.parseNodes = func(b []byte) []proxyNode {
		if !bytes.Equal(b, body) {
			t.Fatal("parser body changed")
		}
		return nil
	}
	r := runSubscriptionHTTPWithEffects(e.q, e)
	p := subscriptionHTTPPayload(r.Analysis)
	if r.Err != nil || p.Format != "uri-list" || p.ValidVmess != 1 || p.UriCounts["vmess"] != 1 || len(p.UriCounts) != 7 || p.Networks["ws"] != 1 || p.WsHostSet != 1 || p.WsPathSet != 1 || p.TlsModes["none"] != 1 {
		t.Fatalf("%+v %v", p, r.Err)
	}
}
func TestSubscriptionHTTPServiceSafeCaptureFailureUnlocks(t *testing.T) {
	a, e := subscriptionHTTPSafeFX(t, "diagnostics")
	e.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{"source-snapshot": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply { panic("authority unavailable") }}
	w := &subscriptionHTTPTestWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t}
	e.w = w
	r := runSubscriptionHTTPWithEffects(e.q, e)
	if r.Err == nil || w.Code != 500 || w.calls != 1 {
		t.Fatal("capture fault not safe500")
	}
	if !a.mu.TryLock() {
		t.Fatal("capture lock leaked")
	}
	a.mu.Unlock()
}
