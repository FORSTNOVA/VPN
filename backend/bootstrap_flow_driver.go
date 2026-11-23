package main

import (
	"errors"
	"fmt"
	"reflect"

	"sync/atomic"
)

var bootstrapFlowRecoveries atomic.Uint64
var bootstrapFlowRecoveryReason atomic.Value

func bootstrapInterrupted() bootstrapServiceResult {
	return bootstrapServiceResult{Err: errors.New("bootstrap operation interrupted")}
}
func runRustBootstrapServiceWithStart(q bootstrapServiceRequest, fx bootstrapServiceEffects, start bootstrapFlowStart) (result bootstrapServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = bootstrapInterrupted()
			handled = started
		}
	}()
	input, err := bootstrapStartInput(q)
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
	mirror := newBootstrapMirror(q)
	defer mirror.close()
	complete := func(r bootstrapServiceResult) (bootstrapServiceResult, bool) {
		mirror.close()
		closeSession()
		if f, ok := fx.(interface {
			FinalizeBootstrapResult(bootstrapServiceRequest, bootstrapServiceResult) bootstrapServiceResult
		}); ok {
			r = f.FinalizeBootstrapResult(q, r)
		}
		return r, true
	}

	arena := newBootstrapArena(q)
	last := uint64(0)
	var pending *bootstrapMirrorEvent
	take := func() (bootstrapMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (bootstrapServiceResult, bool) {
		bootstrapFlowRecoveries.Add(1)
		if !started {
			return bootstrapServiceResult{}, false
		}
		for {
			e, ok := take()
			if !ok {
				return bootstrapInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return bootstrapInterrupted(), true
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
		f, err := decodeBootstrapFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 254 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !arena.resultMatches(f, e.result) {
				bootstrapFlowRecoveryReason.Store(fmt.Sprintf("result=%d terminal=%v", 0, e.terminal))
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
			bootstrapFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
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
