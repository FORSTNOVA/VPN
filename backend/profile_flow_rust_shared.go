//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var profileFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustProfileFlowAvailable() bool {
	profileFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_profile_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		profileFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_profile_flow_start_v1")
		profileFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_profile_flow_next_v1")
		profileFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_profile_flow_advance_v1")
		profileFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_profile_flow_close_v1")
	})
	return profileFlowRust.start != 0 && profileFlowRust.next != 0 && profileFlowRust.advance != 0 && profileFlowRust.close != 0
}

type rustProfileFlowSession struct{ id uint64 }

func startRustProfileFlow(input []byte) (profileFlowSession, error) {
	if !rustProfileFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("profile flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(profileFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("profile flow start failed")
	}
	return &rustProfileFlowSession{id}, nil
}
func (s *rustProfileFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(profileFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(profileFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustProfileFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("profile flow reply too large")
	}
	status, _, _ := nativeCall(profileFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("profile flow advance rejected")
	}
	return nil
}
func (s *rustProfileFlowSession) close() {
	if s.id != 0 {
		nativeCall(profileFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustProfileService(s *profileServiceState, r profileServiceRequest, fx profileServiceEffects) (profileServiceResult, bool) {
	return runRustProfileServiceWithStart(s, r, fx, startRustProfileFlow)
}
func runSelectedProfileService(s *profileServiceState, r profileServiceRequest, fx profileServiceEffects) profileServiceResult {
	if os.Getenv("SMARTVPN_RUST_PROFILE_FLOW") == "1" {
		if result, used := runRustProfileService(s, r, fx); used {
			return result
		}
	}
	return runGoProfileService(s, r, fx)
}
