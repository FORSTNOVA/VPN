package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var adaptiveFlowRecoveries atomic.Uint64
var adaptiveFlowRecoveryReason atomic.Value

func adaptiveInterrupted() adaptiveServiceResult {
	return adaptiveServiceResult{Err: errors.New("adaptive operation interrupted")}
}
func runRustAdaptiveServiceWithStart(q adaptiveServiceRequest, fx adaptiveServiceEffects, start adaptiveFlowStart) (result adaptiveServiceResult, handled bool) {
	started := false
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(adaptiveResponsePanic); ok {
				panic(p)
			}
			result = adaptiveInterrupted()
			handled = started
		}
	}()
	input, err := adaptiveStartInput(q)
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
	sessionClosed := false
	closeSession := func() {
		if !sessionClosed {
			sessionClosed = true
			session.close()
		}
	}
	defer closeSession()
	mirror := newAdaptiveMirror(q)
	defer mirror.close()
	complete := func(r adaptiveServiceResult) (adaptiveServiceResult, bool) {
		mirror.close()
		closeSession()
		if f, ok := fx.(interface {
			FinalizeAdaptiveResult(adaptiveServiceRequest, adaptiveServiceResult) adaptiveServiceResult
		}); ok {
			r = f.FinalizeAdaptiveResult(q, r)
		}
		return r, true
	}

	arena := newAdaptiveArena(q)
	last := uint64(0)
	var pending *adaptiveMirrorEvent
	take := func() (adaptiveMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (adaptiveServiceResult, bool) {
		adaptiveFlowRecoveries.Add(1)
		if !started {
			return adaptiveServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return adaptiveInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return adaptiveInterrupted(), true
				}
				return complete(e.result)
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
		f, err := decodeAdaptiveFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 254 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				adaptiveFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
				pending = &e
				return finish()
			}
			started = true // A validated pure completion owns its Release.
			return complete(e.result)
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
			adaptiveFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
