package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type chinaHTTPRoundTrip func(*http.Request) (*http.Response, error)

func (f chinaHTTPRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type chinaCloseBody struct {
	io.Reader
	closes int
}

func (b *chinaCloseBody) Close() error { b.closes++; return nil }
func TestChinaServiceSafeHTTPAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		body    string
		wantErr bool
	}{{"ok", 200, "body", false}, {"status", 503, "ignored", true}, {"oversize", 200, strings.Repeat("x", chinaIPListMax+1), true}} {
		t.Run(tc.name, func(t *testing.T) {
			b := &chinaCloseBody{Reader: strings.NewReader(tc.body)}
			calls := 0
			c := &http.Client{Timeout: chinaIPFetchWait, Transport: chinaHTTPRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" {
					t.Fatal(r.Method)
				}
				return &http.Response{StatusCode: tc.code, Status: "synthetic", Body: b, Header: make(http.Header)}, nil
			})}
			v, e := fetchChinaIPFileUsing("https://fake.invalid/list", c)
			if (e != nil) != tc.wantErr || calls != 1 || b.closes != 1 {
				t.Fatalf("%v calls=%d close=%d", e, calls, b.closes)
			}
			if !tc.wantErr && string(v) != tc.body {
				t.Fatal("body")
			}
		})
	}
}
func TestChinaServiceSafeProbeLedger(t *testing.T) {
	a := &app{}
	e := newAppChinaEffects(a, chinaServiceRequest{Op: "probe"})
	list, config := 0, 0
	e.hooks = map[string]func(chinaServiceEffect) chinaServiceReply{
		"free-port":           func(chinaServiceEffect) chinaServiceReply { return chinaServiceReply{} },
		"probe-mkdir":         func(chinaServiceEffect) chinaServiceReply { return chinaServiceReply{} },
		"probe-list-write":    func(chinaServiceEffect) chinaServiceReply { panic("write failed") },
		"probe-remove-list":   func(chinaServiceEffect) chinaServiceReply { list++; panic("close failed") },
		"probe-remove-config": func(chinaServiceEffect) chinaServiceReply { config++; return chinaServiceReply{} },
	}
	r := runChinaServiceWithEffects(e.q, e)
	if r.Err == nil || list != 1 || config != 1 {
		t.Fatalf("%v %d %d", r.Err, list, config)
	}
	e.Release()
	if list != 1 || config != 1 {
		t.Fatal("cleanup repeated")
	}
}
func TestChinaServiceSafeProbeFiles(t *testing.T) {
	a := &app{home: t.TempDir()}
	e := newAppChinaEffects(a, chinaServiceRequest{Op: "probe"})
	e.hooks = map[string]func(chinaServiceEffect) chinaServiceReply{"free-port": func(chinaServiceEffect) chinaServiceReply { e.port = 12345; return chinaServiceReply{} }, "probe-check": func(chinaServiceEffect) chinaServiceReply {
		if _, err := os.Stat(filepath.Join(a.home, "china-probe.yaml")); err != nil {
			t.Fatal(err)
		}
		return chinaServiceReply{Err: errors.New("synthetic reject")}
	}, "log": func(chinaServiceEffect) chinaServiceReply { return chinaServiceReply{} }}
	r := runChinaServiceWithEffects(e.q, e)
	if r.Err != nil || r.Flag {
		t.Fatalf("%+v", r)
	}
	for _, p := range []string{"china-probe.yaml", "providers/china-probe.txt"} {
		if _, err := os.Stat(filepath.Join(a.home, p)); !os.IsNotExist(err) {
			t.Fatal("probe file retained")
		}
	}
}
func TestChinaServiceSafeRefreshFailure(t *testing.T) {
	a := &app{}
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: true})
	e := newAppChinaEffects(a, chinaServiceRequest{Op: "refresh"})
	e.hooks = map[string]func(chinaServiceEffect) chinaServiceReply{"stat": func(chinaServiceEffect) chinaServiceReply { panic("read interrupted") }}
	r := runChinaServiceWithEffects(e.q, e)
	if r.Err == nil || a.chinaStateLocked().Fetching {
		t.Fatal("refresh left in flight")
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}
func TestChinaServiceSafeBorrowedLock(t *testing.T) {
	a := &app{}
	a.mu.Lock()
	e := newAppChinaEffects(a, chinaServiceRequest{})
	e.Release()
	if a.mu.TryLock() {
		t.Fatal("released caller lock")
	}
	a.mu.Unlock()
}

func TestChinaServiceSafeHTTPConfiguration(t *testing.T) {
	c := newChinaIPHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr == http.DefaultTransport || tr.Proxy != nil || c.Timeout != chinaIPFetchWait {
		t.Fatal("single HTTP transport configuration")
	}
}
func TestChinaServiceSafeWriteFailureKeepsTemp(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "providers", "china-ip.txt")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	e := newAppChinaEffects(&app{home: home}, chinaServiceRequest{Op: "write"})
	r := runChinaServiceWithEffects(chinaServiceRequest{Op: "write", Ranges: []string{"1.0.0.0/8"}}, e)
	if r.Err == nil {
		t.Fatal("rename to directory succeeded unexpectedly")
	}
	b, err := os.ReadFile(target + ".tmp")
	if err != nil || string(b) != "1.0.0.0/8\n" {
		t.Fatal("temporary file removed or changed")
	}
}

type chinaPanicWriter struct{ calls int }

func (w *chinaPanicWriter) Header() http.Header       { return http.Header{} }
func (w *chinaPanicWriter) WriteHeader(int)           { w.calls++; panic("writer failure") }
func (w *chinaPanicWriter) Write([]byte) (int, error) { w.calls++; panic("writer failure") }
func TestChinaServiceSafeHTTPStartedPanic(t *testing.T) {
	a := &app{}
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: true}) // refuse duplicate launch, no worker
	w := &chinaPanicWriter{}
	defer func() {
		if recover() == nil || w.calls != 1 {
			t.Fatalf("writer panic not propagated once: %d", w.calls)
		}
	}()
	a.refreshChinaDirect(w, nil)
}

func TestChinaServiceSafeRefreshAuthorityGate(t *testing.T) {
	for _, mode := range []string{"closed", "pending", "catalog-fault"} {
		t.Run(mode, func(t *testing.T) {
			a := &app{}
			s := a.serviceStateLocked()
			switch mode {
			case "closed":
				_ = a.closeServiceStateLocked()
			case "pending":
				s.pending = &serviceStateCommand{Kind: 7}
			case "catalog-fault":
				s.catalogFault = errors.New("unconfirmed resources")
			}
			e := newAppChinaEffects(a, chinaServiceRequest{Op: "refresh"})
			calls := 0
			e.hooks = map[string]func(chinaServiceEffect) chinaServiceReply{}
			for _, kind := range []string{"stat", "fetch", "spawn", "read", "fetch-override"} {
				e.hooks[kind] = func(chinaServiceEffect) chinaServiceReply { calls++; panic("IO after unavailable authority") }
			}
			before := s.nextCommand
			r := runChinaServiceWithEffects(e.q, e)
			if s.nextCommand != before {
				t.Fatal("unavailable authority retried mutation")
			}
			if r.Err == nil || calls != 0 {
				t.Fatalf("%v IO=%d", r.Err, calls)
			}
			if !a.mu.TryLock() {
				t.Fatal("gate leaked lock")
			}
			a.mu.Unlock()
		})
	}
}

func TestChinaServiceSafeOptionsPartialError(t *testing.T) {
	a := &app{}
	a.patchControlLocked(controlImportPolicy, serviceControlState{ImportPolicy: true})
	q := chinaServiceRequest{Op: "options", Body: []byte("[invalid"), HasSubscription: true}
	e := newAppChinaEffects(a, q)
	e.hooks = map[string]func(chinaServiceEffect) chinaServiceReply{"stat": func(chinaServiceEffect) chinaServiceReply { return chinaServiceReply{Err: errors.New("missing")} }}
	r := runChinaServiceWithEffects(q, e)
	if r.Err == nil || !r.Options.SourceRuleOrder {
		t.Fatalf("import failure lost partial options: %+v", r)
	}
}
