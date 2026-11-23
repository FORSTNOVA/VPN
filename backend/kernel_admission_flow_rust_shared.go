//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

var kernelAdmissionFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustKernelAdmissionFlowAvailable() bool {
	kernelAdmissionFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_kernel_admission_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		kernelAdmissionFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_admission_flow_start_v1")
		kernelAdmissionFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_admission_flow_next_v1")
		kernelAdmissionFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_admission_flow_advance_v1")
		kernelAdmissionFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_admission_flow_close_v1")
	})
	return kernelAdmissionFlowRust.start != 0 && kernelAdmissionFlowRust.next != 0 && kernelAdmissionFlowRust.advance != 0 && kernelAdmissionFlowRust.close != 0
}

type rustKernelAdmissionFlowSession struct{ id uint64 }

func startRustKernelAdmissionFlow(input []byte) (kernelAdmissionFlowSession, error) {
	if !rustKernelAdmissionFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("kernelAdmission flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(kernelAdmissionFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("kernelAdmission flow start failed")
	}
	return &rustKernelAdmissionFlowSession{id}, nil
}
func (s *rustKernelAdmissionFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(kernelAdmissionFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("kernelAdmission frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(kernelAdmissionFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("kernelAdmission next rejected")
	}
	return out[:written], step, nil
}
func (s *rustKernelAdmissionFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("kernelAdmission flow reply too large")
	}
	status, _, _ := nativeCall(kernelAdmissionFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("kernelAdmission flow advance rejected")
	}
	return nil
}
func (s *rustKernelAdmissionFlowSession) close() {
	if s.id != 0 {
		nativeCall(kernelAdmissionFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustKernelAdmissionService(r kernelAdmissionRequest, fx kernelAdmissionEffects) (kernelAdmissionResult, bool) {
	return runRustKernelAdmissionServiceWithStart(r, fx, startRustKernelAdmissionFlow)
}
func runSelectedKernelAdmissionService(r kernelAdmissionRequest, fx kernelAdmissionEffects) kernelAdmissionResult {
	if windowsKernelFlowSelected("SMARTVPN_RUST_KERNEL_ADMISSION_FLOW") {
		if result, used := runRustKernelAdmissionService(r, fx); used {
			return result
		}
	}
	return runGoKernelAdmissionService(r, fx)
}
