//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var labelsFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustLabelsFlowAvailable() bool {
	labelsFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_labels_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		labelsFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_labels_flow_start_v1")
		labelsFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_labels_flow_next_v1")
		labelsFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_labels_flow_advance_v1")
		labelsFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_labels_flow_close_v1")
	})
	return labelsFlowRust.start != 0 && labelsFlowRust.next != 0 && labelsFlowRust.advance != 0 && labelsFlowRust.close != 0
}

type rustLabelsFlowSession struct{ id uint64 }

func startRustLabelsFlow(input []byte) (labelsFlowSession, error) {
	if !rustLabelsFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("labels flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(labelsFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("labels flow start failed")
	}
	return &rustLabelsFlowSession{id}, nil
}
func (s *rustLabelsFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(labelsFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(labelsFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustLabelsFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("labels flow reply too large")
	}
	status, _, _ := nativeCall(labelsFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("labels flow advance rejected")
	}
	return nil
}
func (s *rustLabelsFlowSession) close() {
	if s.id != 0 {
		nativeCall(labelsFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustLabelsService(r labelsServiceRequest, fx labelsServiceEffects) (labelsServiceResult, bool) {
	return runRustLabelsServiceWithStart(r, fx, startRustLabelsFlow)
}
func runSelectedLabelsService(r labelsServiceRequest, fx labelsServiceEffects) labelsServiceResult {
	if os.Getenv("SMARTVPN_RUST_LABELS_FLOW") == "1" {
		if result, used := runRustLabelsService(r, fx); used {
			return result
		}
	}
	return runGoLabelsService(r, fx)
}
