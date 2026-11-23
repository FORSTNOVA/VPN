//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var privacyLogRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustPrivacyLogAvailable() bool {
	privacyLogRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_privacy_log_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		privacyLogRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_privacy_log_start_v1")
		privacyLogRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_privacy_log_next_v1")
		privacyLogRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_privacy_log_advance_v1")
		privacyLogRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_privacy_log_close_v1")
	})
	return privacyLogRust.start != 0 && privacyLogRust.next != 0 && privacyLogRust.advance != 0 && privacyLogRust.close != 0
}

type rustPrivacyLogSession struct{ id uint64 }

func startRustPrivacyLog(input []byte) (privacyLogSession, error) {
	if !rustPrivacyLogAvailable() || len(input) == 0 || len(input) > 64<<20 {
		return nil, errors.New("privacy log unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(privacyLogRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("privacy log start failed")
	}
	return &rustPrivacyLogSession{id}, nil
}
func (s *rustPrivacyLogSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(privacyLogRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 64<<20 {
		return nil, 0, errors.New("privacy log frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(privacyLogRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("privacy log next rejected")
	}
	return out[:written], step, nil
}
func (s *rustPrivacyLogSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 64<<20 {
		return errors.New("privacy log reply too large")
	}
	status, _, _ := nativeCall(privacyLogRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("privacy log advance rejected")
	}
	return nil
}
func (s *rustPrivacyLogSession) close() {
	if s.id != 0 {
		nativeCall(privacyLogRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustPrivacyLog(r privacyLogRequest, fx privacyLogEffects) (privacyLogResult, bool) {
	return runRustPrivacyLogWithStart(r, fx, startRustPrivacyLog)
}
func runSelectedPrivacyLog(r privacyLogRequest, fx privacyLogEffects) privacyLogResult {
	if os.Getenv("SMARTVPN_RUST_PRIVACY_LOG") == "1" {
		if result, used := runRustPrivacyLog(r, fx); used {
			return result
		}
	}
	return runGoPrivacyLog(r, fx)
}
