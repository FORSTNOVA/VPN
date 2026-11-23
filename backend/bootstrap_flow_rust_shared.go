//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var bootstrapFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustBootstrapFlowAvailable() bool {
	bootstrapFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_bootstrap_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		bootstrapFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_bootstrap_flow_start_v1")
		bootstrapFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_bootstrap_flow_next_v1")
		bootstrapFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_bootstrap_flow_advance_v1")
		bootstrapFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_bootstrap_flow_close_v1")
	})
	return bootstrapFlowRust.start != 0 && bootstrapFlowRust.next != 0 && bootstrapFlowRust.advance != 0 && bootstrapFlowRust.close != 0
}

type rustBootstrapFlowSession struct{ id uint64 }

func startRustBootstrapFlow(input []byte) (bootstrapFlowSession, error) {
	if !rustBootstrapFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("bootstrap flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(bootstrapFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("bootstrap flow start failed")
	}
	return &rustBootstrapFlowSession{id}, nil
}
func (s *rustBootstrapFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(bootstrapFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("bootstrap frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(bootstrapFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("bootstrap next rejected")
	}
	return out[:written], step, nil
}
func (s *rustBootstrapFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("bootstrap flow reply too large")
	}
	status, _, _ := nativeCall(bootstrapFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("bootstrap flow advance rejected")
	}
	return nil
}
func (s *rustBootstrapFlowSession) close() {
	if s.id != 0 {
		nativeCall(bootstrapFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustBootstrapService(r bootstrapServiceRequest, fx bootstrapServiceEffects) (bootstrapServiceResult, bool) {
	return runRustBootstrapServiceWithStart(r, fx, startRustBootstrapFlow)
}
func runSelectedBootstrapService(r bootstrapServiceRequest, fx bootstrapServiceEffects) bootstrapServiceResult {
	if (windowsKernelFlowSelected("SMARTVPN_RUST_BOOTSTRAP_FLOW") && r.Op == "proxy") || os.Getenv("SMARTVPN_RUST_BOOTSTRAP_FLOW") == "1" {
		if result, used := runRustBootstrapService(r, fx); used {
			return result
		}
	}
	return runGoBootstrapService(r, fx)
}
