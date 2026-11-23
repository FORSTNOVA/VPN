package main

import (
	"errors"
	"fmt"

	"reflect"
	"sync/atomic"
)

var regionJobFlowRecoveries atomic.Uint64
var regionJobFlowRecoveryReason atomic.Value

func regionJobInterrupted() regionJobServiceResult {
	return regionJobServiceResult{Status: 500, Message: "region job operation interrupted", Err: errors.New("region job operation interrupted")}
}
func runRustRegionJobServiceWithStart(q regionJobServiceRequest, fx regionJobServiceEffects, start regionJobFlowStart) (result regionJobServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = regionJobInterrupted()
			handled = started
		}
	}()
	input, err := regionJobStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newRegionJobMirror(q)
	defer mirror.close()

	arena := newRegionJobArena(q)
	last := uint64(0)
	var pending *regionJobMirrorEvent
	take := func() (regionJobMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (regionJobServiceResult, bool) {
		regionJobFlowRecoveries.Add(1)
		if !started {
			return regionJobServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return regionJobInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return regionJobInterrupted(), true
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
		f, err := decodeRegionJobFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				regionJobFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			regionJobFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
