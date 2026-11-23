package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type speedFakeRT func(*http.Request) (*http.Response, error)

func (f speedFakeRT) RoundTrip(q *http.Request) (*http.Response, error) { return f(q) }

type speedFakeBody struct {
	read   func([]byte) (int, error)
	closed *atomic.Int64
}

func (b *speedFakeBody) Read(p []byte) (int, error) { return b.read(p) }
func (b *speedFakeBody) Close() error               { b.closed.Add(1); return nil }
func speedTestRuntime(t *testing.T, do speedFakeRT) (*speedRuntime, *atomic.Int64) {
	t.Helper()
	cancels := &atomic.Int64{}
	rt := defaultSpeedRuntime()
	rt.client = func(_ string, d time.Duration) *http.Client {
		if d != speedTestSeconds+5*time.Second {
			t.Errorf("timeout %v", d)
		}
		return &http.Client{Transport: do}
	}
	rt.context = func(d time.Duration) (context.Context, context.CancelFunc) {
		if d != speedTestSeconds {
			t.Errorf("budget %v", d)
		}
		ctx, cancel := context.WithCancel(context.Background())
		return ctx, func() { cancels.Add(1); cancel() }
	}
	rt.now = func() time.Time { return time.Unix(0, 100) }
	rt.since = func(v time.Time) time.Duration {
		if v.UnixNano() != 100 {
			t.Errorf("started %v", v)
		}
		return time.Second
	}
	return rt, cancels
}
func TestSpeedServiceSafeDownloadConcurrency(t *testing.T) {
	var arrived atomic.Int64
	var closed atomic.Int64
	barrier := make(chan struct{})
	rt, cancels := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Errorf("%s", r.Method)
		}
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func(p []byte) (int, error) {
			if len(p) != 64*1024 {
				t.Errorf("buffer %d", len(p))
			}
			if arrived.Add(1) == 4 {
				close(barrier)
			}
			<-barrier
			return 3, io.EOF
		}}}, nil
	})
	q := speedServiceRequest{Op: "download", Allowance: 1, Budget: speedTestSeconds}
	e := newSpeedEffects(nil, q, nil, rt)
	r := runSpeedServiceWithEffects(q, e)
	if r.Err != nil || r.Bytes != 12 || r.Elapsed != time.Second || closed.Load() != 4 || cancels.Load() != 1 {
		t.Fatalf("%+v close=%d cancel=%d", r, closed.Load(), cancels.Load())
	}
	e.Release()
	if cancels.Load() != 1 {
		t.Fatal("repeated cancel")
	}
}
func TestSpeedServiceSafeUploadPartialError(t *testing.T) {
	var arrived atomic.Int64
	barrier := make(chan struct{})
	rt, cancels := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.ContentLength != 5 || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("upload headers")
		}
		if arrived.Add(1) == 2 {
			close(barrier)
		}
		<-barrier
		b, e := io.ReadAll(r.Body)
		if e != nil || len(b) != 5 {
			t.Errorf("source %d %v", len(b), e)
		}
		for _, v := range b {
			if v != 0 {
				t.Error("nonzero payload")
			}
		}
		return nil, errors.New("transport after body")
	})
	q := speedServiceRequest{Op: "upload", Allowance: 11, Budget: speedTestSeconds}
	r := runSpeedServiceWithEffects(q, newSpeedEffects(nil, q, nil, rt))
	if r.Err != nil || r.Bytes != 10 || cancels.Load() != 1 {
		t.Fatalf("%+v cancel%d", r, cancels.Load())
	}
}
func TestSpeedServiceSafeZeroLengthAndEOF(t *testing.T) {
	var started, total atomic.Int64
	rt := defaultSpeedRuntime()
	calls := 0
	rt.now = func() time.Time { calls++; return time.Unix(0, 100) }
	r := &zeroReader{remaining: 2, started: &started, total: &total, runtime: rt}
	if n, e := r.Read(nil); n != 0 || e != nil || started.Load() != 100 || total.Load() != 0 || calls != 1 {
		t.Fatal("zero buffer semantics")
	}
	buf := make([]byte, 4)
	if n, e := r.Read(buf); n != 2 || e != nil || total.Load() != 2 || calls != 2 {
		t.Fatal("short read")
	}
	if n, e := r.Read(buf); n != 0 || e != io.EOF || calls != 2 {
		t.Fatal("EOF clock")
	}
}

type speedPanicWriter struct{ calls int }

func (w *speedPanicWriter) Header() http.Header       { return make(http.Header) }
func (w *speedPanicWriter) WriteHeader(int)           { w.calls++; panic("speed writer") }
func (w *speedPanicWriter) Write([]byte) (int, error) { w.calls++; panic("speed writer") }
func TestSpeedServiceSafeResponsePanicCleanup(t *testing.T) {
	a := &app{}
	rt := defaultSpeedRuntime()
	stops := 0
	rt.hooks = func(speedServiceRequest) map[string]func(speedServiceEffect) speedServiceReply {
		return map[string]func(speedServiceEffect) speedServiceReply{"ensure": func(speedServiceEffect) speedServiceReply { return speedServiceReply{Flag: true} }, "active": func(speedServiceEffect) speedServiceReply { return speedServiceReply{Flag: true} }, "stop": func(speedServiceEffect) speedServiceReply {
			stops++
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Error("stop outside lock")
			}
			return speedServiceReply{}
		}}
	}
	q := speedServiceRequest{Op: "handler"}
	w := &speedPanicWriter{}
	e := newSpeedEffects(a, q, w, rt)
	defer func() {
		if recover() == nil || w.calls != 1 || stops != 1 {
			t.Errorf("panic/write/stop %d %d", w.calls, stops)
		}
		if !a.mu.TryLock() {
			t.Fatal("mutex leaked")
		}
		a.mu.Unlock()
		e.Release()
		if stops != 1 {
			t.Fatal("stop replay")
		}
	}()
	_ = runSpeedServiceWithEffects(q, e)
}
func TestSpeedServiceSafeWorkerPanicJoins(t *testing.T) {
	var closed atomic.Int64
	var count atomic.Int64
	barrier := make(chan struct{})
	var once sync.Once
	rt, cancels := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func([]byte) (int, error) {
			if count.Add(1) == 4 {
				once.Do(func() { close(barrier) })
			}
			<-barrier
			panic("reader panic")
		}}}, nil
	})
	q := speedServiceRequest{Op: "download", Allowance: 10, Budget: speedTestSeconds}
	r := runSpeedServiceWithEffects(q, newSpeedEffects(nil, q, nil, rt))
	if r.Err == nil || closed.Load() != 4 || cancels.Load() != 1 {
		t.Fatalf("%v closes%d cancel%d", r.Err, closed.Load(), cancels.Load())
	}
}

type speedAfterEffectPanic struct {
	speedServiceEffects
	kind string
}

func (f speedAfterEffectPanic) Call(q speedServiceEffect) speedServiceReply {
	r := f.speedServiceEffects.Call(q)
	if q.Kind == f.kind {
		panic("after " + q.Kind)
	}
	return r
}
func TestSpeedServiceSafeStartedBatchFaultJoins(t *testing.T) {
	var closed atomic.Int64
	var calls atomic.Int64
	rt, cancels := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func([]byte) (int, error) { <-r.Context().Done(); return 0, r.Context().Err() }}}, nil
	})
	rt.run = func(q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
		if q.Op == "download" {
			return runGoSpeedService(q, speedAfterEffectPanic{fx, "batch-start"})
		}
		return runGoSpeedService(q, fx)
	}
	q := speedServiceRequest{Op: "download", Allowance: 10, Budget: speedTestSeconds}
	e := newSpeedEffects(nil, q, nil, rt)
	r := runSpeedServiceWithEffects(q, e)
	if r.Err == nil || cancels.Load() != 1 || !e.batch.joined {
		t.Fatalf("%v cancel%d joined%v", r.Err, cancels.Load(), e.batch.joined)
	}
	if calls.Load() != 4 || closed.Load() != 4 {
		t.Fatalf("workers escaped join: %d/%d", calls.Load(), closed.Load())
	}
	e.Release()
	if cancels.Load() != 1 || closed.Load() != 4 {
		t.Fatal("resource replay")
	}
}
