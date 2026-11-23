package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/miekg/dns"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"time"
)

type adaptiveResponsePanic struct{ Value any }
type adaptiveCompletion struct {
	answer  *dns.Msg
	index   int
	healthy bool
	elapsed int64
}
type adaptiveRuntime struct {
	hooks   func(adaptiveServiceRequest) map[string]func(adaptiveServiceEffect) adaptiveServiceReply
	run     func(adaptiveServiceRequest, adaptiveServiceEffects) adaptiveServiceResult
	clock   func() time.Time
	timer   func(time.Duration) (<-chan time.Time, func())
	ticker  func(time.Duration) (<-chan time.Time, func())
	timeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	spawn   func(func())
}

func defaultAdaptiveRuntime() *adaptiveRuntime {
	return &adaptiveRuntime{run: runSelectedAdaptiveService, clock: time.Now, timer: func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTimer(d)
		return t.C, func() { t.Stop() }
	}, ticker: func(d time.Duration) (<-chan time.Time, func()) { t := time.NewTicker(d); return t.C, t.Stop }, timeout: context.WithTimeout, spawn: func(f func()) { go f() }}
}

type appAdaptiveEffects struct {
	relayCancel, listenerClose, clientClose func()
	shutdown                                func() error
	q                                       adaptiveServiceRequest
	r                                       *adaptiveDNS
	a                                       *app
	rt                                      *adaptiveRuntime
	ctx                                     context.Context
	cancel                                  context.CancelFunc
	query, answer, unpacked                 *dns.Msg
	writer                                  dns.ResponseWriter
	request                                 *http.Request
	response                                *http.Response
	body                                    io.ReadCloser
	packet, raw                             []byte
	done                                    chan adaptiveCompletion
	timer, ticker                           <-chan time.Time
	stopTimer, stopTicker                   func()
	start                                   time.Time
	probeName, failed, reason               string
	now                                     time.Time
	lastErr                                 error
	newRelay                                bool
	ready                                   chan struct{}
	serverStarted                           bool
	hooks                                   map[string]func(adaptiveServiceEffect) adaptiveServiceReply
	writerPanic                             any
}

func newAdaptiveEffects(r *adaptiveDNS, a *app, q adaptiveServiceRequest) *appAdaptiveEffects {
	rt := defaultAdaptiveRuntime()
	if r != nil && r.runtime != nil {
		rt = r.runtime
	}
	e := &appAdaptiveEffects{r: r, a: a, q: q, rt: rt}
	if r != nil {
		e.ctx = r.ctx
	}
	e.relayCancel = func() {
		if e.r != nil && e.r.cancel != nil {
			e.r.cancel()
		}
	}
	e.listenerClose = func() {
		if e.r != nil && e.r.listener != nil {
			_ = e.r.listener.Close()
		}
	}
	e.clientClose = func() {
		if e.r != nil && e.r.client != nil {
			e.r.client.CloseIdleConnections()
		}
	}
	e.shutdown = func() error {
		if e.r != nil && e.r.server != nil {
			return e.r.server.Shutdown()
		}
		return nil
	}
	if rt.hooks != nil {
		e.hooks = rt.hooks(q)
	}
	return e
}
func (e *appAdaptiveEffects) closeBody() {
	if b := e.body; b != nil {
		e.body = nil
		_ = b.Close()
	}
}
func (e *appAdaptiveEffects) cancelContext() {
	if c := e.cancel; c != nil {
		e.cancel = nil
		c()
	}
}
func (e *appAdaptiveEffects) stopTimers() {
	if f := e.stopTimer; f != nil {
		e.stopTimer = nil
		f()
	}
	if f := e.stopTicker; f != nil {
		e.stopTicker = nil
		f()
	}
}
func (e *appAdaptiveEffects) Release() {
	defer e.cancelContext()
	defer e.stopTimers()
	defer e.closeBody()
	if !e.newRelay {
		return
	}
	e.newRelay = false
	defer e.listenerClose()
	defer e.clientClose()
	defer func() {
		if e.serverStarted {
			e.serverStarted = false
			_ = e.shutdown()
		}
	}()
	e.relayCancel()
}
func (e *appAdaptiveEffects) FinalizeAdaptiveResult(q adaptiveServiceRequest, r adaptiveServiceResult) adaptiveServiceResult {
	if q.Op == "create" && r.Err == nil {
		e.newRelay = false
	}
	return r
}
func (e *appAdaptiveEffects) child(q adaptiveServiceRequest, ctx context.Context, msg *dns.Msg) (adaptiveServiceResult, *dns.Msg) {
	c := newAdaptiveEffects(e.r, e.a, q)
	c.rt = e.rt
	c.ctx = ctx
	c.query = msg
	c.probeName = e.probeName
	result := runAdaptiveWithEffects(q, c)
	return result, c.answer
}
func (e *appAdaptiveEffects) Call(q adaptiveServiceEffect) (r adaptiveServiceReply) {
	if q.Kind == "answer-write" {
		defer func() {
			if p := recover(); p != nil {
				e.writerPanic = p
				panic(adaptiveResponsePanic{p})
			}
		}()
	}
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	switch q.Kind {
	case "pack":
		e.packet, r.Err = e.query.Pack()
	case "request":
		e.request, r.Err = http.NewRequestWithContext(e.ctx, http.MethodPost, "https://"+dohIPs[q.Index]+"/dns-query", bytes.NewReader(e.packet))
		if r.Err == nil {
			e.request.Header.Set("Content-Type", "application/dns-message")
			e.request.Header.Set("Accept", "application/dns-message")
		}
	case "do":
		e.response, r.Err = e.r.client.Do(e.request)
		if r.Err == nil {
			e.body = e.response.Body
			r.Code = e.response.StatusCode
		}
	case "body-read":
		e.raw, r.Err = io.ReadAll(io.LimitReader(e.body, 65536))
	case "unpack":
		e.unpacked = new(dns.Msg)
		r.Err = e.unpacked.Unpack(e.raw)
		if r.Err == nil {
			v := e.unpacked
			r.Value = int64(len(e.raw))
			r.Code = v.Rcode
			r.QCount = len(e.query.Question)
			r.ACount = len(v.Question)
			if v.Response {
				r.Bits |= 1
			}
			if v.Id == e.query.Id {
				r.Bits |= 2
			}
			if r.QCount == 1 && r.ACount == 1 && v.Question[0] == e.query.Question[0] {
				r.Bits |= 4
			}
		}
	case "answer-accept":
		e.answer = e.unpacked
	case "body-close":
		e.closeBody()
	case "failure":
		if q.Index == -1 {
			r.Err = errors.New("invalid or failed DoH response")
		} else {
			r.Err = fmt.Errorf("DoH HTTP %d", q.Index)
		}
	case "context":
		e.ctx, e.cancel = e.rt.timeout(e.r.ctx, q.Duration)
	case "context-cancel":
		e.cancelContext()
	case "completion-new":
		e.done = make(chan adaptiveCompletion, 2)
	case "preferred":
		r.Index = int(e.r.preferred.Load())
	case "resolve-start":
		relay, rt, ctx, msg, done, index := e.r, e.rt, e.ctx, e.query, e.done, q.Index
		e.rt.spawn(func() {
			c := newAdaptiveEffects(relay, nil, adaptiveServiceRequest{Op: "exchange", Index: index})
			c.rt = rt
			c.ctx = ctx
			c.query = msg.Copy()
			v := runAdaptiveWithEffects(c.q, c)
			if v.Err != nil {
				c.answer = nil
			}
			done <- adaptiveCompletion{answer: c.answer, index: index}
		})
	case "timer-new":
		e.timer, e.stopTimer = e.rt.timer(q.Duration)
	case "timer-stop":
		if f := e.stopTimer; f != nil {
			e.stopTimer = nil
			f()
		}
	case "head-wait":
		select {
		case v := <-e.done:
			e.answer = v.answer
			r.Text = "done"
			r.Flag = v.answer != nil
			r.Index = v.index
		case <-e.timer:
			r.Text = "timer"
		case <-e.ctx.Done():
			r.Text = "cancel"
		}
	case "resolve-wait":
		select {
		case v := <-e.done:
			e.answer = v.answer
			r.Text = "done"
			r.Flag = v.answer != nil
			r.Index = v.index
		case <-e.ctx.Done():
			r.Text = "cancel"
		}
	case "cas":
		r.Flag = e.r.preferred.CompareAndSwap(int32(q.Index), int32(q.Count))
	case "switch-log":
		log.Printf("DNS upstream switched from %s to %s", dohIPs[q.Index], dohIPs[q.Count])
	case "answer-failure":
		e.answer = new(dns.Msg).SetRcode(e.query, dns.RcodeServerFailure)
	case "answer-write":
		_ = e.writer.WriteMsg(e.answer)
	case "ticker-new":
		e.ticker, e.stopTicker = e.rt.ticker(q.Duration)
	case "ticker-stop":
		if f := e.stopTicker; f != nil {
			e.stopTicker = nil
			f()
		}
	case "monitor-wait":
		select {
		case <-e.r.ctx.Done():
		case <-e.ticker:
			r.Flag = true
		}
	case "check-child":
		v, _ := e.child(adaptiveServiceRequest{Op: "check"}, e.r.ctx, nil)
		r.Err = v.Err
		if r.Err != nil {
			panic(r.Err)
		}
	case "probe-name":
		e.probeName = dns.Fqdn(probeDomain())
	case "check-start":
		relay, rt, ctx, done, index, name := e.r, e.rt, e.ctx, e.done, q.Index, e.probeName
		e.rt.spawn(func() {
			c := newAdaptiveEffects(relay, nil, adaptiveServiceRequest{Op: "check-worker", Index: index})
			c.rt = rt
			c.ctx = ctx
			c.probeName = name
			v := runAdaptiveWithEffects(c.q, c)
			done <- adaptiveCompletion{index: index, healthy: v.Err == nil && v.Bool, elapsed: v.Value}
		})
	case "check-wait":
		v := <-e.done
		r.Index = v.index
		r.Flag = v.healthy
		r.Value = v.elapsed
	case "clock-start":
		e.start = e.rt.clock()
	case "probe-question":
		e.query = new(dns.Msg).SetQuestion(e.probeName, dns.TypeTXT)
	case "exchange-child":
		v, ans := e.child(adaptiveServiceRequest{Op: "exchange", Index: e.q.Index}, e.ctx, e.query)
		r.Err = v.Err
		e.answer = ans
	case "elapsed":
		r.Value = int64(e.rt.clock().Sub(e.start))
	case "custom-present":
		r.Flag = e.a.confirmProbe != nil
	case "custom-confirm":
		r.Flag = e.a.confirmProbe()
	case "blocked":
		r.Flag = e.a.healthMetaLocked().Blocked
	case "running":
		r.Flag = e.a.kernelRunningLocked()
	case "probe-config":
		r.Index = e.a.mixedPort
		r.Flag = runtime.GOOS == "android"
		r.Count = len(confirmProbeTargets)
	case "site":
		t := confirmProbeTargets[q.Index]
		p := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(e.a.mixedPort))}
		r.Text = checkSiteWithin(p, t.name, t.url, q.Duration).State
	case "note-present":
		r.Flag = e.a.healthMetaLocked().SweepNote != ""
	case "anomaly-log":
		log.Printf("the locked region measured as exhausted while a request through %s still got through; not blocking", e.failed)
	case "note-anomaly":
		e.a.setHealthNoteLocked(adaptiveAnomalyNote)
	case "reject":
		r.Err = e.a.switchGroupLocked(blockedChoice)
	case "reject-log":
		log.Printf("could not block traffic: %v", e.lastErr)
	case "set-blocked":
		e.a.setHealthBlockedLocked(true)
	case "set-reason":
		e.a.setHealthReasonLocked(e.reason)
	case "set-switch-time":
		e.a.setHealthLastSwitchLocked(e.now)
	case "clear-note":
		e.a.setHealthNoteLocked("")
	case "event":
		e.a.recordSwitchLocked(switchEvent{At: e.now, Group: "SmartVPN", FromNode: e.failed, ToNode: blockedChoice, Trigger: "region_exhausted", Evidence: e.reason})
	case "listen":
		var l net.Listener
		l, r.Err = net.Listen("tcp4", "127.0.0.1:0")
		if r.Err == nil {
			e.r.listener = &lifecycleListener{Listener: l}
			e.newRelay = true
		}
	case "relay-context":
		e.r.ctx, e.r.cancel = context.WithCancel(context.Background())
	case "client":
		p := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", e.q.Index)}
		e.r.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(p), ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	case "server":
		e.ready = make(chan struct{})
		e.r.server = &dns.Server{Listener: e.r.listener, Handler: dns.HandlerFunc(e.r.serve), NotifyStartedFunc: func() { close(e.ready) }}
	case "server-start":
		e.serverStarted = true
		e.rt.spawn(func() { _ = e.r.server.ActivateAndServe() })
	case "server-ready":
		<-e.ready
	case "relay-cancel":
		e.relayCancel()
	case "server-shutdown":
		_ = e.shutdown()
	case "idle-close":
		e.clientClose()
	default:
		panic("unknown adaptive effect " + q.Kind)
	}
	if r.Err != nil {
		e.lastErr = r.Err
	}
	return
}
func runAdaptiveWithEffects(q adaptiveServiceRequest, e *appAdaptiveEffects) (out adaptiveServiceResult) {
	defer func() {
		if p := recover(); p != nil {
			if e.writerPanic != nil {
				panic(e.writerPanic)
			}
			if m, ok := p.(adaptiveResponsePanic); ok {
				panic(m.Value)
			}
			out.Err = fmt.Errorf("adaptive service interrupted: %v", p)
		}
	}()
	defer e.Release()
	run := e.rt.run
	if run == nil {
		run = runSelectedAdaptiveService
	}
	return run(q, e)
}
