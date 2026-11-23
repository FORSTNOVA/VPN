package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var privacyLogRecoveries atomic.Uint64
var privacyLogRecoveryReason atomic.Value

type privacyLogMirrorEvent struct {
	Effect privacyLogEffect
	Result privacyLogResult
	Done   bool
}
type privacyLogMirror struct {
	events       chan privacyLogMirrorEvent
	replies      chan privacyLogReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *privacyLogMirror) Call(q privacyLogEffect) privacyLogReply {
	select {
	case m.events <- privacyLogMirrorEvent{Effect: q}:
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
func (m *privacyLogMirror) Release() {}
func (m *privacyLogMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newPrivacyLogMirror(q privacyLogRequest) *privacyLogMirror {
	m := &privacyLogMirror{events: make(chan privacyLogMirrorEvent), replies: make(chan privacyLogReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoPrivacyLog(q, m)
		select {
		case m.events <- privacyLogMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustPrivacyLogWithStart(q privacyLogRequest, fx privacyLogEffects, start privacyLogStart) (out privacyLogResult, used bool) {
	s, err := start(privacyLogInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newPrivacyLogMirror(q)
	defer m.close()
	var pending *privacyLogMirrorEvent
	fail := func(reason string) (privacyLogResult, bool) {
		privacyLogRecoveries.Add(1)
		privacyLogRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; i < 100000; i++ {
			var e privacyLogMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return privacyLogResult{Err: errors.New("privacy log oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return privacyLogResult{Err: errors.New("privacy log oracle closed")}, true
			}
		}
		return privacyLogResult{Err: errors.New("privacy log continuation limit")}, true
	}
	var step uint64
	for i := 0; i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := privacyLogFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e privacyLogMirrorEvent
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
			want, err := json.Marshal(privacyLogResultJSON(e.Result))
			if err != nil {
				return fail("oracle json")
			}
			var a, b any
			if json.Unmarshal(want, &a) != nil || json.Unmarshal([]byte(data), &b) != nil || !reflect.DeepEqual(a, b) {
				return fail("result mismatch")
			}
			return e.Result, true
		}
		if e.Done || e.Effect.Kind != k || e.Effect.Text != t || e.Effect.Other != other || e.Effect.Number != number {
			return fail("effect mismatch")
		}
		pending = nil
		used = true
		r := fx.Call(e.Effect)
		wire := privacyLogReplyBytes(r)
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return privacyLogResult{Err: errors.New("privacy log oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
