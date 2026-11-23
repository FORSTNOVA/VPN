//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var regionJobFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustRegionJobFlowAvailable() bool {
	regionJobFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_region_job_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		regionJobFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_region_job_flow_start_v1")
		regionJobFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_region_job_flow_next_v1")
		regionJobFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_region_job_flow_advance_v1")
		regionJobFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_region_job_flow_close_v1")
	})
	return regionJobFlowRust.start != 0 && regionJobFlowRust.next != 0 && regionJobFlowRust.advance != 0 && regionJobFlowRust.close != 0
}

type rustRegionJobFlowSession struct{ id uint64 }

func startRustRegionJobFlow(input []byte) (regionJobFlowSession, error) {
	if !rustRegionJobFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("regionJob flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(regionJobFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("regionJob flow start failed")
	}
	return &rustRegionJobFlowSession{id}, nil
}
func (s *rustRegionJobFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(regionJobFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(regionJobFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustRegionJobFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("regionJob flow reply too large")
	}
	status, _, _ := nativeCall(regionJobFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("regionJob flow advance rejected")
	}
	return nil
}
func (s *rustRegionJobFlowSession) close() {
	if s.id != 0 {
		nativeCall(regionJobFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustRegionJobService(r regionJobServiceRequest, fx regionJobServiceEffects) (regionJobServiceResult, bool) {
	return runRustRegionJobServiceWithStart(r, fx, startRustRegionJobFlow)
}
func runSelectedRegionJobService(r regionJobServiceRequest, fx regionJobServiceEffects) regionJobServiceResult {
	if os.Getenv("SMARTVPN_RUST_REGION_JOB_FLOW") == "1" {
		if result, used := runRustRegionJobService(r, fx); used {
			return result
		}
	}
	return runGoRegionJobService(r, fx)
}
