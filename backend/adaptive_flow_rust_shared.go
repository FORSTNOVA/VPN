//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var adaptiveFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustAdaptiveFlowAvailable() bool {
	adaptiveFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_adaptive_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		adaptiveFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_adaptive_flow_start_v1")
		adaptiveFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_adaptive_flow_next_v1")
		adaptiveFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_adaptive_flow_advance_v1")
		adaptiveFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_adaptive_flow_close_v1")
	})
	return adaptiveFlowRust.start != 0 && adaptiveFlowRust.next != 0 && adaptiveFlowRust.advance != 0 && adaptiveFlowRust.close != 0
}

type rustAdaptiveFlowSession struct{ id uint64 }

func startRustAdaptiveFlow(input []byte) (adaptiveFlowSession, error) {
	if !rustAdaptiveFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("adaptive flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(adaptiveFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("adaptive flow start failed")
	}
	return &rustAdaptiveFlowSession{id}, nil
}
func (s *rustAdaptiveFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(adaptiveFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("adaptive frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(adaptiveFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("adaptive next rejected")
	}
	return out[:written], step, nil
}
func (s *rustAdaptiveFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("adaptive flow reply too large")
	}
	status, _, _ := nativeCall(adaptiveFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("adaptive flow advance rejected")
	}
	return nil
}
func (s *rustAdaptiveFlowSession) close() {
	if s.id != 0 {
		nativeCall(adaptiveFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustAdaptiveService(r adaptiveServiceRequest, fx adaptiveServiceEffects) (adaptiveServiceResult, bool) {
	return runRustAdaptiveServiceWithStart(r, fx, startRustAdaptiveFlow)
}
func runSelectedAdaptiveService(r adaptiveServiceRequest, fx adaptiveServiceEffects) adaptiveServiceResult {
	if os.Getenv("SMARTVPN_RUST_ADAPTIVE_FLOW") == "1" {
		if result, used := runRustAdaptiveService(r, fx); used {
			return result
		}
	}
	return runGoAdaptiveService(r, fx)
}
