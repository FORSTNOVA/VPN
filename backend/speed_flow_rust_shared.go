//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var speedFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustSpeedFlowAvailable() bool {
	speedFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_speed_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		speedFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_speed_flow_start_v1")
		speedFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_speed_flow_next_v1")
		speedFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_speed_flow_advance_v1")
		speedFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_speed_flow_close_v1")
	})
	return speedFlowRust.start != 0 && speedFlowRust.next != 0 && speedFlowRust.advance != 0 && speedFlowRust.close != 0
}

type rustSpeedFlowSession struct{ id uint64 }

func startRustSpeedFlow(input []byte) (speedFlowSession, error) {
	if !rustSpeedFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("speed flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(speedFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("speed flow start failed")
	}
	return &rustSpeedFlowSession{id}, nil
}
func (s *rustSpeedFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(speedFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("speed frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(speedFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("speed next rejected")
	}
	return out[:written], step, nil
}
func (s *rustSpeedFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("speed flow reply too large")
	}
	status, _, _ := nativeCall(speedFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("speed flow advance rejected")
	}
	return nil
}
func (s *rustSpeedFlowSession) close() {
	if s.id != 0 {
		nativeCall(speedFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustSpeedService(r speedServiceRequest, fx speedServiceEffects) (speedServiceResult, bool) {
	return runRustSpeedServiceWithStart(r, fx, startRustSpeedFlow)
}
func runSelectedSpeedService(r speedServiceRequest, fx speedServiceEffects) speedServiceResult {
	if os.Getenv("SMARTVPN_RUST_SPEED_FLOW") == "1" {
		if result, used := runRustSpeedService(r, fx); used {
			return result
		}
	}
	return runGoSpeedService(r, fx)
}
