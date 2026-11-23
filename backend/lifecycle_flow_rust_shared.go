//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var lifecycleFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustLifecycleFlowAvailable() bool {
	lifecycleFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_lifecycle_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		lifecycleFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_lifecycle_flow_start_v1")
		lifecycleFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_lifecycle_flow_next_v1")
		lifecycleFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_lifecycle_flow_advance_v1")
		lifecycleFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_lifecycle_flow_close_v1")
	})
	return lifecycleFlowRust.start != 0 && lifecycleFlowRust.next != 0 && lifecycleFlowRust.advance != 0 && lifecycleFlowRust.close != 0
}

type rustLifecycleFlowSession struct{ id uint64 }

func startRustLifecycleFlow(input []byte) (lifecycleFlowSession, error) {
	if !rustLifecycleFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("lifecycle flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(lifecycleFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("lifecycle flow start failed")
	}
	return &rustLifecycleFlowSession{id}, nil
}
func (s *rustLifecycleFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(lifecycleFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(lifecycleFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustLifecycleFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("lifecycle flow reply too large")
	}
	status, _, _ := nativeCall(lifecycleFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("lifecycle flow advance rejected")
	}
	return nil
}
func (s *rustLifecycleFlowSession) close() {
	if s.id != 0 {
		nativeCall(lifecycleFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustLifecycleService(r lifecycleServiceRequest, fx lifecycleServiceEffects) (lifecycleServiceResult, bool) {
	return runRustLifecycleServiceWithStart(r, fx, startRustLifecycleFlow)
}
func runSelectedLifecycleService(r lifecycleServiceRequest, fx lifecycleServiceEffects) lifecycleServiceResult {
	if os.Getenv("SMARTVPN_RUST_LIFECYCLE_FLOW") == "1" {
		if result, used := runRustLifecycleService(r, fx); used {
			return result
		}
	}
	return runGoLifecycleService(r, fx)
}
