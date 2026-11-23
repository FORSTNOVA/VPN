//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var subscriptionFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustSubscriptionFlowAvailable() bool {
	subscriptionFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_registry_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		subscriptionFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_registry_flow_start_v1")
		subscriptionFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_registry_flow_next_v1")
		subscriptionFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_registry_flow_advance_v1")
		subscriptionFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_registry_flow_close_v1")
	})
	return subscriptionFlowRust.start != 0 && subscriptionFlowRust.next != 0 && subscriptionFlowRust.advance != 0 && subscriptionFlowRust.close != 0
}

type rustSubscriptionFlowSession struct{ id uint64 }

func startRustSubscriptionFlow(input []byte) (subscriptionFlowSession, error) {
	if !rustSubscriptionFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("subscription flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(subscriptionFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("subscription flow start failed")
	}
	return &rustSubscriptionFlowSession{id}, nil
}
func (s *rustSubscriptionFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(subscriptionFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(subscriptionFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustSubscriptionFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("subscription flow reply too large")
	}
	status, _, _ := nativeCall(subscriptionFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("subscription flow advance rejected")
	}
	return nil
}
func (s *rustSubscriptionFlowSession) close() {
	if s.id != 0 {
		nativeCall(subscriptionFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustSubscriptionService(s *subscriptionServiceState, r subscriptionServiceRequest, fx subscriptionServiceEffects) (subscriptionServiceResult, bool) {
	return runRustSubscriptionServiceWithStart(s, r, fx, startRustSubscriptionFlow)
}
func runSelectedSubscriptionService(s *subscriptionServiceState, r subscriptionServiceRequest, fx subscriptionServiceEffects) subscriptionServiceResult {
	if os.Getenv("SMARTVPN_RUST_SUBSCRIPTION_FLOW") == "1" {
		if result, used := runRustSubscriptionService(s, r, fx); used {
			return result
		}
	}
	return runGoSubscriptionService(s, r, fx)
}
