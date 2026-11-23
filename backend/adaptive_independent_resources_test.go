package main

import (
	"bytes"
	"context"
	"github.com/miekg/dns"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func adaptiveIndependentChannels(t *testing.T, run adaptiveIndependentRunner) {
	t.Run("resolve-late-worker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		firstGate := make(chan struct{})
		defer func() {
			select {
			case <-firstGate:
			default:
				close(firstGate)
			}
		}()
		started := make(chan int, 2)
		workers := make(chan struct{}, 2)
		timer := make(chan time.Time, 1)
		timer <- time.Time{}
		var mu sync.Mutex
		stops, cancels, calls := 0, 0, 0
		rt := defaultAdaptiveRuntime()
		rt.run = run
		rt.timeout = func(c context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			if d != 5*time.Second {
				t.Error(d)
			}
			c, fn := context.WithCancel(c)
			return c, func() { mu.Lock(); cancels++; mu.Unlock(); fn() }
		}
		rt.timer = func(d time.Duration) (<-chan time.Time, func()) {
			if d != 500*time.Millisecond {
				t.Error(d)
			}
			return timer, func() { mu.Lock(); stops++; mu.Unlock() }
		}
		rt.spawn = func(f func()) { go func() { defer func() { workers <- struct{}{} }(); f() }() }
		r := &adaptiveDNS{ctx: ctx, runtime: rt}
		r.client = &http.Client{Transport: dnsIndependentRT(func(req *http.Request) (*http.Response, error) {
			index := 0
			if req.URL.Host == "8.8.8.8" {
				index = 1
			}
			mu.Lock()
			calls++
			mu.Unlock()
			started <- index
			if index == 0 {
				<-firstGate
			}
			raw, _ := io.ReadAll(req.Body)
			var q dns.Msg
			if q.Unpack(raw) != nil {
				t.Error("query decode")
			}
			a := new(dns.Msg).SetReply(&q)
			raw, _ = a.Pack()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})}
		e := newAdaptiveEffects(r, nil, adaptiveServiceRequest{Op: "resolve"})
		e.query = new(dns.Msg).SetQuestion("example.test.", dns.TypeTXT)
		done := make(chan adaptiveServiceResult, 1)
		go func() { done <- run(e.q, e) }()
		for i := 0; i < 2; i++ {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("both requests not started")
			}
		}
		select {
		case result := <-done:
			if !result.Bool || result.Err != nil || e.answer == nil {
				t.Error(result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("resolve waited for late first")
		}
		if cap(e.done) != 2 || r.preferred.Load() != 1 {
			t.Fatal("completion capacity/CAS")
		}
		close(firstGate)
		for i := 0; i < 2; i++ {
			select {
			case <-workers:
			case <-time.After(5 * time.Second):
				t.Fatal("late worker blocked")
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if calls != 2 || stops != 1 || cancels != 1 {
			t.Fatalf("ownership calls%d timer%d cancel%d", calls, stops, cancels)
		}
	})
	t.Run("check-waits-both", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
		defer func() {
			for _, g := range gates {
				select {
				case <-g:
				default:
					close(g)
				}
			}
		}()
		started := make(chan int, 2)
		workers := make(chan struct{}, 2)
		rt := defaultAdaptiveRuntime()
		rt.run = run
		rt.timeout = func(c context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			if d != 5*time.Second {
				t.Error(d)
			}
			return context.WithCancel(c)
		}
		rt.spawn = func(f func()) { go func() { defer func() { workers <- struct{}{} }(); f() }() }
		rt.hooks = func(q adaptiveServiceRequest) map[string]func(adaptiveServiceEffect) adaptiveServiceReply {
			if q.Op == "check-worker" {
				return map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"clock-start": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{} }, "elapsed": func(adaptiveServiceEffect) adaptiveServiceReply {
					n := 600 * time.Millisecond
					if q.Index == 1 {
						n = 100 * time.Millisecond
					}
					return adaptiveServiceReply{Value: int64(n)}
				}}
			}
			return nil
		}
		r := &adaptiveDNS{ctx: ctx, runtime: rt}
		r.client = &http.Client{Transport: dnsIndependentRT(func(req *http.Request) (*http.Response, error) {
			i := 0
			if req.URL.Host == "8.8.8.8" {
				i = 1
			}
			started <- i
			<-gates[i]
			raw, _ := io.ReadAll(req.Body)
			var q dns.Msg
			_ = q.Unpack(raw)
			a := new(dns.Msg).SetReply(&q)
			raw, _ = a.Pack()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})}
		e := newAdaptiveEffects(r, nil, adaptiveServiceRequest{Op: "check"})
		done := make(chan adaptiveServiceResult, 1)
		go func() { done <- run(e.q, e) }()
		for i := 0; i < 2; i++ {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("check workers not started")
			}
		}
		close(gates[1])
		select {
		case <-workers:
		case <-time.After(5 * time.Second):
			t.Fatal("backup failed")
		}
		select {
		case <-done:
			t.Fatal("check returned before both completions")
		default:
		}
		if r.preferred.Load() != 0 {
			t.Fatal("switched before both")
		}
		close(gates[0])
		select {
		case res := <-done:
			if res.Err != nil {
				t.Fatal(res.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("check blocked")
		}
		select {
		case <-workers:
		case <-time.After(5 * time.Second):
			t.Fatal("worker leak")
		}
		if r.preferred.Load() != 1 || cap(e.done) != 2 {
			t.Fatal("check selection/capacity")
		}
	})
}
func TestAdaptiveIndependentSelectedChannels(t *testing.T) {
	adaptiveIndependentChannels(t, runSelectedAdaptiveService)
}
func adaptiveIndependentMonitor(t *testing.T, run adaptiveIndependentRunner) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	entered := make(chan struct{})
	seen := make(chan struct{}, 1)
	done := make(chan adaptiveServiceResult, 1)
	stops := 0
	rt := defaultAdaptiveRuntime()
	rt.run = run
	rt.ticker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != time.Minute {
			t.Error("monitor cadence", d)
		}
		close(entered)
		return ticks, func() { stops++ }
	}
	r := &adaptiveDNS{ctx: ctx, runtime: rt}
	e := newAdaptiveEffects(r, nil, adaptiveServiceRequest{Op: "monitor"})
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"check-child": func(adaptiveServiceEffect) adaptiveServiceReply { seen <- struct{}{}; return adaptiveServiceReply{} }}
	go func() { done <- run(e.q, e) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ticker setup")
	}
	select {
	case <-seen:
		t.Fatal("initial check added")
	default:
	}
	select {
	case ticks <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("tick stuck")
	}
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("child absent")
	}
	cancel()
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor leaked")
	}
	if stops != 1 {
		t.Fatal("ticker close", stops)
	}
	e.Release()
	if stops != 1 {
		t.Fatal("ticker double close")
	}
}
func TestAdaptiveIndependentSelectedMonitor(t *testing.T) {
	adaptiveIndependentMonitor(t, runSelectedAdaptiveService)
}
