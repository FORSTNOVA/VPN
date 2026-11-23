package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var trafficFlowRecoveries atomic.Uint64
var trafficFlowRecoveryReason atomic.Value

func trafficInterrupted() trafficServiceResult {
	return trafficServiceResult{Status: 500, Message: "traffic operation interrupted", Err: errors.New("traffic operation interrupted")}
}
func runRustTrafficServiceWithStart(q trafficServiceRequest, fx trafficServiceEffects, start trafficFlowStart) (result trafficServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = trafficInterrupted()
			handled = started
		}
	}()
	input, err := trafficStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newTrafficMirror(q)
	defer mirror.close()

	arena := newTrafficArena(q)
	last := uint64(0)
	var pending *trafficMirrorEvent
	take := func() (trafficMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (trafficServiceResult, bool) {
		trafficFlowRecoveries.Add(1)
		if !started {
			return trafficServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return trafficInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return trafficInterrupted(), true
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
		f, err := decodeTrafficFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				trafficFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			trafficFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
