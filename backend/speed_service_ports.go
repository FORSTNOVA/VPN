package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type speedRuntime struct {
	client  func(string, time.Duration) *http.Client
	context func(time.Duration) (context.Context, context.CancelFunc)
	now     func() time.Time
	since   func(time.Time) time.Duration
	run     func(speedServiceRequest, speedServiceEffects) speedServiceResult
	hooks   func(speedServiceRequest) map[string]func(speedServiceEffect) speedServiceReply
}

func defaultSpeedRuntime() *speedRuntime {
	return &speedRuntime{client: diagnosableClient, context: func(d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), d)
	}, now: time.Now, since: time.Since, run: runSelectedSpeedService}
}

type speedBatch struct {
	client         *http.Client
	ctx            context.Context
	cancel         context.CancelFunc
	total, started atomic.Int64
	pending        sync.WaitGroup
	mu             sync.Mutex
	failure        any
	joined         bool
}
type appSpeedEffects struct {
	a                           *app
	q                           speedServiceRequest
	w                           http.ResponseWriter
	runtime                     *speedRuntime
	hooks                       map[string]func(speedServiceEffect) speedServiceReply
	owned, temporary, responded bool
	writerPanic                 any
	proxy                       string
	batch                       *speedBatch
	batchOwned                  bool
	request                     *http.Request
	body                        io.ReadCloser
	buffer                      []byte
	zero                        *zeroReader
	target                      []byte
}

func newAppSpeedEffects(a *app, q speedServiceRequest, w http.ResponseWriter) *appSpeedEffects {
	return newSpeedEffects(a, q, w, defaultSpeedRuntime())
}
func newSpeedEffects(a *app, q speedServiceRequest, w http.ResponseWriter, rt *speedRuntime) *appSpeedEffects {
	e := &appSpeedEffects{a: a, q: q, w: w, runtime: rt}
	if rt.hooks != nil {
		e.hooks = rt.hooks(q)
	}
	return e
}
func (e *appSpeedEffects) join() {
	b := e.batch
	if b == nil || b.joined {
		return
	}
	b.pending.Wait()
	b.joined = true
	if b.failure != nil {
		panic(b.failure)
	}
}
func (e *appSpeedEffects) closeBody() {
	b := e.body
	e.body = nil
	if b != nil {
		_ = b.Close()
	}
}
func (e *appSpeedEffects) finishBatch() {
	if !e.batchOwned {
		return
	}
	e.batchOwned = false
	defer e.join()
	if b := e.batch; b != nil && b.cancel != nil {
		c := b.cancel
		b.cancel = nil
		c()
	}
}
func (e *appSpeedEffects) Release() {
	defer func() {
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	}()
	defer func() {
		if e.temporary {
			e.Call(speedServiceEffect{Kind: "stop"})
		}
	}()
	defer e.closeBody()
	e.finishBatch()
}
func (e *appSpeedEffects) Call(q speedServiceEffect) (r speedServiceReply) {
	if q.Kind == "response" {
		defer func() {
			if v := recover(); v != nil {
				e.writerPanic = v
				panic(speedResponsePanic{Value: v})
			}
		}()
	}
	if q.Kind == "stop" {
		if !e.temporary {
			return
		}
		e.temporary = false
	}
	if f := e.hooks[q.Kind]; f != nil {
		if q.Kind == "response" {
			e.responded = true
		}
		r = f(q)
		if q.Kind == "ensure" && r.Err == nil && r.Flag {
			e.temporary = true
		}
		return
	}
	switch q.Kind {
	case "lock":
		e.a.mu.Lock()
		e.owned = true
	case "unlock":
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	case "ensure":
		r.Flag, r.Err = e.a.ensureKernelForCheckLocked()
		if r.Err == nil && r.Flag {
			e.temporary = true
		}
	case "active":
		r.Text = e.a.activeNodeLocked()
		r.Flag = speedRejectedNode(r.Text)
	case "endpoint":
		e.proxy = net.JoinHostPort("127.0.0.1", strconv.Itoa(e.a.mixedPort))
		r.Text = e.proxy
	case "stop":
		r.Err = e.a.stopMihomoLocked()
	case "measure-begin":
		b := &speedBatch{}
		e.batch = b
		e.batchOwned = true
		b.client = e.runtime.client(e.proxy, q.Budget+5*time.Second)
		b.ctx, b.cancel = e.runtime.context(q.Budget)
	case "batch-start":
		b := e.batch
		for i := 0; i < q.Count; i++ {
			b.pending.Add(1)
			go func() {
				defer b.pending.Done()
				defer func() {
					if v := recover(); v != nil {
						b.mu.Lock()
						if b.failure == nil {
							b.failure = v
						}
						b.mu.Unlock()
					}
				}()
				op := "down-stream"
				if q.Upload {
					op = "up-stream"
				}
				request := speedServiceRequest{Op: op, Allowance: q.Bytes, Budget: q.Budget}
				child := newSpeedEffects(nil, request, nil, e.runtime)
				child.batch = b
				defer child.Release()
				result := e.runtime.run(request, child)
				if result.Err != nil {
					panic(result.Err)
				}
			}()
		}
	case "batch-join":
		e.join()
	case "totals":
		r.Started = e.batch.started.Load()
		if r.Started != 0 {
			r.Bytes = e.batch.total.Load()
		}
	case "total-load":
		r.Bytes = e.batch.total.Load()
	case "elapsed":
		r.Elapsed = e.runtime.since(time.Unix(0, q.Bytes))
	case "measure-end":
		e.finishBatch()
	case "request-new":
		if q.Upload {
			source := &zeroReader{remaining: q.Bytes, started: &e.batch.started, total: &e.batch.total, runtime: e.runtime, batch: e.batch}
			e.request, r.Err = http.NewRequestWithContext(e.batch.ctx, http.MethodPost, speedTestOrigin+"/__up", source)
			if r.Err == nil {
				e.request.ContentLength = q.Bytes
				e.request.Header.Set("Content-Type", "application/octet-stream")
			}
		} else {
			e.request, r.Err = http.NewRequestWithContext(e.batch.ctx, http.MethodGet, fmt.Sprintf("%s/__down?bytes=%d", speedTestOrigin, q.Bytes), nil)
		}
	case "request-do":
		var response *http.Response
		response, r.Err = e.batch.client.Do(e.request)
		if r.Err == nil {
			e.body = response.Body
		}
	case "body-read":
		if e.buffer == nil {
			e.buffer = make([]byte, 64*1024)
		}
		var n int
		n, r.Err = e.body.Read(e.buffer)
		r.Bytes = int64(n)
	case "download-count":
		if e.batch.total.Load() == 0 {
			e.batch.started.CompareAndSwap(0, e.runtime.now().UnixNano())
		}
		e.batch.total.Add(q.Bytes)
	case "body-close":
		e.closeBody()
	case "upload-drain":
		_, _ = io.Copy(io.Discard, io.LimitReader(e.body, 4096))
	case "zero-fill":
		for i := int64(0); i < q.Bytes; i++ {
			e.target[i] = 0
		}
	case "zero-remaining":
		e.zero.remaining = q.Bytes
	case "zero-count":
		e.zero.started.CompareAndSwap(0, e.runtime.now().UnixNano())
		e.zero.total.Add(q.Bytes)
	case "rate":
		r.Rate = throughputMbps(q.Bytes, q.Budget)
	case "response":
		e.responded = true
		if q.Status != 200 {
			http.Error(e.w, q.Text, q.Status)
		} else {
			writeJSON(e.w, map[string]any{"node": q.Node, "temporary": q.Temporary, "speed": q.Result})
		}
	default:
		panic("unknown speed effect: " + q.Kind)
	}
	return
}
func runSpeedServiceWithEffects(q speedServiceRequest, e *appSpeedEffects) (r speedServiceResult) {
	defer func() {
		v := recover()
		if e.writerPanic != nil {
			panic(e.writerPanic)
		}
		if v != nil {
			r.Err = fmt.Errorf("speed service interrupted: %v", v)
			r.Status = 500
			if e.w != nil && !e.responded {
				e.Call(speedServiceEffect{Kind: "response", Status: 500, Text: "speed test unavailable"})
			}
		}
	}()
	defer e.Release()
	r = e.runtime.run(q, e)
	if r.Err != nil && r.Status == 0 {
		r.Status = 500
	}
	if e.w != nil && !e.responded && r.Err != nil {
		e.Call(speedServiceEffect{Kind: "response", Status: 500, Text: "speed test unavailable"})
	}
	return
}
