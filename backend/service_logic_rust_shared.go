//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"runtime"
	"sync"
	"unsafe"
)

var serviceRust struct {
	once                   sync.Once
	tail, redact, takeover uintptr
}

func serviceRustAvailable() bool {
	serviceRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		serviceRust.tail, _ = nativeLookup(rustCore.handle, "smartvpn_log_tail_v1")
		serviceRust.redact, _ = nativeLookup(rustCore.handle, "smartvpn_log_redact_v1")
		serviceRust.takeover, _ = nativeLookup(rustCore.handle, "smartvpn_service_takeover_v1")
	})
	return serviceRust.tail != 0 && serviceRust.redact != 0 && serviceRust.takeover != 0
}
func serviceTakeoverDecision(stale, connected, existingElevated, elevated bool) bool {
	if serviceRustAvailable() {
		var flags uintptr
		for i, v := range []bool{stale, connected, existingElevated, elevated} {
			if v {
				flags |= 1 << i
			}
		}
		value, _, _ := nativeCall(serviceRust.takeover, flags)
		if value <= 1 {
			return value == 1
		}
	}
	return serviceTakeoverDecisionGo(stale, connected, existingElevated, elevated)
}
func tailFromLine(body []byte, limit int) []byte {
	if limit >= 0 && len(body) <= 8<<20 && serviceRustAvailable() {
		var input *byte
		if len(body) > 0 {
			input = &body[0]
		}
		start, _, _ := nativeCall(serviceRust.tail, uintptr(unsafe.Pointer(input)), uintptr(len(body)), uintptr(limit))
		runtime.KeepAlive(body)
		if start <= uintptr(len(body)) {
			return body[start:]
		}
	}
	return tailFromLineGo(body, limit)
}
func redactLogBytes(body, secret []byte) []byte {
	if value, ok := rustRedactLogBytes(body, secret); ok {
		return value
	}
	return redactLogBytesGo(body, secret)
}
func rustRedactLogBytes(body, secret []byte) ([]byte, bool) {
	if len(secret) == 0 || len(body) > 8<<20 || len(secret) > 8<<20 || !serviceRustAvailable() {
		return nil, false
	}
	var input *byte
	if len(body) > 0 {
		input = &body[0]
	}
	output := make([]byte, max(256, len(body)))
	for attempt := 0; attempt < 2; attempt++ {
		var written uintptr
		status, _, _ := nativeCall(serviceRust.redact, uintptr(unsafe.Pointer(input)), uintptr(len(body)), uintptr(unsafe.Pointer(&secret[0])), uintptr(len(secret)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(body)
		runtime.KeepAlive(secret)
		runtime.KeepAlive(output)
		if int32(status) == 0 && written <= uintptr(len(output)) {
			return output[:written], true
		}
		if int32(status) != -2 || written > 16<<20 || written <= uintptr(len(output)) {
			return nil, false
		}
		output = make([]byte, int(written))
	}
	return nil, false
}
