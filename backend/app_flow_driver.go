package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var appFlowRecoveries atomic.Uint64
var appFlowRecoveryReason atomic.Value

func appInterrupted() appServiceResult {
	return appServiceResult{Err: errors.New("app operation interrupted")}
}
func runRustAppServiceWithStart(q appServiceRequest, fx appServiceEffects, start appFlowStart) (result appServiceResult, handled bool) {
	started := false
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(appResponsePanic); ok {
				panic(p)
			}
			result = appInterrupted()
			handled = started
		}
	}()
	input, err := appStartInput(q)
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
	mirror := newAppMirror(q)
	defer mirror.close()

	arena := newAppArena(q)
	last := uint64(0)
	var pending *appMirrorEvent
	take := func() (appMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (appServiceResult, bool) {
		appFlowRecoveries.Add(1)
		if !started {
			return appServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return appInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return appInterrupted(), true
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
		f, err := decodeAppFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				appFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			appFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
