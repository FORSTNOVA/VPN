package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
)

var policyDnsRecoveries atomic.Uint64
var policyDnsRecoveryReason atomic.Value

type policyDnsMirrorEvent struct {
	Effect policyDnsEffect
	Result policyDnsResult
	Done   bool
}
type policyDnsMirror struct {
	events       chan policyDnsMirrorEvent
	replies      chan policyDnsReply
	cancel, done chan struct{}
	once         sync.Once
}

func (m *policyDnsMirror) Call(q policyDnsEffect) policyDnsReply {
	select {
	case m.events <- policyDnsMirrorEvent{Effect: q}:
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
func (m *policyDnsMirror) Release() {}
func (m *policyDnsMirror) close()   { m.once.Do(func() { close(m.cancel) }); <-m.done }
func newPolicyDnsMirror(q policyDnsRequest) *policyDnsMirror {
	m := &policyDnsMirror{events: make(chan policyDnsMirrorEvent), replies: make(chan policyDnsReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() { _ = recover() }()
		r := runGoPolicyDns(q, m)
		select {
		case m.events <- policyDnsMirrorEvent{Done: true, Result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func runRustPolicyDnsWithStart(q policyDnsRequest, fx policyDnsEffects, start policyDnsStart) (out policyDnsResult, used bool) {
	s, err := start(policyDnsInput(q))
	if err != nil {
		return out, false
	}
	defer s.close()
	m := newPolicyDnsMirror(q)
	defer m.close()
	var pending *policyDnsMirrorEvent
	fail := func(reason string) (policyDnsResult, bool) {
		policyDnsRecoveries.Add(1)
		policyDnsRecoveryReason.Store(reason)
		// Resume the suspended oracle, never restart it. Replies already obtained
		// from an external effect are delivered before entering this continuation.
		for i := 0; i < 100000; i++ {
			var e policyDnsMirrorEvent
			if pending != nil {
				e = *pending
				pending = nil
			} else {
				select {
				case e = <-m.events:
				case <-m.done:
					return policyDnsResult{Err: errors.New("policy DNS oracle ended")}, true
				}
			}
			if e.Done {
				return e.Result, true
			}
			r := fx.Call(e.Effect)
			select {
			case m.replies <- r:
			case <-m.done:
				return policyDnsResult{Err: errors.New("policy DNS oracle closed")}, true
			}
		}
		return policyDnsResult{Err: errors.New("policy DNS continuation limit")}, true
	}
	var step uint64
	for i := 0; i < 100000; i++ {
		raw, n, err := s.next()
		if err != nil || n != step+1 {
			return fail("frame")
		}
		step = n
		k, t, other, number, data, err := policyDnsFrame(raw)
		if err != nil {
			return fail("decode")
		}
		var e policyDnsMirrorEvent
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
			want, err := json.Marshal(policyDnsResultJSON(e.Result))
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
		wire := policyDnsReplyBytes(r)
		err = s.advance(step, wire)
		select {
		case m.replies <- r:
		case <-m.done:
			return policyDnsResult{Err: errors.New("policy DNS oracle closed")}, true
		}
		if err != nil {
			return fail("advance")
		}
	}
	return fail("limit")
}
