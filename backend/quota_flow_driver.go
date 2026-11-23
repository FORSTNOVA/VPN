package main

import (
	"fmt"
	"reflect"
	"sync/atomic"
)

var quotaFlowRecoveries atomic.Uint64
var quotaFlowRecoveryReason atomic.Value

func quotaInterrupted() quotaServiceResult {
	return quotaServiceResult{Status: 500, Message: "quota refresh interrupted"}
}
func runRustQuotaServiceWithStart(q quotaServiceRequest, fx quotaServiceEffects, start quotaFlowStart) (result quotaServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = quotaInterrupted()
			handled = started
		}
	}()
	input, err := quotaStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newQuotaMirror(q)
	defer mirror.close()
	original := q.OriginalURL
	index := -1
	var saved subscriptionEntry
	last := uint64(0)
	var pending *quotaMirrorEvent
	take := func() (quotaMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (quotaServiceResult, bool) {
		quotaFlowRecoveries.Add(1)
		if !started {
			return quotaServiceResult{}, false
		}
		for i := 0; i < 100000; i++ {
			e, ok := take()
			if !ok {
				return quotaInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return quotaInterrupted(), true
				}
				return e.result, true
			}
			reply := fx.Call(e.effect)
			mirror.resume(reply)
		}
		return quotaInterrupted(), true
	}
	for i := 0; i < 100000; i++ {
		b, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeQuotaFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 11 {
			e, ok := take()
			if !ok {
				return finish()
			}
			valid := e.terminal && !e.panicked && e.result.Status == f.status && e.result.Message == quotaMessage(f.code, f.message) && e.result.HasEntry == (f.code == 0)
			if f.code == 0 {
				valid = valid && f.index == int64(index) && reflect.DeepEqual(e.result.Entry, saved)
			}
			if !valid {
				quotaFlowRecoveryReason.Store(fmt.Sprintf("result code=%d status=%d expected=%d terminal=%v", f.code, f.status, e.result.Status, e.terminal))
				pending = &e
				return finish()
			}
			return e.result, true
		}
		expected, ok := quotaPort(f, q, original, index)
		if !ok {
			return finish()
		}
		e, ok := take()
		if !ok {
			return finish()
		}
		if e.terminal || !reflect.DeepEqual(expected, e.effect) {
			quotaFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
			pending = &e
			return finish()
		}
		started = true
		reply := fx.Call(expected)
		if f.kind == 1 {
			for _, entry := range reply.Entries {
				if entry.ID == q.ID {
					original = entry.URL
					break
				}
			}
		}
		if f.kind == 9 {
			index = int(f.index)
		}
		if f.kind == 10 {
			saved = cloneQuotaEntries([]subscriptionEntry{reply.Entry})[0]
		}
		encoded, encodingErr := quotaReplyBytes(f, reply)
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
