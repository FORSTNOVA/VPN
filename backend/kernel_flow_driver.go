package main

import (
	"errors"
	"fmt"

	"reflect"
	"sync/atomic"
)

var kernelFlowRecoveries atomic.Uint64
var kernelFlowRecoveryReason atomic.Value

func kernelInterrupted() kernelServiceResult {
	return kernelServiceResult{Err: errors.New("kernel operation interrupted")}
}
func runRustKernelServiceWithStart(q kernelServiceRequest, fx kernelServiceEffects, start kernelFlowStart) (result kernelServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = kernelInterrupted()
			handled = started
		}
	}()
	input, err := kernelStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newKernelMirror(q)
	defer mirror.close()
	arena := newKernelArena()
	last := uint64(0)
	var pending *kernelMirrorEvent
	take := func() (kernelMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (kernelServiceResult, bool) {
		kernelFlowRecoveries.Add(1)
		if !started {
			return kernelServiceResult{}, false
		}
		for i := 0; i < 100000; i++ {
			e, ok := take()
			if !ok {
				return kernelInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return kernelInterrupted(), true
				}
				return e.result, true
			}
			reply := fx.Call(e.effect)
			mirror.resume(reply)
		}
		return kernelInterrupted(), true
	}
	for i := 0; i < 100000; i++ {
		b, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeKernelFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				kernelFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", f.a, e.terminal))
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
			kernelFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
	return finish()
}
