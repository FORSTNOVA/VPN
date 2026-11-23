package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var chainProbeFlowRecoveries atomic.Uint64
var chainProbeFlowRecoveryReason atomic.Value

func chainProbeInterrupted() chainProbeServiceResult {
	return chainProbeServiceResult{Status: 500, Message: "chainProbe operation interrupted", Err: errors.New("chainProbe operation interrupted")}
}
func runRustChainProbeServiceWithStart(q chainProbeServiceRequest, fx chainProbeServiceEffects, start chainProbeFlowStart) (result chainProbeServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = chainProbeInterrupted()
			handled = started
		}
	}()
	input, err := chainProbeStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newChainProbeMirror(q)
	defer mirror.close()

	arena := newChainProbeArena(q)
	last := uint64(0)
	var pending *chainProbeMirrorEvent
	take := func() (chainProbeMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (chainProbeServiceResult, bool) {
		chainProbeFlowRecoveries.Add(1)
		if !started {
			return chainProbeServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return chainProbeInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return chainProbeInterrupted(), true
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
		f, err := decodeChainProbeFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				chainProbeFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			chainProbeFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
