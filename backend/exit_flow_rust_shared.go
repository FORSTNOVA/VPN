//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

var exitFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustExitFlowAvailable() bool {
	exitFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_exit_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		exitFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_exit_flow_start_v1")
		exitFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_exit_flow_next_v1")
		exitFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_exit_flow_advance_v1")
		exitFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_exit_flow_close_v1")
	})
	return exitFlowRust.start != 0 && exitFlowRust.next != 0 && exitFlowRust.advance != 0 && exitFlowRust.close != 0
}

type rustExitFlowSession struct{ id uint64 }

func startRustExitFlow(input []byte) (exitFlowSession, error) {
	if !rustExitFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("exit flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(exitFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("exit flow start failed")
	}
	return &rustExitFlowSession{id}, nil
}
func (s *rustExitFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(exitFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(exitFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustExitFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("exit flow reply too large")
	}
	status, _, _ := nativeCall(exitFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("exit flow advance rejected")
	}
	return nil
}
func (s *rustExitFlowSession) close() {
	if s.id != 0 {
		nativeCall(exitFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustExitService(r exitServiceRequest, fx exitServiceEffects) (exitServiceResult, bool) {
	return runRustExitServiceWithStart(r, fx, startRustExitFlow)
}
func runSelectedExitService(r exitServiceRequest, fx exitServiceEffects) exitServiceResult {
	if windowsKernelFlowSelected("SMARTVPN_RUST_EXIT_FLOW") {
		if result, used := runRustExitService(r, fx); used {
			return result
		}
	}
	return runGoExitService(r, fx)
}
