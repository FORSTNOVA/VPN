package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var embeddedKernelRecoveries atomic.Uint64
var embeddedKernelRecoveryReason atomic.Value

type embeddedKernelMirrorEvent struct {
	Effect embeddedKernelEffect
	Result embeddedKernelResult
	Done   bool
}
type embeddedKernelMirror struct {
	events       chan embeddedKernelMirrorEvent
	replies      chan embeddedKernelReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *embeddedKernelMirror) Call(q embeddedKernelEffect) embeddedKernelReply {
	select {
	case m.events <- embeddedKernelMirrorEvent{Effect: q}:
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
func (m *embeddedKernelMirror) Release() {}
func (m *embeddedKernelMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newEmbeddedKernelMirror(q embeddedKernelRequest) *embeddedKernelMirror {
	m := &embeddedKernelMirror{events: make(chan embeddedKernelMirrorEvent), replies: make(chan embeddedKernelReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoEmbeddedKernel(q, m)
		select {
		case m.events <- embeddedKernelMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustEmbeddedKernelWithStart(q embeddedKernelRequest, fx embeddedKernelEffects, start embeddedKernelStart) (out embeddedKernelResult, used bool) {
	s, err := start(embeddedKernelInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newEmbeddedKernelMirror(q)
	defer m.close()
	var pending *embeddedKernelMirrorEvent
	fail := func(reason string) (embeddedKernelResult, bool) {
		embeddedKernelRecoveries.Add(1)
		embeddedKernelRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; q.Op == "logs" || i < 100000; i++ {
			var e embeddedKernelMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return embeddedKernelResult{Err: errors.New("embedded kernel oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return embeddedKernelResult{Err: errors.New("embedded kernel oracle closed")}, true
			}
		}
		return embeddedKernelResult{Err: errors.New("embedded kernel continuation limit")}, true
	}
	var step uint64
	for i := 0; q.Op == "logs" || i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := embeddedKernelFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e embeddedKernelMirrorEvent
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
			want, err := json.Marshal(embeddedKernelResultJSON(e.Result))
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
		wire := embeddedKernelReplyBytes(r)
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return embeddedKernelResult{Err: errors.New("embedded kernel oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
