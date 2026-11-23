package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type bootIndependentBody struct {
	io.Reader
	closed int
}

func (b *bootIndependentBody) Close() error { b.closed++; return nil }
func bootIndependentResources(t *testing.T, run bootIndependentRunner) {
	for _, body := range []string{`{"elevated":true,"connected":"bad"}`, `{broken`, strings.Repeat(" ", 64*1024) + `{"connected":true}`} {
		q := bootstrapServiceRequest{Op: "probe"}
		e := newBootstrapEffects(q)
		e.record = serviceRecord{Port: 1234, Token: "synthetic"}
		b := &bootIndependentBody{Reader: strings.NewReader(body)}
		e.request = func(r serviceRecord, m, p string) (*http.Response, error) {
			if m != "GET" || p != "/api/state" {
				t.Fatal("request route")
			}
			return &http.Response{StatusCode: 200, Body: b}, nil
		}
		e.platform = func(bootstrapServiceEffect) bootstrapServiceReply { panic("unexpected platform") }
		r := run(q, e)
		if !r.Bool || r.Err != nil || b.closed != 1 {
			t.Fatalf("probe malformed/limit %+v closed%d", r, b.closed)
		}
		if strings.Contains(body, `"elevated":true`) && !e.existingElevated {
			t.Fatal("partial JSON lost")
		}
		if e.existingConnected {
			t.Fatal("invalid/overlimit connected changed")
		}
		e.Release()
		if b.closed != 1 {
			t.Fatal("double close")
		}
	}
	q := bootstrapServiceRequest{Op: "handover", ShutdownWait: 0}
	e := newBootstrapEffects(q)
	e.record = serviceRecord{Port: 1234, Token: "synthetic"}
	calls := 0
	closed := 0
	e.clock = func() time.Time { return time.Unix(1, 0) }
	e.sleep = func(time.Duration) { t.Fatal("zero timeout slept") }
	e.request = func(_ serviceRecord, m, p string) (*http.Response, error) {
		calls++
		if calls == 1 {
			if m != "POST" {
				t.Fatal("POST first")
			}
			return nil, bootIndependentErr
		}
		if m != "GET" {
			t.Fatal("GET probe")
		}
		return &http.Response{StatusCode: 200, Body: &bootCloseCallback{Reader: strings.NewReader(`{}`), close: func() { closed++ }}}, nil
	}
	r := run(q, e)
	if r.Bool || calls != 2 || closed != 1 {
		t.Fatalf("zero wait %+v calls%d closed%d", r, calls, closed)
	}
	q = bootstrapServiceRequest{Op: "lock"}
	e = newBootstrapEffects(q)
	closeCount := 0
	e.clock = func() time.Time { return time.Time{} }
	e.hooks = map[string]func(bootstrapServiceEffect) bootstrapServiceReply{"lock-open": func(bootstrapServiceEffect) bootstrapServiceReply {
		e.lockClose = func() { closeCount++ }
		return bootstrapServiceReply{}
	}, "lock-try": func(bootstrapServiceEffect) bootstrapServiceReply { return bootstrapServiceReply{} }, "lock-acquired": func(bootstrapServiceEffect) bootstrapServiceReply { return bootstrapServiceReply{} }}
	r = run(q, e)
	if !r.Lock || r.Err != nil || closeCount != 0 {
		t.Fatalf("handoff %+v close%d", r, closeCount)
	}
	e.Release()
	if closeCount != 0 {
		t.Fatal("transferred lock closed")
	}
}

type bootCloseCallback struct {
	io.Reader
	close func()
}

func (b *bootCloseCallback) Close() error { b.close(); return nil }
func TestBootstrapIndependentSelectedResources(t *testing.T) {
	bootIndependentResources(t, runSelectedBootstrapService)
}
