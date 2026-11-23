//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var manualFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustManualFlowAvailable() bool {
	manualFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_manual_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		manualFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_manual_flow_start_v1")
		manualFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_manual_flow_next_v1")
		manualFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_manual_flow_advance_v1")
		manualFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_manual_flow_close_v1")
	})
	return manualFlowRust.start != 0 && manualFlowRust.next != 0 && manualFlowRust.advance != 0 && manualFlowRust.close != 0
}

type rustManualFlowSession struct{ id uint64 }

func startRustManualFlow(input []byte) (manualFlowSession, error) {
	if !rustManualFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("manual flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(manualFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("manual flow start failed")
	}
	return &rustManualFlowSession{id}, nil
}
func (s *rustManualFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(manualFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(manualFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustManualFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("manual flow reply too large")
	}
	status, _, _ := nativeCall(manualFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("manual flow advance rejected")
	}
	return nil
}
func (s *rustManualFlowSession) close() {
	if s.id != 0 {
		nativeCall(manualFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustManualService(r manualServiceRequest, fx manualServiceEffects) (manualServiceResult, bool) {
	return runRustManualServiceWithStart(r, fx, startRustManualFlow)
}
func runSelectedManualService(r manualServiceRequest, fx manualServiceEffects) manualServiceResult {
	if os.Getenv("SMARTVPN_RUST_MANUAL_FLOW") == "1" {
		if result, used := runRustManualService(r, fx); used {
			return result
		}
	}
	return runGoManualService(r, fx)
}
