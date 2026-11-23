package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var pathFlowRecoveries atomic.Uint64
var pathFlowRecoveryReason atomic.Value

func pathInterrupted() pathServiceResult {
	return pathServiceResult{Err: errors.New("path operation interrupted")}
}
func runRustPathServiceWithStart(q pathServiceRequest, fx pathServiceEffects, start pathFlowStart) (result pathServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = pathInterrupted()
			handled = started
		}
	}()
	input, err := pathStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	// A guarded round can arrive with its admission lock already held.
	// Before the first effect, fallback must retain that lock for Go.
	defer func() {
		if started {
			fx.Release()
		}
	}()
	defer session.close()
	mirror := newPathMirror(q)
	defer mirror.close()

	arena := newPathArena(q)
	last := uint64(0)
	var pending *pathMirrorEvent
	take := func() (pathMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (pathServiceResult, bool) {
		pathFlowRecoveries.Add(1)
		if !started {
			return pathServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return pathInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return pathInterrupted(), true
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
		f, err := decodePathFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				pathFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
				pending = &e
				return finish()
			}
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
			pathFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
