//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var chinaFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustChinaFlowAvailable() bool {
	chinaFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_china_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		chinaFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_china_flow_start_v1")
		chinaFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_china_flow_next_v1")
		chinaFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_china_flow_advance_v1")
		chinaFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_china_flow_close_v1")
	})
	return chinaFlowRust.start != 0 && chinaFlowRust.next != 0 && chinaFlowRust.advance != 0 && chinaFlowRust.close != 0
}

type rustChinaFlowSession struct{ id uint64 }

func startRustChinaFlow(input []byte) (chinaFlowSession, error) {
	if !rustChinaFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("china flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(chinaFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("china flow start failed")
	}
	return &rustChinaFlowSession{id}, nil
}
func (s *rustChinaFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(chinaFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(chinaFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustChinaFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("china flow reply too large")
	}
	status, _, _ := nativeCall(chinaFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("china flow advance rejected")
	}
	return nil
}
func (s *rustChinaFlowSession) close() {
	if s.id != 0 {
		nativeCall(chinaFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustChinaService(r chinaServiceRequest, fx chinaServiceEffects) (chinaServiceResult, bool) {
	return runRustChinaServiceWithStart(r, fx, startRustChinaFlow)
}
func runSelectedChinaService(r chinaServiceRequest, fx chinaServiceEffects) chinaServiceResult {
	if os.Getenv("SMARTVPN_RUST_CHINA_FLOW") == "1" {
		if result, used := runRustChinaService(r, fx); used {
			return result
		}
	}
	return runGoChinaService(r, fx)
}
