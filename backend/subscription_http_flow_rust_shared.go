//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var subscriptionHTTPFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustSubscriptionHTTPFlowAvailable() bool {
	subscriptionHTTPFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_subscription_http_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		subscriptionHTTPFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_subscription_http_flow_start_v1")
		subscriptionHTTPFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_subscription_http_flow_next_v1")
		subscriptionHTTPFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_subscription_http_flow_advance_v1")
		subscriptionHTTPFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_subscription_http_flow_close_v1")
	})
	return subscriptionHTTPFlowRust.start != 0 && subscriptionHTTPFlowRust.next != 0 && subscriptionHTTPFlowRust.advance != 0 && subscriptionHTTPFlowRust.close != 0
}

type rustSubscriptionHTTPFlowSession struct{ id uint64 }

func startRustSubscriptionHTTPFlow(input []byte) (subscriptionHTTPFlowSession, error) {
	if !rustSubscriptionHTTPFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("subscriptionHTTP flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(subscriptionHTTPFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("subscriptionHTTP flow start failed")
	}
	return &rustSubscriptionHTTPFlowSession{id}, nil
}
func (s *rustSubscriptionHTTPFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(subscriptionHTTPFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscriptionHTTP frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(subscriptionHTTPFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscriptionHTTP next rejected")
	}
	return out[:written], step, nil
}
func (s *rustSubscriptionHTTPFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("subscriptionHTTP flow reply too large")
	}
	status, _, _ := nativeCall(subscriptionHTTPFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("subscriptionHTTP flow advance rejected")
	}
	return nil
}
func (s *rustSubscriptionHTTPFlowSession) close() {
	if s.id != 0 {
		nativeCall(subscriptionHTTPFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustSubscriptionHTTPService(r subscriptionHTTPServiceRequest, fx subscriptionHTTPServiceEffects) (subscriptionHTTPServiceResult, bool) {
	return runRustSubscriptionHTTPServiceWithStart(r, fx, startRustSubscriptionHTTPFlow)
}
func runSelectedSubscriptionHTTPService(r subscriptionHTTPServiceRequest, fx subscriptionHTTPServiceEffects) subscriptionHTTPServiceResult {
	if os.Getenv("SMARTVPN_RUST_SUBSCRIPTION_HTTP_FLOW") == "1" {
		if result, used := runRustSubscriptionHTTPService(r, fx); used {
			return result
		}
	}
	return runGoSubscriptionHTTPService(r, fx)
}
