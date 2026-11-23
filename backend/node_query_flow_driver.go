package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var nodeQueryFlowRecoveries atomic.Uint64
var nodeQueryFlowRecoveryReason atomic.Value

func nodeQueryInterrupted() nodeQueryServiceResult {
	return nodeQueryServiceResult{Status: 500, Err: errors.New("node query operation interrupted")}
}
func runRustNodeQueryServiceWithStart(q nodeQueryServiceRequest, fx nodeQueryServiceEffects, start nodeQueryFlowStart) (result nodeQueryServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = nodeQueryInterrupted()
			handled = started
		}
	}()
	input, err := nodeQueryStartInput(q)
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
	mirror := newNodeQueryMirror(q)
	defer mirror.close()

	arena := newNodeQueryArena(q)
	last := uint64(0)
	var pending *nodeQueryMirrorEvent
	take := func() (nodeQueryMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (nodeQueryServiceResult, bool) {
		nodeQueryFlowRecoveries.Add(1)
		if !started {
			return nodeQueryServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return nodeQueryInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return nodeQueryInterrupted(), true
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
		f, err := decodeNodeQueryFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				nodeQueryFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			nodeQueryFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
