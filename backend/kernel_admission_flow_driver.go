package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var kernelAdmissionFlowRecoveries atomic.Uint64
var kernelAdmissionFlowRecoveryReason atomic.Value

func kernelAdmissionInterrupted() kernelAdmissionResult {
	return kernelAdmissionResult{Err: errors.New("kernelAdmission operation interrupted")}
}
func runRustKernelAdmissionServiceWithStart(q kernelAdmissionRequest, fx kernelAdmissionEffects, start kernelAdmissionFlowStart) (result kernelAdmissionResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = kernelAdmissionInterrupted()
			handled = started
		}
	}()
	input, err := kernelAdmissionStartInput(q)
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
	mirror := newKernelAdmissionMirror(q)
	defer mirror.close()
	complete := func(r kernelAdmissionResult) (kernelAdmissionResult, bool) {
		mirror.close()
		closeSession()
		if f, ok := fx.(interface {
			FinalizeKernelAdmissionResult(kernelAdmissionRequest, kernelAdmissionResult) kernelAdmissionResult
		}); ok {
			r = f.FinalizeKernelAdmissionResult(q, r)
		}
		return r, true
	}

	arena := newKernelAdmissionArena(q)
	last := uint64(0)
	var pending *kernelAdmissionMirrorEvent
	take := func() (kernelAdmissionMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (kernelAdmissionResult, bool) {
		kernelAdmissionFlowRecoveries.Add(1)
		if !started {
			return kernelAdmissionResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return kernelAdmissionInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return kernelAdmissionInterrupted(), true
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
		f, err := decodeKernelAdmissionFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == "result" {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				kernelAdmissionFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			kernelAdmissionFlowRecoveryReason.Store(fmt.Sprintf("frame=%s expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
