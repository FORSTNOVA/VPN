//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var embeddedKernelRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustEmbeddedKernelAvailable() bool {
	embeddedKernelRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_embedded_kernel_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		embeddedKernelRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_embedded_kernel_start_v1")
		embeddedKernelRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_embedded_kernel_next_v1")
		embeddedKernelRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_embedded_kernel_advance_v1")
		embeddedKernelRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_embedded_kernel_close_v1")
	})
	return embeddedKernelRust.start != 0 && embeddedKernelRust.next != 0 && embeddedKernelRust.advance != 0 && embeddedKernelRust.close != 0
}

type rustEmbeddedKernelSession struct{ id uint64 }

func startRustEmbeddedKernel(input []byte) (embeddedKernelSession, error) {
	if !rustEmbeddedKernelAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("embedded kernel unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(embeddedKernelRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("embedded kernel start failed")
	}
	return &rustEmbeddedKernelSession{id}, nil
}
func (s *rustEmbeddedKernelSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(embeddedKernelRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("embedded kernel frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(embeddedKernelRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("embedded kernel next rejected")
	}
	return out[:written], step, nil
}
func (s *rustEmbeddedKernelSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("embedded kernel reply too large")
	}
	status, _, _ := nativeCall(embeddedKernelRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("embedded kernel advance rejected")
	}
	return nil
}
func (s *rustEmbeddedKernelSession) close() {
	if s.id != 0 {
		nativeCall(embeddedKernelRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustEmbeddedKernel(r embeddedKernelRequest, fx embeddedKernelEffects) (embeddedKernelResult, bool) {
	return runRustEmbeddedKernelWithStart(r, fx, startRustEmbeddedKernel)
}
func runSelectedEmbeddedKernel(r embeddedKernelRequest, fx embeddedKernelEffects) embeddedKernelResult {
	if os.Getenv("SMARTVPN_RUST_EMBEDDED_KERNEL") == "1" {
		if result, used := runRustEmbeddedKernel(r, fx); used {
			return result
		}
	}
	return runGoEmbeddedKernel(r, fx)
}
