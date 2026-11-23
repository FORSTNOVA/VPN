//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

var connectionFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustConnectionFlowAvailable() bool {
	connectionFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_connection_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		connectionFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_connection_flow_start_v1")
		connectionFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_connection_flow_next_v1")
		connectionFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_connection_flow_advance_v1")
		connectionFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_connection_flow_close_v1")
	})
	return connectionFlowRust.start != 0 && connectionFlowRust.next != 0 && connectionFlowRust.advance != 0 && connectionFlowRust.close != 0
}

type rustConnectionFlowSession struct{ id uint64 }

func startRustConnectionFlow(input []byte) (connectionFlowSession, error) {
	if !rustConnectionFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("connection flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(connectionFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("connection flow start failed")
	}
	return &rustConnectionFlowSession{id}, nil
}
func (s *rustConnectionFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(connectionFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(connectionFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustConnectionFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("connection flow reply too large")
	}
	status, _, _ := nativeCall(connectionFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("connection flow advance rejected")
	}
	return nil
}
func (s *rustConnectionFlowSession) close() {
	if s.id != 0 {
		nativeCall(connectionFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustConnectionService(r connectionServiceRequest, fx connectionServiceEffects) (connectionServiceResult, bool) {
	return runRustConnectionServiceWithStart(r, fx, startRustConnectionFlow)
}
func runSelectedConnectionService(r connectionServiceRequest, fx connectionServiceEffects) connectionServiceResult {
	if windowsKernelFlowSelected("SMARTVPN_RUST_CONNECTION_FLOW") {
		if result, used := runRustConnectionService(r, fx); used {
			return result
		}
	}
	return runGoConnectionService(r, fx)
}
