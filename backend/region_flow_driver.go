package main

import (
	"fmt"

	"reflect"
	"sync/atomic"
)

var regionFlowRecoveries atomic.Uint64
var regionFlowRecoveryReason atomic.Value

func regionInterrupted() regionServiceResult {
	return regionServiceResult{Status: 500, Message: "region operation interrupted"}
}
func runRustRegionServiceWithStart(q regionServiceRequest, fx regionServiceEffects, start regionFlowStart) (result regionServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = regionInterrupted()
			handled = started
		}
	}()
	input, err := regionStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newRegionMirror(q)
	defer mirror.close()

	arena := newRegionArena(q)
	last := uint64(0)
	var pending *regionMirrorEvent
	take := func() (regionMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (regionServiceResult, bool) {
		regionFlowRecoveries.Add(1)
		if !started {
			return regionServiceResult{}, false
		}
		for i := 0; i < 100000; i++ {
			e, ok := take()
			if !ok {
				return regionInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return regionInterrupted(), true
				}
				return e.result, true
			}
			reply := fx.Call(e.effect)
			mirror.resume(reply)
		}
		return regionInterrupted(), true
	}
	for i := 0; i < 100000; i++ {
		b, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeRegionFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				regionFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			regionFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
