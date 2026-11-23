package main

import (
	"errors"
	"fmt"

	"sync/atomic"
)

var chainFlowRecoveries atomic.Uint64
var chainFlowRecoveryReason atomic.Value

func chainInterrupted() chainServiceResult {
	return chainServiceResult{Status: 500, Message: "chain operation interrupted", Err: errors.New("chain operation interrupted")}
}
func runRustChainServiceWithStart(q chainServiceRequest, fx chainServiceEffects, start chainFlowStart) (result chainServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = chainInterrupted()
			handled = started
		}
	}()
	input, err := chainStartInput(q)
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
				FinalizeChainResult(chainServiceRequest, chainServiceResult) chainServiceResult
			}); ok {
				result = f.FinalizeChainResult(q, result)
			}
		}
	}()
	defer session.close()
	mirror := newChainMirror(q)
	defer mirror.close()

	arena := newChainArena(q)
	last := uint64(0)
	var pending *chainMirrorEvent
	take := func() (chainMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (chainServiceResult, bool) {
		chainFlowRecoveries.Add(1)
		if !started {
			return chainServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return chainInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return chainInterrupted(), true
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
		f, err := decodeChainFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				chainFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			chainFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
