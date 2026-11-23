package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var tunReportRecoveries atomic.Uint64
var tunReportRecoveryReason atomic.Value

type tunReportMirrorEvent struct {
	Effect tunReportEffect
	Result tunReportResult
	Done   bool
}
type tunReportMirror struct {
	events       chan tunReportMirrorEvent
	replies      chan tunReportReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *tunReportMirror) Call(q tunReportEffect) tunReportReply {
	select {
	case m.events <- tunReportMirrorEvent{Effect: q}:
	case <-m.cancel:
		panic("cancelled")
	}
	select {
	case r := <-m.replies:
		return r
	case <-m.cancel:
		panic("cancelled")
	}
}
func (m *tunReportMirror) Release() {}
func (m *tunReportMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newTunReportMirror(q tunReportRequest) *tunReportMirror {
	m := &tunReportMirror{events: make(chan tunReportMirrorEvent), replies: make(chan tunReportReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoTunReport(q, m)
		select {
		case m.events <- tunReportMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustTunReportWithStart(q tunReportRequest, fx tunReportEffects, start tunReportStart) (out tunReportResult, used bool) {
	s, err := start(tunReportInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newTunReportMirror(q)
	defer m.close()
	var pending *tunReportMirrorEvent
	fail := func(reason string) (tunReportResult, bool) {
		tunReportRecoveries.Add(1)
		tunReportRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; i < 100000; i++ {
			var e tunReportMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return tunReportResult{Err: errors.New("tunnel reporting oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return tunReportResult{Err: errors.New("tunnel reporting oracle closed")}, true
			}
		}
		return tunReportResult{Err: errors.New("tunnel reporting continuation limit")}, true
	}
	var step uint64
	for i := 0; i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, data, err := tunReportFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e tunReportMirrorEvent
		select {
		case e = <-m.events:
		case <-m.done:
			return fail("oracle ended")
		}
		pending = &e
		if k == "result" {
			if !e.Done {
				return fail("early done")
			}
			want, err := json.Marshal(e.Result.Data)
			if err != nil {
				return fail("oracle json")
			}
			var a, b any
			if json.Unmarshal(want, &a) != nil || json.Unmarshal([]byte(data), &b) != nil || !reflect.DeepEqual(a, b) {
				return fail("result mismatch")
			}
			return e.Result, true
		}
		if e.Done || e.Effect.Kind != k || e.Effect.Text != t {
			return fail("effect mismatch")
		}
		pending = nil
		used = true
		r := fx.Call(e.Effect)
		wire := tunReportReplyBytes(r)
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return tunReportResult{Err: errors.New("tunnel reporting oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
