package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var labelsFlowRecoveries atomic.Uint64
var labelsFlowRecoveryReason atomic.Value

func labelsInterrupted() labelsServiceResult {
	return labelsServiceResult{Err: errors.New("labels operation interrupted")}
}
func runRustLabelsServiceWithStart(q labelsServiceRequest, fx labelsServiceEffects, start labelsFlowStart) (result labelsServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = labelsInterrupted()
			handled = started
		}
	}()
	input, err := labelsStartInput(q)
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
	mirror := newLabelsMirror(q)
	defer mirror.close()

	arena := newLabelsArena(q)
	last := uint64(0)
	var pending *labelsMirrorEvent
	take := func() (labelsMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (labelsServiceResult, bool) {
		labelsFlowRecoveries.Add(1)
		if !started {
			return labelsServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return labelsInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return labelsInterrupted(), true
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
		f, err := decodeLabelsFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				labelsFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			labelsFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
