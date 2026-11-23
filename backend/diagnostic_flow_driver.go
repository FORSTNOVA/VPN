package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var diagnosticFlowRecoveries atomic.Uint64
var diagnosticFlowRecoveryReason atomic.Value

func diagnosticInterrupted() diagnoseServiceResult {
	return diagnoseServiceResult{Status: 500, Message: "diagnostic operation interrupted", Err: errors.New("diagnostic operation interrupted")}
}
func runRustDiagnoseServiceWithStart(q diagnoseServiceRequest, fx diagnoseServiceEffects, start diagnosticFlowStart) (result diagnoseServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = diagnosticInterrupted()
			handled = started
		}
	}()
	input, err := diagnosticStartInput(q)
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
				FinalizeDiagnoseResult(diagnoseServiceRequest, diagnoseServiceResult) diagnoseServiceResult
			}); ok {
				result = f.FinalizeDiagnoseResult(q, result)
			}
		}
	}()
	defer session.close()
	mirror := newDiagnosticMirror(q)
	defer mirror.close()

	arena := newDiagnosticArena(q)
	last := uint64(0)
	var pending *diagnosticMirrorEvent
	take := func() (diagnosticMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (diagnoseServiceResult, bool) {
		diagnosticFlowRecoveries.Add(1)
		if !started {
			return diagnoseServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return diagnosticInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return diagnosticInterrupted(), true
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
		f, err := decodeDiagnosticFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				diagnosticFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			diagnosticFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
