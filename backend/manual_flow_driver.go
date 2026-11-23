package main

import (
	"errors"
	"fmt"

	"sync/atomic"
)

var manualFlowRecoveries atomic.Uint64
var manualFlowRecoveryReason atomic.Value

func manualInterrupted() manualServiceResult {
	return manualServiceResult{Status: 500, Message: "manual operation interrupted", Err: errors.New("manual operation interrupted")}
}
func runRustManualServiceWithStart(q manualServiceRequest, fx manualServiceEffects, start manualFlowStart) (result manualServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = manualInterrupted()
			handled = started
		}
	}()
	input, err := manualStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer func() {
		if handled {
			if f, ok := fx.(interface {
				FinalizeManualResult(manualServiceRequest, manualServiceResult) manualServiceResult
			}); ok {
				result = f.FinalizeManualResult(q, result)
			}
		}
	}()
	defer session.close()
	mirror := newManualMirror(q)
	defer mirror.close()

	arena := newManualArena(q)
	last := uint64(0)
	var pending *manualMirrorEvent
	take := func() (manualMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (manualServiceResult, bool) {
		manualFlowRecoveries.Add(1)
		if !started {
			return manualServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return manualInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return manualInterrupted(), true
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
		f, err := decodeManualFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				manualFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
		if e.terminal || !manualOpaqueEqual(expected, e.effect) {
			manualFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
