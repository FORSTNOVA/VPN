//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var regionFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustRegionFlowAvailable() bool {
	regionFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_region_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		regionFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_region_flow_start_v1")
		regionFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_region_flow_next_v1")
		regionFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_region_flow_advance_v1")
		regionFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_region_flow_close_v1")
	})
	return regionFlowRust.start != 0 && regionFlowRust.next != 0 && regionFlowRust.advance != 0 && regionFlowRust.close != 0
}

type rustRegionFlowSession struct{ id uint64 }

func startRustRegionFlow(input []byte) (regionFlowSession, error) {
	if !rustRegionFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("region flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(regionFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("region flow start failed")
	}
	return &rustRegionFlowSession{id}, nil
}
func (s *rustRegionFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(regionFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(regionFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustRegionFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("region flow reply too large")
	}
	status, _, _ := nativeCall(regionFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("region flow advance rejected")
	}
	return nil
}
func (s *rustRegionFlowSession) close() {
	if s.id != 0 {
		nativeCall(regionFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustRegionService(r regionServiceRequest, fx regionServiceEffects) (regionServiceResult, bool) {
	return runRustRegionServiceWithStart(r, fx, startRustRegionFlow)
}
func runSelectedRegionService(r regionServiceRequest, fx regionServiceEffects) regionServiceResult {
	if os.Getenv("SMARTVPN_RUST_REGION_FLOW") == "1" {
		if result, used := runRustRegionService(r, fx); used {
			return result
		}
	}
	return runGoRegionService(r, fx)
}
