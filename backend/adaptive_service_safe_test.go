package main

import (
	"context"
	"errors"
	"github.com/miekg/dns"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type adaptiveSafeTransport struct {
	do     func(*http.Request) (*http.Response, error)
	closed atomic.Int32
}

func (t *adaptiveSafeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t.do(r) }
func (t *adaptiveSafeTransport) CloseIdleConnections()                             { t.closed.Add(1) }

type adaptiveSafeBody struct {
	io.Reader
	closed     atomic.Int32
	panicClose bool
}

func (b *adaptiveSafeBody) Close() error {
	b.closed.Add(1)
	if b.panicClose {
		panic("close fault")
	}
	return nil
}
func adaptiveSafeRelay() *adaptiveDNS {
	r := &adaptiveDNS{ctx: context.Background()}
	rt := defaultAdaptiveRuntime()
	rt.timeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		if d != 5*time.Second {
			panic("timeout changed")
		}
		return context.WithCancel(ctx)
	}
	rt.timer = func(d time.Duration) (<-chan time.Time, func()) {
		if d != 500*time.Millisecond {
			panic("timer changed")
		}
		return make(chan time.Time), func() {}
	}
	rt.ticker = func(time.Duration) (<-chan time.Time, func()) { panic("unexpected ticker") }
	r.runtime = rt
	return r
}
func adaptiveSafeResponse(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	if req.Method != "POST" || req.Header.Get("Content-Type") != "application/dns-message" || req.Header.Get("Accept") != "application/dns-message" {
		t.Fatal("request changed")
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	q := new(dns.Msg)
	if err = q.Unpack(raw); err != nil {
		t.Fatal(err)
	}
	a := new(dns.Msg).SetReply(q)
	body, err := a.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: &adaptiveSafeBody{Reader: strings.NewReader(string(body))}}
}

func TestAdaptiveServiceSafeExchange(t *testing.T) {
	for _, code := range []int{200, 503} {
		r := adaptiveSafeRelay()
		var body *adaptiveSafeBody
		r.client = &http.Client{Transport: &adaptiveSafeTransport{do: func(req *http.Request) (*http.Response, error) {
			v := adaptiveSafeResponse(t, req)
			v.StatusCode = code
			body = v.Body.(*adaptiveSafeBody)
			return v, nil
		}}}
		q := new(dns.Msg).SetQuestion("example.test.", dns.TypeTXT)
		ans, err := r.exchange(context.Background(), 0, q)
		if (err == nil) != (code == 200) || (ans != nil) != (code == 200) || body.closed.Load() != 1 {
			t.Fatalf("code %d answer %v error %v", code, ans, err)
		}
	}
	r := adaptiveSafeRelay()
	b := &adaptiveSafeBody{Reader: strings.NewReader("bad"), panicClose: true}
	r.client = &http.Client{Transport: &adaptiveSafeTransport{do: func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 503, Body: b}, nil }}}
	e := newAdaptiveEffects(r, nil, adaptiveServiceRequest{Op: "exchange"})
	e.query = new(dns.Msg).SetQuestion("example.test.", dns.TypeTXT)
	v := runAdaptiveWithEffects(e.q, e)
	e.Release()
	if v.Err == nil || b.closed.Load() != 1 {
		t.Fatal("body cleanup replay/lost error")
	}
}
func TestAdaptiveServiceSafeResolveLateWorker(t *testing.T) {
	r := adaptiveSafeRelay()
	workers := make(chan struct{}, 2)
	r.runtime.spawn = func(f func()) { go func() { f(); workers <- struct{}{} }() }
	first := make(chan struct{})
	started := make(chan string, 2)
	finished := make(chan string, 2)
	timer := make(chan time.Time, 1)
	timer <- time.Time{}
	r.runtime.timer = func(d time.Duration) (<-chan time.Time, func()) {
		if d != 500*time.Millisecond {
			t.Fatal(d)
		}
		return timer, func() {}
	}
	r.client = &http.Client{Transport: &adaptiveSafeTransport{do: func(req *http.Request) (*http.Response, error) {
		host := req.URL.Host
		started <- host
		if host == dohIPs[0] {
			<-first
		}
		resp := adaptiveSafeResponse(t, req)
		finished <- host
		return resp, nil
	}}}
	msg := new(dns.Msg).SetQuestion("example.test.", dns.TypeA)
	out := make(chan *dns.Msg, 1)
	go func() { out <- r.resolve(msg) }()
	<-started
	<-started
	ans := <-out
	if ans == nil || r.preferred.Load() != 1 {
		t.Fatal("backup did not win")
	}
	close(first)
	<-finished
	<-finished
	<-workers
	<-workers
	// Both worker sends finish even after the parent returned, because completion capacity is two.
}
func TestAdaptiveServiceSafeCheckJoinsBoth(t *testing.T) {
	r := adaptiveSafeRelay()
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	started := make(chan int, 2)
	done0 := make(chan struct{})
	r.runtime.hooks = func(q adaptiveServiceRequest) map[string]func(adaptiveServiceEffect) adaptiveServiceReply {
		if q.Op != "check-worker" {
			return nil
		}
		return map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"clock-start": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }, "elapsed": func(adaptiveServiceEffect) adaptiveServiceReply {
			if q.Index == 0 {
				close(done0)
				return adaptiveServiceReply{Value: int64(600 * time.Millisecond)}
			}
			return adaptiveServiceReply{Value: int64(100 * time.Millisecond)}
		}}
	}
	r.client = &http.Client{Transport: &adaptiveSafeTransport{do: func(req *http.Request) (*http.Response, error) {
		i := 0
		if req.URL.Host == dohIPs[1] {
			i = 1
		}
		started <- i
		<-gates[i]
		return adaptiveSafeResponse(t, req), nil
	}}}
	done := make(chan struct{})
	go func() { r.checkUpstreams(); close(done) }()
	<-started
	<-started
	close(gates[0])
	<-done0
	select {
	case <-done:
		t.Fatal("check returned before both completions")
	default:
	}
	close(gates[1])
	<-done
	if r.preferred.Load() != 1 {
		t.Fatal("faster backup not selected")
	}
}
func TestAdaptiveServiceSafeHealthFailClosed(t *testing.T) {
	a := &app{}
	a.setHealthBlockedLocked(false)
	a.setHealthReasonLocked("keep")
	q := adaptiveServiceRequest{Op: "block"}
	e := newAdaptiveEffects(nil, a, q)
	reject := 0
	e.failed = "node"
	e.reason = "exhausted"
	e.now = time.Unix(3, 0)
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"running": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{Flag: true} }, "probe-config": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{Index: 1, Count: 2} }, "site": func(v adaptiveServiceEffect) adaptiveServiceReply {
		if v.Duration != confirmProbeTimeout {
			t.Fatal(v.Duration)
		}
		return adaptiveServiceReply{Text: "restricted"}
	}, "reject": func(adaptiveServiceEffect) adaptiveServiceReply { reject++; panic("must not reject") }, "event": func(adaptiveServiceEffect) adaptiveServiceReply { panic("must not event") }}
	if v := runAdaptiveWithEffects(q, e); v.Err != nil || reject != 0 || a.healthMetaLocked().Blocked || a.healthMetaLocked().BlockReason != "keep" || a.healthMetaLocked().SweepNote == "" {
		t.Fatalf("false block: %+v", v)
	}
	e = newAdaptiveEffects(nil, a, q)
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"custom-present": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }, "blocked": func(adaptiveServiceEffect) adaptiveServiceReply { panic("authority unavailable") }, "reject": func(adaptiveServiceEffect) adaptiveServiceReply { reject++; return adaptiveServiceReply{} }}
	if v := runAdaptiveWithEffects(q, e); v.Err == nil || reject != 0 {
		t.Fatal("authority failure became exhausted")
	}
	a.confirmProbe = func() bool { return true }
	e = newAdaptiveEffects(nil, a, adaptiveServiceRequest{Op: "confirm"})
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"blocked": func(adaptiveServiceEffect) adaptiveServiceReply { panic("custom must precede authority") }}
	if v := runAdaptiveWithEffects(e.q, e); v.Err != nil || !v.Bool {
		t.Fatal("custom precedence")
	}
}
func TestAdaptiveServiceSafeRejectFailure(t *testing.T) {
	a := &app{}
	a.confirmProbe = func() bool { return false }
	a.setHealthReasonLocked("old")
	q := adaptiveServiceRequest{Op: "block"}
	e := newAdaptiveEffects(nil, a, q)
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"reject": func(adaptiveServiceEffect) adaptiveServiceReply {
		return adaptiveServiceReply{Err: errors.New("mock failed")}
	}, "reject-log": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }}
	if v := runAdaptiveWithEffects(q, e); v.Err != nil || a.healthMetaLocked().Blocked || a.healthMetaLocked().BlockReason != "old" {
		t.Fatal("failed reject mutated health")
	}
}
func TestAdaptiveServiceSafeCreateGuard(t *testing.T) {
	for _, fail := range []bool{false, true} {
		r := adaptiveSafeRelay()
		e := newAdaptiveEffects(r, nil, adaptiveServiceRequest{Op: "create"})
		var trace []string
		e.relayCancel = func() { trace = append(trace, "cancel") }
		e.shutdown = func() error { trace = append(trace, "shutdown"); return nil }
		e.clientClose = func() { trace = append(trace, "idle") }
		e.listenerClose = func() { trace = append(trace, "listener") }
		e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{}
		for _, k := range []string{"listen", "relay-context", "client", "server", "server-start", "server-ready"} {
			kind := k
			e.hooks[k] = func(adaptiveServiceEffect) adaptiveServiceReply {
				if kind == "listen" {
					e.newRelay = true
				}
				if kind == "server-start" {
					e.serverStarted = true
				}
				if kind == "server-ready" && fail {
					panic("ready fault")
				}
				return adaptiveServiceReply{}
			}
		}
		v := runAdaptiveWithEffects(e.q, e)
		e.Release()
		if fail {
			if v.Err == nil || strings.Join(trace, ",") != "cancel,shutdown,idle,listener" {
				t.Fatalf("cleanup %+v %v", v, trace)
			}
		} else if v.Err != nil || len(trace) != 0 {
			t.Fatalf("successful create not handed over %+v %v", v, trace)
		}
	}
}
func TestAdaptiveServiceSafeAuthorityErrorReply(t *testing.T) {
	for _, kind := range []string{"custom-present", "custom-confirm", "blocked", "running", "probe-config", "site", "note-present", "set-blocked"} {
		a := &app{}
		q := adaptiveServiceRequest{Op: "block"}
		e := newAdaptiveEffects(nil, a, q)
		rejects := 0
		e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"custom-present": func(adaptiveServiceEffect) adaptiveServiceReply {
			return adaptiveServiceReply{Flag: kind == "custom-confirm"}
		}, "custom-confirm": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }, "blocked": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }, "running": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{Flag: true} }, "probe-config": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{Index: 1, Count: 1} }, "site": func(adaptiveServiceEffect) adaptiveServiceReply {
			if kind == "note-present" {
				return adaptiveServiceReply{Text: "ok"}
			}
			return adaptiveServiceReply{Text: "error"}
		}, "reject": func(adaptiveServiceEffect) adaptiveServiceReply { rejects++; return adaptiveServiceReply{} }, "set-blocked": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }, "set-reason": func(adaptiveServiceEffect) adaptiveServiceReply { panic("continued after unconfirmed commit") }}
		e.hooks[kind] = func(adaptiveServiceEffect) adaptiveServiceReply {
			return adaptiveServiceReply{Err: errors.New("authority unavailable")}
		}
		v := runAdaptiveWithEffects(q, e)
		wantReject := 0
		if kind == "set-blocked" {
			wantReject = 1
		}
		if v.Err == nil || rejects != wantReject {
			t.Fatalf("%s error %+v rejects %d", kind, v, rejects)
		}
	}
}
