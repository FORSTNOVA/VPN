package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var metadataProjectionRecoveries atomic.Uint64
var metadataProjectionRecoveryReason atomic.Value

type metadataProjectionMirrorEvent struct {
	Effect metadataProjectionEffect
	Result metadataProjectionResult
	Done   bool
}
type metadataProjectionMirror struct {
	events       chan metadataProjectionMirrorEvent
	replies      chan metadataProjectionReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *metadataProjectionMirror) Call(q metadataProjectionEffect) metadataProjectionReply {
	select {
	case m.events <- metadataProjectionMirrorEvent{Effect: q}:
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
func (m *metadataProjectionMirror) Release() {}
func (m *metadataProjectionMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newMetadataProjectionMirror(q metadataProjectionRequest) *metadataProjectionMirror {
	m := &metadataProjectionMirror{events: make(chan metadataProjectionMirrorEvent), replies: make(chan metadataProjectionReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoMetadataProjection(q, m)
		select {
		case m.events <- metadataProjectionMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustMetadataProjectionWithStart(q metadataProjectionRequest, fx metadataProjectionEffects, start metadataProjectionStart) (out metadataProjectionResult, used bool) {
	s, err := start(metadataProjectionInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newMetadataProjectionMirror(q)
	defer m.close()
	var pending *metadataProjectionMirrorEvent
	fail := func(reason string) (metadataProjectionResult, bool) {
		metadataProjectionRecoveries.Add(1)
		metadataProjectionRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for {
			var e metadataProjectionMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return metadataProjectionResult{Err: errors.New("metadata projection oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return metadataProjectionResult{Err: errors.New("metadata projection oracle closed")}, true
			}
		}

	}
	var step uint64
	for {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := metadataProjectionFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e metadataProjectionMirrorEvent
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
			want, err := json.Marshal(metadataProjectionResultJSON(e.Result))
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
		wire := metadataProjectionReplyBytes(metadataProjectionWireReply(r))
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return metadataProjectionResult{Err: errors.New("metadata projection oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}

}
