//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var healthFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustHealthFlowAvailable() bool {
	healthFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_health_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		healthFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_health_flow_start_v1")
		healthFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_health_flow_next_v1")
		healthFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_health_flow_advance_v1")
		healthFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_health_flow_close_v1")
	})
	return healthFlowRust.start != 0 && healthFlowRust.next != 0 && healthFlowRust.advance != 0 && healthFlowRust.close != 0
}

type rustHealthFlowSession struct{ id uint64 }

func startRustHealthFlow(input []byte) (healthFlowSession, error) {
	if !rustHealthFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("health flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(healthFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("health flow start failed")
	}
	return &rustHealthFlowSession{id}, nil
}
func (s *rustHealthFlowSession) next() ([]byte, uint64, error) {
	var out [128]byte
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(healthFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(&out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("health flow next failed")
	}
	return append([]byte(nil), out[:written]...), step, nil
}
func (s *rustHealthFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("health flow reply too large")
	}
	status, _, _ := nativeCall(healthFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("health flow advance rejected")
	}
	return nil
}
func (s *rustHealthFlowSession) close() {
	if s.id != 0 {
		nativeCall(healthFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustHealthService(s *healthServiceState, r healthServiceRequest, fx healthServiceEffects) bool {
	return runRustHealthServiceWithStart(s, r, fx, startRustHealthFlow)
}
func runSelectedHealthService(s *healthServiceState, r healthServiceRequest, fx healthServiceEffects) {
	if os.Getenv("SMARTVPN_RUST_HEALTH_FLOW") == "1" && runRustHealthService(s, r, fx) {
		return
	}
	runGoHealthService(s, r, fx)
}
