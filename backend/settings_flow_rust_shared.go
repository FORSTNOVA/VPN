//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var settingsFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustSettingsFlowAvailable() bool {
	settingsFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_settings_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		settingsFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_settings_flow_start_v1")
		settingsFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_settings_flow_next_v1")
		settingsFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_settings_flow_advance_v1")
		settingsFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_settings_flow_close_v1")
	})
	return settingsFlowRust.start != 0 && settingsFlowRust.next != 0 && settingsFlowRust.advance != 0 && settingsFlowRust.close != 0
}

type rustSettingsFlowSession struct{ id uint64 }

func startRustSettingsFlow(input []byte) (settingsFlowSession, error) {
	if !rustSettingsFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("settings flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(settingsFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("settings flow start failed")
	}
	return &rustSettingsFlowSession{id}, nil
}
func (s *rustSettingsFlowSession) next() ([]byte, uint64, error) {
	var out [128]byte
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(settingsFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(&out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("settings flow next failed")
	}
	return append([]byte(nil), out[:written]...), step, nil
}
func (s *rustSettingsFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("settings flow reply too large")
	}
	status, _, _ := nativeCall(settingsFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("settings flow advance rejected")
	}
	return nil
}
func (s *rustSettingsFlowSession) close() {
	if s.id != 0 {
		nativeCall(settingsFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustSettingsService(s *settingsServiceState, r settingsServiceRequest, fx settingsServiceEffects) (settingsServiceResult, bool) {
	return runRustSettingsServiceWithStart(s, r, fx, startRustSettingsFlow)
}
func runSelectedSettingsService(s *settingsServiceState, r settingsServiceRequest, fx settingsServiceEffects) settingsServiceResult {
	if os.Getenv("SMARTVPN_RUST_SETTINGS_FLOW") == "1" {
		if result, used := runRustSettingsService(s, r, fx); used {
			return result
		}
	}
	return runGoSettingsService(s, r, fx)
}
