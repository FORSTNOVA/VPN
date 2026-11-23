package main

import (
	"errors"
	"reflect"
	"time"
)

type adaptiveFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type adaptiveFlowStart func([]byte) (adaptiveFlowSession, error)
type adaptiveFrame struct {
	kind       byte
	effect     adaptiveServiceEffect
	token      int64
	errorCode  byte
	resultBool bool
}

func adaptiveStartInput(q adaptiveServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"exchange": 0, "resolve": 1, "switch": 2, "serve": 3, "monitor": 4, "check": 5, "check-worker": 6, "confirm": 7, "note": 8, "block": 9, "create": 10, "close": 11}[q.Op]
	if !ok {
		return nil, errors.New("unsupported adaptive operation")
	}
	b := []byte{op}
	for _, v := range []int{q.Index, q.From, q.To} {
		b = registryInt(b, int64(v))
	}
	return b, nil
}
func decodeAdaptiveFrame(b []byte) (adaptiveFrame, error) {
	r := quotaReader{b: b}
	f := adaptiveFrame{kind: r.byte()}
	f.effect.Index = int(r.integer())
	f.effect.Value = r.integer()
	f.effect.Duration = time.Duration(r.integer())
	f.token = r.integer()
	f.effect.Flag = serviceReadBool(&r)
	f.errorCode = r.byte()
	f.resultBool = serviceReadBool(&r)
	return f, serviceWireDone(&r)
}

type adaptiveArena struct {
	q    adaptiveServiceRequest
	errs [256]error
}

func newAdaptiveArena(q adaptiveServiceRequest) *adaptiveArena { return &adaptiveArena{q: q} }
func (a *adaptiveArena) port(f adaptiveFrame) (adaptiveServiceEffect, bool) {
	e := f.effect
	k, ok := adaptivePortKinds[f.kind]
	e.Kind = k
	if f.kind == 9 || f.kind == 10 {
		e.Count = int(e.Value)
		e.Value = 0
	}
	return e, ok
}
func (a *adaptiveArena) resultMatches(f adaptiveFrame, r adaptiveServiceResult) bool {
	if r.Bool != f.resultBool || r.Value != f.effect.Value {
		return false
	}
	if f.errorCode == 0 {
		return r.Err == nil
	}
	if f.errorCode != 1 || f.token <= 0 || f.token >= 256 {
		return false
	}
	want := a.errs[f.token]
	return r.Err != nil && want != nil && reflect.TypeOf(r.Err) == reflect.TypeOf(want) && reflect.TypeOf(want).Comparable() && r.Err == want
}
func (a *adaptiveArena) replyBytes(f adaptiveFrame, r adaptiveServiceReply) ([]byte, error) {
	b := []byte{f.kind}
	for _, v := range []bool{r.Err == nil, r.Flag, r.Other} {
		b = registryBool(b, v)
	}
	value := r.Value
	if f.kind == 27 {
		value = int64(r.Count)
	}
	b = registryInt(b, value)
	b = registryInt(b, int64(r.Index))
	token := int64(0)
	if r.Err != nil {
		a.errs[f.kind] = r.Err
		token = int64(f.kind)
	}
	b = registryInt(b, token)
	event := byte(0)
	switch r.Text {
	case "done":
		event = 1
	case "timer":
		event = 2
	case "cancel":
		event = 3
	}
	b = append(b, event)
	b = registryInt(b, r.Value)
	b = registryInt(b, r.Value)
	for _, v := range []bool{r.Bits&1 != 0, r.Bits&2 != 0, r.Bits&4 != 0} {
		b = registryBool(b, v)
	}
	for _, v := range []int{r.QCount, r.ACount, r.Code} {
		b = registryInt(b, int64(v))
	}
	b = registryBytes(b, r.Text)
	b = registryInt(b, int64(r.Bits))
	if len(b) > 8<<20 {
		return nil, errors.New("adaptive reply limit")
	}
	return b, nil
}

var adaptivePortKinds = map[byte]string{
	1: "pack", 2: "request", 3: "do", 4: "body-close", 5: "failure", 6: "body-read", 7: "unpack", 8: "answer-accept", 9: "cas", 10: "switch-log", 11: "context", 12: "context-cancel", 13: "completion-new", 14: "preferred", 15: "resolve-start", 16: "timer-new", 17: "timer-stop", 18: "head-wait", 19: "resolve-wait", 20: "probe-name", 21: "check-start", 22: "check-wait", 23: "custom-present", 24: "custom-confirm", 25: "blocked", 26: "running", 27: "probe-config", 28: "site", 29: "note-present", 30: "anomaly-log", 31: "note-anomaly", 32: "answer-failure", 33: "answer-write", 34: "ticker-new", 35: "ticker-stop", 36: "monitor-wait", 37: "check-child", 38: "clock-start", 39: "probe-question", 40: "exchange-child", 41: "elapsed", 42: "reject", 43: "reject-log", 44: "set-blocked", 45: "set-reason", 46: "set-switch-time", 47: "clear-note", 48: "event", 49: "listen", 50: "relay-context", 51: "client", 52: "server", 53: "server-start", 54: "server-ready", 55: "relay-cancel", 56: "server-shutdown", 57: "idle-close"}
