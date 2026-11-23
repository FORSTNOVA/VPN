//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var pathMonitorFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustPathMonitorFlowAvailable() bool {
	pathMonitorFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_path_monitor_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 2 {
			return
		}
		pathMonitorFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_path_monitor_flow_start_v1")
		pathMonitorFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_path_monitor_flow_next_v1")
		pathMonitorFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_path_monitor_flow_advance_v1")
		pathMonitorFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_path_monitor_flow_close_v1")
	})
	return pathMonitorFlowRust.start != 0 && pathMonitorFlowRust.next != 0 && pathMonitorFlowRust.advance != 0 && pathMonitorFlowRust.close != 0
}

type rustPathMonitorFlowSession struct{ id uint64 }

func startRustPathMonitorFlow(input []byte) (pathMonitorFlowSession, error) {
	if !rustPathMonitorFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("pathMonitor flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(pathMonitorFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("pathMonitor flow start failed")
	}
	return &rustPathMonitorFlowSession{id}, nil
}
func (s *rustPathMonitorFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(pathMonitorFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(pathMonitorFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustPathMonitorFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("pathMonitor flow reply too large")
	}
	status, _, _ := nativeCall(pathMonitorFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("pathMonitor flow advance rejected")
	}
	return nil
}
func (s *rustPathMonitorFlowSession) close() {
	if s.id != 0 {
		nativeCall(pathMonitorFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustPathMonitorService(r pathMonitorServiceRequest, fx pathMonitorServiceEffects) (pathMonitorServiceResult, bool) {
	return runRustPathMonitorServiceWithStart(r, fx, startRustPathMonitorFlow)
}
func runSelectedPathMonitorService(r pathMonitorServiceRequest, fx pathMonitorServiceEffects) pathMonitorServiceResult {
	if os.Getenv("SMARTVPN_RUST_PATH_MONITOR_FLOW") == "1" {
		if result, used := runRustPathMonitorService(r, fx); used {
			return result
		}
	}
	return runGoPathMonitorService(r, fx)
}
