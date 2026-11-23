//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var androidResolverRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustAndroidResolverAvailable() bool {
	androidResolverRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_android_resolver_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		androidResolverRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_android_resolver_start_v1")
		androidResolverRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_android_resolver_next_v1")
		androidResolverRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_android_resolver_advance_v1")
		androidResolverRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_android_resolver_close_v1")
	})
	return androidResolverRust.start != 0 && androidResolverRust.next != 0 && androidResolverRust.advance != 0 && androidResolverRust.close != 0
}

type rustAndroidResolverSession struct{ id uint64 }

func startRustAndroidResolver(input []byte) (androidResolverSession, error) {
	if !rustAndroidResolverAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("Android resolver unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(androidResolverRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("Android resolver start failed")
	}
	return &rustAndroidResolverSession{id}, nil
}
func (s *rustAndroidResolverSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(androidResolverRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("Android resolver frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(androidResolverRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("Android resolver next rejected")
	}
	return out[:written], step, nil
}
func (s *rustAndroidResolverSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("Android resolver reply too large")
	}
	status, _, _ := nativeCall(androidResolverRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("Android resolver advance rejected")
	}
	return nil
}
func (s *rustAndroidResolverSession) close() {
	if s.id != 0 {
		nativeCall(androidResolverRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustAndroidResolver(r androidResolverRequest, fx androidResolverEffects) (androidResolverResult, bool) {
	return runRustAndroidResolverWithStart(r, fx, startRustAndroidResolver)
}
func runSelectedAndroidResolver(r androidResolverRequest, fx androidResolverEffects) androidResolverResult {
	if os.Getenv("SMARTVPN_RUST_ANDROID_RESOLVER") == "1" {
		if result, used := runRustAndroidResolver(r, fx); used {
			return result
		}
	}
	return runGoAndroidResolver(r, fx)
}
