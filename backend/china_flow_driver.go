package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var chinaFlowRecoveries atomic.Uint64
var chinaFlowRecoveryReason atomic.Value

func chinaInterrupted() chinaServiceResult {
	return chinaServiceResult{Err: errors.New("china operation interrupted")}
}
func runRustChinaServiceWithStart(q chinaServiceRequest, fx chinaServiceEffects, start chinaFlowStart) (result chinaServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = chinaInterrupted()
			handled = started
		}
	}()
	input, err := chinaStartInput(q)
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
	mirror := newChinaMirror(q)
	defer mirror.close()

	arena := newChinaArena(q)
	last := uint64(0)
	var pending *chinaMirrorEvent
	take := func() (chinaMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (chinaServiceResult, bool) {
		chinaFlowRecoveries.Add(1)
		if !started {
			return chinaServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return chinaInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return chinaInterrupted(), true
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
		f, err := decodeChinaFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 31 || f.kind == 33 {
			encoded, e := arena.internal(f)
			if e != nil {
				return finish()
			}
			last = step
			if e = session.advance(step, encoded); e != nil {
				return finish()
			}
			continue
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				chinaFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			chinaFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
