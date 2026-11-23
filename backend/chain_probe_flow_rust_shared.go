//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var chainProbeFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustChainProbeFlowAvailable() bool {
	chainProbeFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_chain_probe_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		chainProbeFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_chain_probe_flow_start_v1")
		chainProbeFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_chain_probe_flow_next_v1")
		chainProbeFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_chain_probe_flow_advance_v1")
		chainProbeFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_chain_probe_flow_close_v1")
	})
	return chainProbeFlowRust.start != 0 && chainProbeFlowRust.next != 0 && chainProbeFlowRust.advance != 0 && chainProbeFlowRust.close != 0
}

type rustChainProbeFlowSession struct{ id uint64 }

func startRustChainProbeFlow(input []byte) (chainProbeFlowSession, error) {
	if !rustChainProbeFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("chainProbe flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(chainProbeFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("chainProbe flow start failed")
	}
	return &rustChainProbeFlowSession{id}, nil
}
func (s *rustChainProbeFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(chainProbeFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(chainProbeFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustChainProbeFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("chainProbe flow reply too large")
	}
	status, _, _ := nativeCall(chainProbeFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("chainProbe flow advance rejected")
	}
	return nil
}
func (s *rustChainProbeFlowSession) close() {
	if s.id != 0 {
		nativeCall(chainProbeFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustChainProbeService(r chainProbeServiceRequest, fx chainProbeServiceEffects) (chainProbeServiceResult, bool) {
	return runRustChainProbeServiceWithStart(r, fx, startRustChainProbeFlow)
}
func runSelectedChainProbeService(r chainProbeServiceRequest, fx chainProbeServiceEffects) chainProbeServiceResult {
	if os.Getenv("SMARTVPN_RUST_CHAIN_PROBE_FLOW") == "1" {
		if result, used := runRustChainProbeService(r, fx); used {
			return result
		}
	}
	return runGoChainProbeService(r, fx)
}
