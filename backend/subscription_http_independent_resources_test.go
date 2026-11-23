package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func subHTTPIndependentRaw(t *testing.T, run subHTTPIndependentRunner) {
	raw := []struct {
		body   []byte
		format string
	}{{nil, "empty"}, {[]byte("proxies: []"), "clash-yaml"}, {[]byte(`{"outbounds":[]}`), "singbox-json"}, {[]byte("trojan://p@example.org:443#n"), "uri-list"}, {[]byte("{broken"), "singbox-json"}, {[]byte{255, 254}, "unknown"}, {[]byte("hy2://invalid"), "uri-list"}}
	for _, v := range append([]struct {
		body   []byte
		format string
	}{}, raw[1:4]...) {
		raw = append(raw, struct {
			body   []byte
			format string
		}{[]byte(base64.StdEncoding.EncodeToString(v.body)), "base64-" + v.format})
	}
	for i, c := range raw {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			q := subscriptionHTTPServiceRequest{Op: "analyze"}
			e := newAppSubscriptionHTTPEffects(nil, q)
			e.payload = c.body
			r := run(q, e)
			if r.Err != nil || r.Analysis.Format != c.format || r.Analysis.Bytes != len(c.body) || r.Analysis.Present != (len(c.body) > 0) {
				t.Fatalf("raw %+v", r)
			}
		})
	}
	vm := base64.RawStdEncoding.EncodeToString([]byte(`{"ps":"normal","net":"ws","tls":"reality","host":"h","path":"/"}`))
	e := newAppSubscriptionHTTPEffects(nil, subscriptionHTTPServiceRequest{Op: "analyze"})
	e.payload = []byte("vmess://" + vm + "\n vmess://" + vm + "\nhy2://x")
	r := subscriptionHTTPPayload(run(e.q, e).Analysis)
	if r.ValidVmess != 1 || r.UriCounts["vmess"] != 1 || r.Networks["ws"] != 1 || r.WsHostSet != 1 || r.WsPathSet != 1 {
		t.Fatalf("actual raw vmess %+v", r)
	}
}

type subHTTPIndependentTrackedBody struct {
	io.Reader
	closed int
}

func (b *subHTTPIndependentTrackedBody) Close() error { b.closed++; return nil }
func subHTTPIndependentDiagnostic(t *testing.T, run subHTTPIndependentRunner) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			a := &app{}
			q := subscriptionHTTPServiceRequest{Op: "diagnostics"}
			e := newAppSubscriptionHTTPEffects(a, q)
			e.w = httptest.NewRecorder()
			e.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{"source-snapshot": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
				e.subscriptionURL = "https://synthetic.invalid/sub?z=1&a=2"
				e.home = "never-read"
				return subscriptionHTTPServiceReply{Flag: true}
			}}
			data := []byte("proxies: []")
			if large {
				data = bytes.Repeat([]byte(" "), 10*1024*1024+1)
			}
			body := &subHTTPIndependentTrackedBody{Reader: bytes.NewReader(data)}
			e.do = func(r *http.Request) (*http.Response, error) {
				if !a.mu.TryLock() {
					t.Fatal("HTTP under lock")
				}
				a.mu.Unlock()
				if r.Header.Get("User-Agent") != subscriptionUserAgent {
					t.Fatal("UA")
				}
				return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: body}, nil
			}
			reads := 0
			e.readFile = func(p string) ([]byte, error) {
				reads++
				if strings.HasSuffix(p, ".source") {
					return []byte(subscriptionHash(e.subscriptionURL)), nil
				}
				if strings.HasSuffix(p, ".format") {
					return []byte(" clash \n"), nil
				}
				return nil, os.ErrNotExist
			}
			r := run(q, e)
			if r.Err != nil || body.closed != 1 || reads != 3 || e.report.Fetch.StatusCode != 503 || e.report.AppliedFormat != "clash" {
				t.Fatalf("diagnostic %+v close=%d reads=%d", r, body.closed, reads)
			}
			if large {
				if e.report.Fetch.Bytes != 10*1024*1024 || e.report.Fetch.Error != subscriptionHTTPLargeError {
					t.Fatal(e.report.Fetch)
				}
			} else if e.report.Fetch.Payload.Format != "clash-yaml" || e.report.Fetch.Error != "" {
				t.Fatal(e.report.Fetch)
			}
			if !a.mu.TryLock() {
				t.Fatal("lock leaked")
			}
			a.mu.Unlock()
		})
	}
}
func subHTTPIndependentChannels(t *testing.T, run subHTTPIndependentRunner) {
	a := &app{}
	q := subscriptionHTTPServiceRequest{Op: "start"}
	e := newAppSubscriptionHTTPEffects(a, q)
	var c <-chan struct{}
	e.spawn = func(v <-chan struct{}) {
		if a.autoMergeCancel == nil || v != a.autoMergeCancel {
			t.Fatal("spawn before publish")
		}
		c = v
	}
	if r := run(q, e); r.Err != nil {
		t.Fatal(r.Err)
	}
	ticks := make(chan time.Time)
	seen := make(chan struct{})
	finished := make(chan struct{})
	stopCount := 0
	loop := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "loop"})
	loop.cancel = c
	loop.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != 10*time.Minute {
			t.Fatal(d)
		}
		return ticks, func() { stopCount++ }
	}
	loop.tick = func() {
		child := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "tick"})
		child.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{"auto-tick": func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Error("child not locked")
			}
			return subscriptionHTTPServiceReply{}
		}}
		if r := run(child.q, child); r.Err != nil {
			t.Error(r.Err)
		}
		seen <- struct{}{}
	}
	go func() { defer close(finished); run(loop.q, loop) }()
	for i := 0; i < 2; i++ {
		select {
		case ticks <- time.Time{}:
		case <-time.After(5 * time.Second):
			t.Fatal("ticker blocked")
		}
		select {
		case <-seen:
		case <-time.After(5 * time.Second):
			t.Fatal("child blocked")
		}
	}
	stop := newAppSubscriptionHTTPEffects(a, subscriptionHTTPServiceRequest{Op: "stop"})
	run(stop.q, stop)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("loop leaked")
	}
	if stopCount != 1 || a.autoMergeCancel != nil {
		t.Fatal("stop resource count")
	}
	loop.Release()
	if stopCount != 1 {
		t.Fatal("double stop")
	}
}
func TestSubscriptionHTTPIndependentSelectedRaw(t *testing.T) {
	subHTTPIndependentRaw(t, runSelectedSubscriptionHTTPService)
}
func TestSubscriptionHTTPIndependentSelectedDiagnostic(t *testing.T) {
	subHTTPIndependentDiagnostic(t, runSelectedSubscriptionHTTPService)
}
func TestSubscriptionHTTPIndependentSelectedChannels(t *testing.T) {
	subHTTPIndependentChannels(t, runSelectedSubscriptionHTTPService)
}

type subHTTPWriter struct {
	a       *app
	headers http.Header
	writes  int
	fail    bool
}

func (w *subHTTPWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = http.Header{}
	}
	return w.headers
}
func (w *subHTTPWriter) WriteHeader(int) {
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		panic("writer unlocked")
	}
	w.writes++
}
func (w *subHTTPWriter) Write(b []byte) (int, error) {
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		panic("writer unlocked")
	}
	if w.fail {
		panic("synthetic writer")
	}
	return len(b), nil
}
func subHTTPIndependentRefreshResources(t *testing.T, run subHTTPIndependentRunner) {
	for _, writerPanic := range []bool{false, true} {
		t.Run(fmt.Sprint(writerPanic), func(t *testing.T) {
			a := &app{cachedNodes: []proxyNode{{Name: "old"}}}
			q := subscriptionHTTPServiceRequest{Op: "refresh"}
			e := newAppSubscriptionHTTPEffects(a, q)
			w := &subHTTPWriter{a: a, fail: writerPanic}
			e.w = w
			e.hooks = map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply{}
			calls := []string{}
			for _, kind := range []string{"running", "fetch-profile", "provider-put", "wait-nodes", "list-nodes", "save-nodes"} {
				k := kind
				e.hooks[k] = func(v subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {
					calls = append(calls, k)
					switch k {
					case "running":
						return subscriptionHTTPServiceReply{Flag: true}
					case "wait-nodes":
						if v.Interval != 15*time.Second {
							t.Fatal(v.Interval)
						}
					case "list-nodes":
						e.nodes = []proxyNode{{Name: "new"}}
					case "save-nodes":
						return subscriptionHTTPServiceReply{Err: subHTTPIndependentError}
					}
					return subscriptionHTTPServiceReply{}
				}
			}
			var p any
			func() { defer func() { p = recover() }(); run(q, e) }()
			if writerPanic {
				if _, ok := p.(subscriptionHTTPResponsePanic); !ok {
					t.Fatalf("writer panic lost %T %v", p, p)
				}
			} else if p != nil {
				t.Fatal(p)
			}
			if len(a.cachedNodes) != 1 || a.cachedNodes[0].Name != "new" || w.writes != 1 || len(calls) != 6 {
				t.Fatalf("partialcommit/calls %+v %v", a.cachedNodes, calls)
			}
			if !a.mu.TryLock() {
				t.Fatal("writer leaked mutex")
			}
			a.mu.Unlock()
		})
	}
}
func TestSubscriptionHTTPIndependentSelectedRefreshResources(t *testing.T) {
	subHTTPIndependentRefreshResources(t, runSelectedSubscriptionHTTPService)
}
