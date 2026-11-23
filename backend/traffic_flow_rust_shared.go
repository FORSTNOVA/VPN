//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var trafficFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustTrafficFlowAvailable() bool {
	trafficFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_traffic_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		trafficFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_flow_start_v1")
		trafficFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_flow_next_v1")
		trafficFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_flow_advance_v1")
		trafficFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_flow_close_v1")
	})
	return trafficFlowRust.start != 0 && trafficFlowRust.next != 0 && trafficFlowRust.advance != 0 && trafficFlowRust.close != 0
}

type rustTrafficFlowSession struct{ id uint64 }

func startRustTrafficFlow(input []byte) (trafficFlowSession, error) {
	if !rustTrafficFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("traffic flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(trafficFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("traffic flow start failed")
	}
	return &rustTrafficFlowSession{id}, nil
}
func (s *rustTrafficFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(trafficFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(trafficFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustTrafficFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("traffic flow reply too large")
	}
	status, _, _ := nativeCall(trafficFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("traffic flow advance rejected")
	}
	return nil
}
func (s *rustTrafficFlowSession) close() {
	if s.id != 0 {
		nativeCall(trafficFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustTrafficService(r trafficServiceRequest, fx trafficServiceEffects) (trafficServiceResult, bool) {
	return runRustTrafficServiceWithStart(r, fx, startRustTrafficFlow)
}
func runSelectedTrafficService(r trafficServiceRequest, fx trafficServiceEffects) trafficServiceResult {
	if os.Getenv("SMARTVPN_RUST_TRAFFIC_FLOW") == "1" {
		if result, used := runRustTrafficService(r, fx); used {
			return result
		}
	}
	return runGoTrafficService(r, fx)
}
