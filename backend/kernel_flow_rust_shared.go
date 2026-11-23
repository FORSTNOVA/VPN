//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

var kernelFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustKernelFlowAvailable() bool {
	kernelFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_kernel_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		kernelFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_flow_start_v1")
		kernelFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_flow_next_v1")
		kernelFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_flow_advance_v1")
		kernelFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_kernel_flow_close_v1")
	})
	return kernelFlowRust.start != 0 && kernelFlowRust.next != 0 && kernelFlowRust.advance != 0 && kernelFlowRust.close != 0
}

type rustKernelFlowSession struct{ id uint64 }

func startRustKernelFlow(input []byte) (kernelFlowSession, error) {
	if !rustKernelFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("kernel flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(kernelFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("kernel flow start failed")
	}
	return &rustKernelFlowSession{id}, nil
}
func (s *rustKernelFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(kernelFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(kernelFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustKernelFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("kernel flow reply too large")
	}
	status, _, _ := nativeCall(kernelFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("kernel flow advance rejected")
	}
	return nil
}
func (s *rustKernelFlowSession) close() {
	if s.id != 0 {
		nativeCall(kernelFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustKernelService(r kernelServiceRequest, fx kernelServiceEffects) (kernelServiceResult, bool) {
	return runRustKernelServiceWithStart(r, fx, startRustKernelFlow)
}
func runSelectedKernelService(r kernelServiceRequest, fx kernelServiceEffects) kernelServiceResult {
	if windowsKernelFlowSelected("SMARTVPN_RUST_KERNEL_FLOW") {
		if result, used := runRustKernelService(r, fx); used {
			return result
		}
	}
	return runGoKernelService(r, fx)
}
