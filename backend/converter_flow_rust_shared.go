//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var converterFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustConverterFlowAvailable() bool {
	converterFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_converter_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 2 {
			return
		}
		converterFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_converter_flow_start_v1")
		converterFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_converter_flow_next_v1")
		converterFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_converter_flow_advance_v1")
		converterFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_converter_flow_close_v1")
	})
	return converterFlowRust.start != 0 && converterFlowRust.next != 0 && converterFlowRust.advance != 0 && converterFlowRust.close != 0
}

type rustConverterFlowSession struct{ id uint64 }

func startRustConverterFlow(input []byte) (converterFlowSession, error) {
	if !rustConverterFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("converter flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(converterFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("converter flow start failed")
	}
	return &rustConverterFlowSession{id}, nil
}
func (s *rustConverterFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(converterFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("converter frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(converterFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("converter next rejected")
	}
	return out[:written], step, nil
}
func (s *rustConverterFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("converter flow reply too large")
	}
	status, _, _ := nativeCall(converterFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("converter flow advance rejected")
	}
	return nil
}
func (s *rustConverterFlowSession) close() {
	if s.id != 0 {
		nativeCall(converterFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustConverterService(r converterServiceRequest, fx converterServiceEffects) (converterServiceResult, bool) {
	return runRustConverterServiceWithStart(r, fx, startRustConverterFlow)
}
func runSelectedConverterService(r converterServiceRequest, fx converterServiceEffects) converterServiceResult {
	if os.Getenv("SMARTVPN_RUST_CONVERTER_FLOW") == "1" {
		if result, used := runRustConverterService(r, fx); used {
			return result
		}
	}
	return runGoConverterService(r, fx)
}
