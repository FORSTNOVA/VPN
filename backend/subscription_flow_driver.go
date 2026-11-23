package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

var subscriptionFlowRecoveries atomic.Uint64
var subscriptionFlowRecoveryReason atomic.Value

func registryResultMatches(e registryEffect, r subscriptionServiceRequest, result subscriptionServiceResult, initial subscriptionServiceState) bool {
	if e.index == 22 {
		return r.Op == "save" && e.aux == 500 && result.Err != nil
	}
	if e.aux != int64(result.Status) || result.Err != nil {
		return false
	}
	same := func(expected []subscriptionEntry) bool {
		v := registryMetadata(expected)
		if len(v) != len(e.entries) {
			return false
		}
		for i := range v {
			if v[i] != e.entries[i] {
				return false
			}
		}
		return true
	}
	keys := map[int64]string{1: "empty-url", 3: "duplicate", 4: "missing-id", 5: "not-found", 6: "label", 7: "delete-running", 8: "activate-running", 9: "add-save", 10: "save", 11: "save", 12: "save", 15: "hours", 16: "settings-save"}
	if key, ok := keys[e.index]; ok {
		return result.Message == subscriptionServiceMessages[key]
	}
	switch e.index {
	case 0:
		return result.Status == 200 && result.Message == ""
	case 2:
		return result.Message == r.URLValidationError
	case 13:
		return strings.HasPrefix(result.Message, subscriptionServiceMessages["merge-failed"])
	case 14, 17:
		return result.Message == e.data
	case 20:
		if r.Op != "find" && r.Op != "find-url" {
			return false
		}
		if !result.Found {
			return len(e.entries) == 0 && len(e.data) == 0
		}
		if !same([]subscriptionEntry{result.Entry}) || len(e.data) != 8 {
			return false
		}
		idx := binary.LittleEndian.Uint64([]byte(e.data))
		return idx < uint64(len(initial.Subscriptions)) && initial.Subscriptions[idx].ID == result.Entry.ID && initial.Subscriptions[idx].URL == result.Entry.URL
	case 21:
		if r.Op != "list" || !same(result.Entries) || len(e.data) != 8*len(result.Entries) {
			return false
		}
		seen := map[uint64]bool{}
		for i := range result.Entries {
			idx := binary.LittleEndian.Uint64([]byte(e.data[i*8:]))
			if idx >= uint64(len(initial.Subscriptions)) || seen[idx] {
				return false
			}
			seen[idx] = true
			got := initial.Subscriptions[idx]
			want := result.Entries[i]
			if got.ID != want.ID || got.URL != want.URL || got.Label != want.Label {
				return false
			}
		}
		return true
	case 23:
		return r.Op == "add" && same([]subscriptionEntry{result.Entry})
	case 24:
		return r.Op == "update" && same([]subscriptionEntry{result.Entry})
	case 25:
		return r.Op == "delete"
	case 26:
		if r.Op != "activate" || len(e.entries) != 1 || len(e.data) != 8 {
			return false
		}
		count, ok := result.Data["nodes"].(int)
		return ok && int64(binary.LittleEndian.Uint64([]byte(e.data))) == int64(count) && e.entries[0].url == result.Data["subscriptionUrl"] && e.entries[0].label == result.Data["label"]
	case 27:
		return r.Op == "activate"
	case 28:
		count, ok := result.Data["nodes"].(int)
		return r.Op == "activate" && ok && len(e.data) == 8 && int64(binary.LittleEndian.Uint64([]byte(e.data))) == int64(count)
	case 29:
		return r.Op == "hours"
	default:
		return false
	}
}

// The Go oracle is a suspended safety continuation, not a second IO executor.
// Rust emits the external plan; the mirror validates each port and provides the
// authoritative opaque state/rollback. Internal Rust mutations are acknowledged
// but never allowed to overwrite fields excluded from its metadata snapshot.
func runRustSubscriptionServiceWithStart(s *subscriptionServiceState, r subscriptionServiceRequest, fx subscriptionServiceEffects, start subscriptionFlowStart) (result subscriptionServiceResult, handled bool) {
	input, err := registryStartInput(*s, r)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer session.close()
	mirror := newSubscriptionMirror(*s, r)
	defer mirror.close()
	initial := subscriptionCloneResourceState(*s)
	started := false
	transferring := false
	last := uint64(0)
	var pending *subscriptionMirrorEvent
	defer func() {
		if recover() != nil {
			result = subscriptionServiceResult{Status: 500, Message: "subscription effect interrupted", Err: errors.New("subscription effect interrupted")}
			handled = started
		}
	}()
	take := func() (subscriptionMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	execute := func(e subscriptionMirrorEvent) subscriptionEffectReply {
		if transferring {
			*s = e.live
		} else {
			*s = subscriptionCloneResourceState(e.live)
		}
		started = true
		if e.isolated {
			isolated := e.state
			if !transferring {
				isolated = subscriptionCloneResourceState(e.state)
			}
			reply := fx.Call(e.kind, e.arg, &isolated)
			mirror.resume(isolated, reply, transferring)
			return reply
		}
		reply := fx.Call(e.kind, e.arg, s)
		mirror.resume(*s, reply, transferring)
		return reply
	}
	// Resume only requests that the same oracle invocation has not executed yet.
	finish := func() (subscriptionServiceResult, bool) {
		subscriptionFlowRecoveries.Add(1)
		transferring = true
		if !started {
			return subscriptionServiceResult{}, false
		}
		for n := 0; n < 1000000; n++ {
			e, ok := take()
			if !ok {
				return subscriptionServiceResult{Status: 500, Message: "subscription continuation interrupted", Err: errors.New("subscription continuation interrupted")}, true
			}
			if e.terminal {
				*s = subscriptionCloneResourceState(e.state)
				if e.panicked {
					return subscriptionServiceResult{Status: 500, Message: "subscription continuation interrupted", Err: errors.New("subscription continuation interrupted")}, true
				}
				return e.result, true
			}
			execute(e)
		}
		return subscriptionServiceResult{Status: 500, Message: "subscription continuation limit", Err: errors.New("subscription continuation limit")}, true
	}
	nextPort := func() (subscriptionMirrorEvent, bool) {
		for {
			e, ok := take()
			if !ok {
				return e, false
			}
			if e.terminal || e.kind != "log" {
				return e, true
			}
			execute(e)
		}
	}
	for n := 0; n < 1000000; n++ {
		raw, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		e, err := decodeRegistryEffect(raw)
		if err != nil {
			return finish()
		}
		reply := registryAck()
		if e.kind == 63 {
			event, ok := nextPort()
			if !ok {
				return finish()
			}
			if !event.terminal || event.panicked || !registryResultMatches(e, r, event.result, initial) {
				subscriptionFlowRecoveryReason.Store(fmt.Sprintf("op=%s result=%d status=%d expected=%d terminal=%v", r.Op, e.index, e.aux, event.result.Status, event.terminal))
				pending = &event
				return finish()
			}
			*s = subscriptionCloneResourceState(event.state)
			return event.result, true
		}
		if kind, arg, external := registryPort(e); external {
			event, ok := nextPort()
			if !ok {
				return finish()
			}
			if event.terminal || event.kind != kind || event.arg != arg {
				subscriptionFlowRecoveryReason.Store(fmt.Sprintf("op=%s kind=%d expected=%s got=%s argMatch=%v terminal=%v", r.Op, e.kind, event.kind, kind, event.arg == arg, event.terminal))
				pending = &event
				return finish()
			}
			out := execute(event)
			if e.kind == 1 && len(out.Loaded) > 256 {
				return finish()
			}
			reply = registryReplyBytes(e.kind, out, *s)
		} else {
			switch e.kind {
			case 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 44:
			default:
				return finish()
			}
		}
		last = step
		if err := session.advance(step, reply); err != nil {
			return finish()
		}
	}
	return finish()
}
