package main

import (
	"errors"
	"reflect"
	"time"
)

type bootstrapFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type bootstrapFlowStart func([]byte) (bootstrapFlowSession, error)
type bootstrapFrame struct {
	kind                    byte
	effect                  bootstrapServiceEffect
	errorCode               byte
	token                   int64
	resultBool, reuse, lock bool
}

func bootstrapStartInput(q bootstrapServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"join": 0, "lock": 1, "running": 2, "probe": 3, "handover": 4, "identity": 5, "owner": 6, "identify": 7, "proxy": 8, "clear-owner": 9, "portable": 10, "write-record": 11, "remove-record": 12, "windows": 13, "android-start": 14, "android-stop": 15, "jni-start": 16, "tunnel": 17}[q.Op]
	if !ok {
		return nil, errors.New("unsupported bootstrap operation")
	}
	b := []byte{op}
	b = registryBool(b, q.Elevated)
	b = registryBool(b, q.Enable)
	b = registryBool(b, q.Authorized)
	b = registryInt(b, int64(q.Wait))
	b = registryInt(b, int64(q.ShutdownWait))
	return b, nil
}
func decodeBootstrapFrame(b []byte) (bootstrapFrame, error) {
	r := quotaReader{b: b}
	f := bootstrapFrame{kind: r.byte()}
	f.effect.Count = int(r.integer())
	f.effect.Value = r.integer()
	f.effect.Flag = serviceReadBool(&r)
	f.effect.Duration = time.Duration(r.integer())
	f.errorCode = r.byte()
	f.token = r.integer()
	f.resultBool = serviceReadBool(&r)
	f.reuse = serviceReadBool(&r)
	f.lock = serviceReadBool(&r)
	return f, serviceWireDone(&r)
}

type bootstrapArena struct {
	q    bootstrapServiceRequest
	errs [256]error
}

func newBootstrapArena(q bootstrapServiceRequest) *bootstrapArena { return &bootstrapArena{q: q} }
func (a *bootstrapArena) replyBytes(f bootstrapFrame, r bootstrapServiceReply) ([]byte, error) {
	b := []byte{f.kind}
	for _, v := range []bool{r.Err == nil, r.Flag, r.Other, r.Elevated, r.Connected} {
		b = registryBool(b, v)
	}
	for _, v := range []int64{int64(r.Count), r.Value, r.OtherValue} {
		b = registryInt(b, v)
	}
	token := int64(0)
	if r.Err != nil {
		a.errs[f.kind] = r.Err
		token = int64(f.kind)
	}
	b = registryInt(b, token)
	return b, nil
}
func (a *bootstrapArena) port(f bootstrapFrame) (bootstrapServiceEffect, bool) {
	e := f.effect
	k, ok := bootstrapPortKinds[f.kind]
	e.Kind = k
	return e, ok
}
func (a *bootstrapArena) resultMatches(f bootstrapFrame, r bootstrapServiceResult) bool {
	if r.Bool != f.resultBool || r.Reuse != f.reuse || r.Lock != f.lock {
		return false
	}
	if f.errorCode == 0 {
		return r.Err == nil
	}
	if f.errorCode != 1 || f.token <= 0 || f.token >= 256 {
		return false
	}
	want := a.errs[f.token]
	// Error tokens denote the exact Go error resource, not merely errors.Is equivalence.
	if r.Err == nil || want == nil || reflect.TypeOf(r.Err) != reflect.TypeOf(want) || !reflect.TypeOf(want).Comparable() {
		return false
	}
	return r.Err == want
}

var bootstrapPortKinds = map[byte]string{79: "proxy-script-fallback", 1: "failure", 2: "executable", 3: "executable-stat", 4: "record-valid", 5: "service-request", 6: "body-close", 7: "service-read", 8: "service-decode", 9: "record-read", 10: "record-decode", 11: "service-facts", 12: "lock-open", 13: "deadline", 14: "lock-try", 15: "lock-acquired", 16: "lock-close", 17: "before", 18: "sleep", 19: "lock-release", 20: "log", 21: "identify-client", 22: "idle-close", 23: "identify-request", 24: "response-status", 25: "identify-decode", 26: "proxy-state", 27: "owner-read", 28: "owner-address", 29: "owner-key", 30: "owner-key-close", 31: "owner-delete", 32: "proxy-script", 33: "proxy-port", 34: "proxy-execute", 35: "owner-write", 36: "bundle-present", 37: "portable-stat", 38: "portable-mkdir", 39: "portable-write", 40: "portable-remove", 41: "portable-select", 42: "record-encode", 43: "record-temp", 44: "record-rename", 45: "record-own", 46: "record-remove", 47: "android-home", 48: "config-home", 49: "bundle-dir", 50: "default-home", 51: "home-mkdir", 52: "open-log", 53: "elevated", 54: "announce", 55: "new-service", 56: "prepare", 57: "token", 58: "serve", 59: "android-publish", 60: "record-build", 61: "signal-wait", 62: "server-close", 63: "cleanup", 64: "service-lock", 65: "android-detach", 66: "service-unlock", 67: "app-lock", 68: "drop", 69: "app-unlock", 70: "bridge-init", 71: "socket-hook", 72: "android-current", 73: "android-reuse", 74: "tunnel-decode", 75: "tunnel-grant", 76: "tunnel-release", 77: "permission", 78: "kernel-running"}
