package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
)

var connectionFlowRecoveries atomic.Uint64
var connectionFlowRecoveryReason atomic.Value

func connectionInterrupted() connectionServiceResult {
	return connectionServiceResult{Status: 500, Message: "connection operation interrupted", Err: errors.New("connection operation interrupted")}
}
func runRustConnectionServiceWithStart(q connectionServiceRequest, fx connectionServiceEffects, start connectionFlowStart) (result connectionServiceResult, handled bool) {
	started := false
	defer func() {
		if recover() != nil {
			result = connectionInterrupted()
			handled = started
		}
	}()
	input, err := connectionStartInput(q)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer fx.Release()
	defer session.close()
	mirror := newConnectionMirror(q)
	defer mirror.close()
	var current *connectionServiceState
	var nodes []proxyNode
	last := uint64(0)
	var pending *connectionMirrorEvent
	take := func() (connectionMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	finish := func() (connectionServiceResult, bool) {
		connectionFlowRecoveries.Add(1)
		if !started {
			return connectionServiceResult{}, false
		}
		for i := 0; i < 100000; i++ {
			e, ok := take()
			if !ok {
				return connectionInterrupted(), true
			}
			if e.terminal {
				if e.panicked {
					return connectionInterrupted(), true
				}
				return e.result, true
			}
			reply := fx.Call(e.effect)
			mirror.resume(reply)
		}
		return connectionInterrupted(), true
	}
	for i := 0; i < 100000; i++ {
		b, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeConnectionFrame(b)
		if err != nil {
			return finish()
		}
		if f.kind == 63 {
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !connectionResultMatches(f, e.result, q, current) {
				connectionFlowRecoveryReason.Store(fmt.Sprintf("result=%d status=%d expected=%d terminal=%v", f.b, f.a, e.result.Status, e.terminal))
				pending = &e
				return finish()
			}
			return e.result, true
		}
		expected, ok := connectionPort(f, current, nodes)
		if !ok {
			return finish()
		}
		e, ok := take()
		if !ok {
			return finish()
		}
		if e.terminal || !reflect.DeepEqual(expected, e.effect) {
			connectionFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s", f.kind, e.effect.Kind, expected.Kind))
			pending = &e
			return finish()
		}
		started = true
		reply := fx.Call(expected)
		if reply.State != nil {
			current = cloneConnectionReply(reply).State
		}
		if f.kind == 8 {
			nodes = connectionCopyNodes(reply.Nodes)
		}
		encoded, encodingErr := connectionReplyBytes(f, reply)
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
func connectionResultMatches(f connectionFrame, r connectionServiceResult, q connectionServiceRequest, current *connectionServiceState) bool {
	if q.Op == "drop" {
		if f.b == 20 {
			return r.Err == nil
		}
		return f.b == 21 && r.Err != nil && r.Err.Error() == f.text && errors.Is(r.Err, os.ErrProcessDone) == (f.a != 0)
	}
	if int64(r.Status) != f.a || r.Err != nil {
		return false
	}
	if f.b == 20 {
		return r.Message == "" && reflect.DeepEqual(r.Data, map[string]any{"ok": true})
	}
	if f.b == 22 {
		return r.Message == "" && reflect.DeepEqual(r.Data, map[string]any{"ok": true, "pending": true})
	}
	if f.b == 0 {
		if len(f.items) != 4 || len(f.items[2]) != 1 || len(f.items[3]) != 8 || f.items[2][0] > 1 {
			return false
		}
		data := map[string]any{"ok": true, "selectedNode": f.data, "autoSwitched": f.items[2][0] == 1, "latencyMs": int(int64(binary.LittleEndian.Uint64([]byte(f.items[3])))), "lockedRegion": f.items[0], "mode": f.items[1]}
		return r.Message == "" && reflect.DeepEqual(r.Data, data)
	}
	msg := map[int64]string{1: connectionMessages["invalid-selection"], 2: connectionMessages["chain-selection"], 3: "fetch the subscription nodes before selecting one", 4: connectionMessages["proxy-conflict"], 5: f.text, 6: "subscription returned no selectable nodes; fetch nodes or check the subscription", 8: "could not select the proxy group", 9: "Mihomo did not activate the proxy group", 10: "could not save the selected node", 11: "could not save the fetched node list", 12: "could not set the Windows system proxy", 13: "Mihomo could not be stopped", 14: "could not save the pending node selection", 15: "could not read Mihomo nodes", 16: "node is no longer in the subscription; refresh nodes", 17: "Mihomo could not select that node"}[f.b]
	if f.b == 7 {
		if current == nil {
			return false
		}
		msg = fmt.Sprintf(connectionMessages["no-region-node"], current.LockedRegion)
	}
	return msg != "" && r.Message == msg && r.Data == nil
}
