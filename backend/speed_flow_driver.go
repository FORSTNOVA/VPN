package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var speedFlowRecoveries atomic.Uint64
var speedFlowRecoveryReason atomic.Value

func speedInterrupted() speedServiceResult {
	return speedServiceResult{Err: errors.New("speed operation interrupted")}
}
func runRustSpeedServiceWithStart(q speedServiceRequest, fx speedServiceEffects, start speedFlowStart) (result speedServiceResult, handled bool) {
	started := false
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(speedResponsePanic); ok {
				panic(p)
			}
			result = speedInterrupted()
			handled = started
		}
	}()
	input, err := speedStartInput(q)
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
	mirror := newSpeedMirror(q)
	defer mirror.close()

	arena := newSpeedArena(q)
	last := uint64(0)
	var pending *speedMirrorEvent
	take := func() (speedMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (speedServiceResult, bool) {
		speedFlowRecoveries.Add(1)
		if !started {
			return speedServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return speedInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return speedInterrupted(), true
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
		f, err := decodeSpeedFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 25 {
			encoded, e := arena.internal(f)
			if e != nil {
				return finish()
			}
			last = step
			if e = session.advance(step, encoded); e != nil {
				return finish()
			}
			continue
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				speedFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			speedFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
