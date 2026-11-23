package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var nodeFrontRecoveries atomic.Uint64
var nodeFrontRecoveryReason atomic.Value

type nodeFrontMirrorEvent struct {
	Effect nodeFrontEffect
	Result nodeFrontResult
	Done   bool
}
type nodeFrontMirror struct {
	events       chan nodeFrontMirrorEvent
	replies      chan nodeFrontReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *nodeFrontMirror) Call(q nodeFrontEffect) nodeFrontReply {
	select {
	case m.events <- nodeFrontMirrorEvent{Effect: q}:
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
func (m *nodeFrontMirror) Release() {}
func (m *nodeFrontMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newNodeFrontMirror(q nodeFrontRequest) *nodeFrontMirror {
	m := &nodeFrontMirror{events: make(chan nodeFrontMirrorEvent), replies: make(chan nodeFrontReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoNodeFront(q, m)
		select {
		case m.events <- nodeFrontMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustNodeFrontWithStart(q nodeFrontRequest, fx nodeFrontEffects, start nodeFrontStart) (out nodeFrontResult, used bool) {
	s, err := start(nodeFrontInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newNodeFrontMirror(q)
	defer m.close()
	var pending *nodeFrontMirrorEvent
	fail := func(reason string) (nodeFrontResult, bool) {
		nodeFrontRecoveries.Add(1)
		nodeFrontRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; i < 100000; i++ {
			var e nodeFrontMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return nodeFrontResult{Err: errors.New("node frontend oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return nodeFrontResult{Err: errors.New("node frontend oracle closed")}, true
			}
		}
		return nodeFrontResult{Err: errors.New("node frontend continuation limit")}, true
	}
	var step uint64
	for i := 0; i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := nodeFrontFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e nodeFrontMirrorEvent
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
			want, err := json.Marshal(nodeFrontResultJSON(e.Result))
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
		wire := nodeFrontReplyBytes(nodeFrontWireReply(e.Effect.Kind, r))
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return nodeFrontResult{Err: errors.New("node frontend oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
