//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var quotaFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustQuotaFlowAvailable() bool {
	quotaFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_quota_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		quotaFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_quota_flow_start_v1")
		quotaFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_quota_flow_next_v1")
		quotaFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_quota_flow_advance_v1")
		quotaFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_quota_flow_close_v1")
	})
	return quotaFlowRust.start != 0 && quotaFlowRust.next != 0 && quotaFlowRust.advance != 0 && quotaFlowRust.close != 0
}

type rustQuotaFlowSession struct{ id uint64 }

func startRustQuotaFlow(input []byte) (quotaFlowSession, error) {
	if !rustQuotaFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("quota flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(quotaFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("quota flow start failed")
	}
	return &rustQuotaFlowSession{id}, nil
}
func (s *rustQuotaFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(quotaFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(quotaFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustQuotaFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("quota flow reply too large")
	}
	status, _, _ := nativeCall(quotaFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("quota flow advance rejected")
	}
	return nil
}
func (s *rustQuotaFlowSession) close() {
	if s.id != 0 {
		nativeCall(quotaFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustQuotaService(r quotaServiceRequest, fx quotaServiceEffects) (quotaServiceResult, bool) {
	return runRustQuotaServiceWithStart(r, fx, startRustQuotaFlow)
}
func runSelectedQuotaService(r quotaServiceRequest, fx quotaServiceEffects) quotaServiceResult {
	if os.Getenv("SMARTVPN_RUST_QUOTA_FLOW") == "1" {
		if result, used := runRustQuotaService(r, fx); used {
			return result
		}
	}
	return runGoQuotaService(r, fx)
}
