package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var converterFlowRecoveries atomic.Uint64
var converterFlowRecoveryReason atomic.Value

func converterInterrupted() converterServiceResult {
	return converterServiceResult{Err: errors.New("converter operation interrupted")}
}
func runRustConverterServiceWithStart(q converterServiceRequest, fx converterServiceEffects, start converterFlowStart) (result converterServiceResult, handled bool) {
	started := false
	defer func() {
		if p := recover(); p != nil {
			result = converterInterrupted()
			handled = started
		}
	}()
	input, err := converterStartInput(q)
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
	mirror := newConverterMirror(q)
	defer mirror.close()
	complete := func(r converterServiceResult) (converterServiceResult, bool) {
		mirror.close()
		closeSession()
		if f, ok := fx.(interface {
			FinalizeConverterResult(converterServiceRequest, converterServiceResult) converterServiceResult
		}); ok {
			r = f.FinalizeConverterResult(q, r)
		}
		return r, true
	}

	arena := newConverterArena(q)
	last := uint64(0)
	var pending *converterMirrorEvent
	take := func() (converterMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (converterServiceResult, bool) {
		converterFlowRecoveries.Add(1)
		if !started {
			return converterServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return converterInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return converterInterrupted(), true
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
		f, err := decodeConverterFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == "result" {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				converterFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			converterFlowRecoveryReason.Store(fmt.Sprintf("frame=%s expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
