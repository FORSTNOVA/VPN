package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var driverInstallRecoveries atomic.Uint64
var driverInstallRecoveryReason atomic.Value

type driverInstallMirrorEvent struct {
	Effect driverInstallEffect
	Result driverInstallResult
	Done   bool
}
type driverInstallMirror struct {
	events       chan driverInstallMirrorEvent
	replies      chan driverInstallReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *driverInstallMirror) Call(q driverInstallEffect) driverInstallReply {
	select {
	case m.events <- driverInstallMirrorEvent{Effect: q}:
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
func (m *driverInstallMirror) Release() {}
func (m *driverInstallMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newDriverInstallMirror(q driverInstallRequest) *driverInstallMirror {
	m := &driverInstallMirror{events: make(chan driverInstallMirrorEvent), replies: make(chan driverInstallReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoDriverInstall(q, m)
		select {
		case m.events <- driverInstallMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustDriverInstallWithStart(q driverInstallRequest, fx driverInstallEffects, start driverInstallStart) (out driverInstallResult, used bool) {
	s, err := start(driverInstallInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newDriverInstallMirror(q)
	defer m.close()
	var pending *driverInstallMirrorEvent
	fail := func(reason string) (driverInstallResult, bool) {
		driverInstallRecoveries.Add(1)
		driverInstallRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; i < 100000; i++ {
			var e driverInstallMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return driverInstallResult{Err: errors.New("driver installation oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return driverInstallResult{Err: errors.New("driver installation oracle closed")}, true
			}
		}
		return driverInstallResult{Err: errors.New("driver installation continuation limit")}, true
	}
	var step uint64
	for i := 0; i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := driverInstallFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e driverInstallMirrorEvent
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
			want, err := json.Marshal(driverInstallResultJSON(e.Result))
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
		wire := driverInstallReplyBytes(r)
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return driverInstallResult{Err: errors.New("driver installation oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
