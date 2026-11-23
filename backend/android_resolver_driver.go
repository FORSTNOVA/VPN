package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var androidResolverRecoveries atomic.Uint64
var androidResolverRecoveryReason atomic.Value

type androidResolverMirrorEvent struct {
	Effect androidResolverEffect
	Result androidResolverResult
	Done   bool
}
type androidResolverMirror struct {
	events       chan androidResolverMirrorEvent
	replies      chan androidResolverReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *androidResolverMirror) Call(q androidResolverEffect) androidResolverReply {
	select {
	case m.events <- androidResolverMirrorEvent{Effect: q}:
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
func (m *androidResolverMirror) Release() {}
func (m *androidResolverMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newAndroidResolverMirror(q androidResolverRequest) *androidResolverMirror {
	m := &androidResolverMirror{events: make(chan androidResolverMirrorEvent), replies: make(chan androidResolverReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoAndroidResolver(q, m)
		select {
		case m.events <- androidResolverMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustAndroidResolverWithStart(q androidResolverRequest, fx androidResolverEffects, start androidResolverStart) (out androidResolverResult, used bool) {
	s, err := start(androidResolverInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newAndroidResolverMirror(q)
	defer m.close()
	var pending *androidResolverMirrorEvent
	fail := func(reason string) (androidResolverResult, bool) {
		androidResolverRecoveries.Add(1)
		androidResolverRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; i < 100000; i++ {
			var e androidResolverMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return androidResolverResult{Err: errors.New("Android resolver oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return androidResolverResult{Err: errors.New("Android resolver oracle closed")}, true
			}
		}
		return androidResolverResult{Err: errors.New("Android resolver continuation limit")}, true
	}
	var step uint64
	for i := 0; i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := androidResolverFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e androidResolverMirrorEvent
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
			want, err := json.Marshal(androidResolverResultJSON(e.Result))
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
		wire := androidResolverReplyBytes(r)
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return androidResolverResult{Err: errors.New("Android resolver oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
