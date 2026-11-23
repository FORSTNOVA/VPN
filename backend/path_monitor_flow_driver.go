package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var pathMonitorFlowRecoveries atomic.Uint64
var pathMonitorFlowRecoveryReason atomic.Value

func pathMonitorInterrupted() pathMonitorServiceResult {
	return pathMonitorServiceResult{Status: 500, Err: errors.New("path monitor operation interrupted")}
}
func runRustPathMonitorServiceWithStart(q pathMonitorServiceRequest, fx pathMonitorServiceEffects, start pathMonitorFlowStart) (result pathMonitorServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = pathMonitorInterrupted()
			handled = started
		}
	}()
	input, err := pathMonitorStartInput(q)
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
	mirror := newPathMonitorMirror(q)
	defer mirror.close()

	arena := newPathMonitorArena(q)
	last := uint64(0)
	var pending *pathMonitorMirrorEvent
	take := func() (pathMonitorMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (pathMonitorServiceResult, bool) {
		pathMonitorFlowRecoveries.Add(1)
		if !started {
			return pathMonitorServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return pathMonitorInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return pathMonitorInterrupted(), true
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
		f, err := decodePathMonitorFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				pathMonitorFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			pathMonitorFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
