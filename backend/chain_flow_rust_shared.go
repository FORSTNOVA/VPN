//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var chainFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustChainFlowAvailable() bool {
	chainFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_chain_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		chainFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_chain_flow_start_v1")
		chainFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_chain_flow_next_v1")
		chainFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_chain_flow_advance_v1")
		chainFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_chain_flow_close_v1")
	})
	return chainFlowRust.start != 0 && chainFlowRust.next != 0 && chainFlowRust.advance != 0 && chainFlowRust.close != 0
}

type rustChainFlowSession struct{ id uint64 }

func startRustChainFlow(input []byte) (chainFlowSession, error) {
	if !rustChainFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("chain flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(chainFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("chain flow start failed")
	}
	return &rustChainFlowSession{id}, nil
}
func (s *rustChainFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(chainFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(chainFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustChainFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("chain flow reply too large")
	}
	status, _, _ := nativeCall(chainFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("chain flow advance rejected")
	}
	return nil
}
func (s *rustChainFlowSession) close() {
	if s.id != 0 {
		nativeCall(chainFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustChainService(r chainServiceRequest, fx chainServiceEffects) (chainServiceResult, bool) {
	return runRustChainServiceWithStart(r, fx, startRustChainFlow)
}
func runSelectedChainService(r chainServiceRequest, fx chainServiceEffects) chainServiceResult {
	if os.Getenv("SMARTVPN_RUST_CHAIN_FLOW") == "1" {
		if result, used := runRustChainService(r, fx); used {
			return result
		}
	}
	return runGoChainService(r, fx)
}
