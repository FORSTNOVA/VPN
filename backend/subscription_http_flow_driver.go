package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var subscriptionHTTPFlowRecoveries atomic.Uint64
var subscriptionHTTPFlowRecoveryReason atomic.Value

func subscriptionHTTPInterrupted() subscriptionHTTPServiceResult {
	return subscriptionHTTPServiceResult{Err: errors.New("subscriptionHTTP operation interrupted")}
}
func runRustSubscriptionHTTPServiceWithStart(q subscriptionHTTPServiceRequest, fx subscriptionHTTPServiceEffects, start subscriptionHTTPFlowStart) (result subscriptionHTTPServiceResult, handled bool) {
	started := false
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(subscriptionHTTPResponsePanic); ok {
				panic(p)
			}
			result = subscriptionHTTPInterrupted()
			handled = started
		}
	}()
	input, err := subscriptionHTTPStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer func() {
		if started {
			fx.Release()
		}
	}()
	defer session.close()
	mirror := newSubscriptionHTTPMirror(q)
	defer mirror.close()

	arena := newSubscriptionHTTPArena(q)
	last := uint64(0)
	var pending *subscriptionHTTPMirrorEvent
	take := func() (subscriptionHTTPMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (subscriptionHTTPServiceResult, bool) {
		subscriptionHTTPFlowRecoveries.Add(1)
		if !started {
			return subscriptionHTTPServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return subscriptionHTTPInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return subscriptionHTTPInterrupted(), true
				}
				return e.result, true
			}
			reply := fx.Call(e.effect)
			mirror.resume(reply)
		}
	}
	for {
		b, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeSubscriptionHTTPFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				subscriptionHTTPFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
				pending = &e
				return finish()
			}
			started = true // Validated pure completion owns Release too.
			return e.result, true
		}
		expected, ok := arena.port(f)
		if !ok {
			return finish()
		}
		e, ok := take()
		if !ok {
			return finish()
		}
		if e.terminal || !reflect.DeepEqual(expected, e.effect) {
			subscriptionHTTPFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
			pending = &e
			return finish()
		}
		started = true
		reply := fx.Call(expected)
		encoded, encodingErr := arena.replyBytes(f, reply)
		mirror.resume(reply)
		if encodingErr != nil {
			return finish()
		}
		last = step
		if err := session.advance(step, encoded); err != nil {
			return finish()
		}
	}
}
