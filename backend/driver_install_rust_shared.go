//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var driverInstallRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustDriverInstallAvailable() bool {
	driverInstallRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_driver_install_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		driverInstallRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_driver_install_start_v1")
		driverInstallRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_driver_install_next_v1")
		driverInstallRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_driver_install_advance_v1")
		driverInstallRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_driver_install_close_v1")
	})
	return driverInstallRust.start != 0 && driverInstallRust.next != 0 && driverInstallRust.advance != 0 && driverInstallRust.close != 0
}

type rustDriverInstallSession struct{ id uint64 }

func startRustDriverInstall(input []byte) (driverInstallSession, error) {
	if !rustDriverInstallAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("driver installation unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(driverInstallRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("driver installation start failed")
	}
	return &rustDriverInstallSession{id}, nil
}
func (s *rustDriverInstallSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(driverInstallRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("driver installation frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(driverInstallRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("driver installation next rejected")
	}
	return out[:written], step, nil
}
func (s *rustDriverInstallSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("driver installation reply too large")
	}
	status, _, _ := nativeCall(driverInstallRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("driver installation advance rejected")
	}
	return nil
}
func (s *rustDriverInstallSession) close() {
	if s.id != 0 {
		nativeCall(driverInstallRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustDriverInstall(r driverInstallRequest, fx driverInstallEffects) (driverInstallResult, bool) {
	return runRustDriverInstallWithStart(r, fx, startRustDriverInstall)
}
func runSelectedDriverInstall(r driverInstallRequest, fx driverInstallEffects) driverInstallResult {
	if os.Getenv("SMARTVPN_RUST_DRIVER_INSTALL") == "1" {
		if result, used := runRustDriverInstall(r, fx); used {
			return result
		}
	}
	return runGoDriverInstall(r, fx)
}
