//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var diagnosticFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustDiagnosticFlowAvailable() bool {
	diagnosticFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_diagnostic_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		diagnosticFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_diagnostic_flow_start_v1")
		diagnosticFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_diagnostic_flow_next_v1")
		diagnosticFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_diagnostic_flow_advance_v1")
		diagnosticFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_diagnostic_flow_close_v1")
	})
	return diagnosticFlowRust.start != 0 && diagnosticFlowRust.next != 0 && diagnosticFlowRust.advance != 0 && diagnosticFlowRust.close != 0
}

type rustDiagnosticFlowSession struct{ id uint64 }

func startRustDiagnosticFlow(input []byte) (diagnosticFlowSession, error) {
	if !rustDiagnosticFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("diagnostic flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(diagnosticFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("diagnostic flow start failed")
	}
	return &rustDiagnosticFlowSession{id}, nil
}
func (s *rustDiagnosticFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(diagnosticFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(diagnosticFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustDiagnosticFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("diagnostic flow reply too large")
	}
	status, _, _ := nativeCall(diagnosticFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("diagnostic flow advance rejected")
	}
	return nil
}
func (s *rustDiagnosticFlowSession) close() {
	if s.id != 0 {
		nativeCall(diagnosticFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustDiagnoseService(r diagnoseServiceRequest, fx diagnoseServiceEffects) (diagnoseServiceResult, bool) {
	return runRustDiagnoseServiceWithStart(r, fx, startRustDiagnosticFlow)
}
func runSelectedDiagnoseService(r diagnoseServiceRequest, fx diagnoseServiceEffects) diagnoseServiceResult {
	if os.Getenv("SMARTVPN_RUST_DIAGNOSTIC_FLOW") == "1" {
		if result, used := runRustDiagnoseService(r, fx); used {
			return result
		}
	}
	return runGoDiagnoseService(r, fx)
}
