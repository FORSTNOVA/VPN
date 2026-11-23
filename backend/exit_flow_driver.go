package main

import (
	"errors"
	"fmt"

	"reflect"
	"sync/atomic"
)

var exitFlowRecoveries atomic.Uint64
var exitFlowRecoveryReason atomic.Value

func exitInterrupted() exitServiceResult {
	return exitServiceResult{Err: errors.New("exit operation interrupted")}
}
func runRustExitServiceWithStart(q exitServiceRequest, fx exitServiceEffects, start exitFlowStart) (result exitServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = exitInterrupted()
			handled = started
		}
	}()
	input, err := exitStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newExitMirror(q)
	defer mirror.close()

	last := uint64(0)
	var pending *exitMirrorEvent
	take := func() (exitMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (exitServiceResult, bool) {
		exitFlowRecoveries.Add(1)
		if !started {
			return exitServiceResult{}, false
		}
		for i := 0; i < 100000; i++ {
			e, ok := take()
			if !ok {
				return exitInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return exitInterrupted(), true
				}
				return e.result, true
			}
			reply := fx.Call(e.effect)
			mirror.resume(reply)
		}
		return exitInterrupted(), true
	}
	for i := 0; i < 100000; i++ {
		b, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeExitFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || e.result.Err != nil {
				exitFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
				pending = &e
				return finish()
			}
			return e.result, true
		}
		expected, ok := exitPort(f)
		if !ok {
			return finish()
		}
		e, ok := take()
		if !ok {
			return finish()
		}
		if e.terminal || !reflect.DeepEqual(expected, e.effect) {
			exitFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
			pending = &e
			return finish()
		}
		started = true
		reply := fx.Call(expected)
		encoded, encodingErr := exitReplyBytes(f, reply)
		mirror.resume(reply)
		if encodingErr != nil {
			return finish()
		}
		last = step
		if err := session.advance(step, encoded); err != nil {
			return finish()
		}
	}
	return finish()
}
