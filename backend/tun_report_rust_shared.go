//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var tunReportRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustTunReportAvailable() bool {
	tunReportRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_tun_report_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		tunReportRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_tun_report_start_v1")
		tunReportRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_tun_report_next_v1")
		tunReportRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_tun_report_advance_v1")
		tunReportRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_tun_report_close_v1")
	})
	return tunReportRust.start != 0 && tunReportRust.next != 0 && tunReportRust.advance != 0 && tunReportRust.close != 0
}

type rustTunReportSession struct{ id uint64 }

func startRustTunReport(input []byte) (tunReportSession, error) {
	if !rustTunReportAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("tun report unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(tunReportRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("tun report start failed")
	}
	return &rustTunReportSession{id}, nil
}
func (s *rustTunReportSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(tunReportRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(tunReportRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustTunReportSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("tun report reply too large")
	}
	status, _, _ := nativeCall(tunReportRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("tun report advance rejected")
	}
	return nil
}
func (s *rustTunReportSession) close() {
	if s.id != 0 {
		nativeCall(tunReportRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustTunReport(r tunReportRequest, fx tunReportEffects) (tunReportResult, bool) {
	return runRustTunReportWithStart(r, fx, startRustTunReport)
}
func runSelectedTunReport(r tunReportRequest, fx tunReportEffects) tunReportResult {
	if os.Getenv("SMARTVPN_RUST_TUN_REPORT") == "1" {
		if result, used := runRustTunReport(r, fx); used {
			return result
		}
	}
	return runGoTunReport(r, fx)
}
