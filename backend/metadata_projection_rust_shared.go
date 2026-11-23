//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var metadataProjectionRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustMetadataProjectionAvailable() bool {
	metadataProjectionRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_metadata_projection_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		metadataProjectionRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_metadata_projection_start_v1")
		metadataProjectionRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_metadata_projection_next_v1")
		metadataProjectionRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_metadata_projection_advance_v1")
		metadataProjectionRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_metadata_projection_close_v1")
	})
	return metadataProjectionRust.start != 0 && metadataProjectionRust.next != 0 && metadataProjectionRust.advance != 0 && metadataProjectionRust.close != 0
}

type rustMetadataProjectionSession struct{ id uint64 }

func startRustMetadataProjection(input []byte) (metadataProjectionSession, error) {
	if !rustMetadataProjectionAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("metadata projection unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(metadataProjectionRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("metadata projection start failed")
	}
	return &rustMetadataProjectionSession{id}, nil
}
func (s *rustMetadataProjectionSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(metadataProjectionRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("metadata projection frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(metadataProjectionRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("metadata projection next rejected")
	}
	return out[:written], step, nil
}
func (s *rustMetadataProjectionSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("metadata projection reply too large")
	}
	status, _, _ := nativeCall(metadataProjectionRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("metadata projection advance rejected")
	}
	return nil
}
func (s *rustMetadataProjectionSession) close() {
	if s.id != 0 {
		nativeCall(metadataProjectionRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustMetadataProjection(r metadataProjectionRequest, fx metadataProjectionEffects) (metadataProjectionResult, bool) {
	return runRustMetadataProjectionWithStart(r, fx, startRustMetadataProjection)
}
func runSelectedMetadataProjection(r metadataProjectionRequest, fx metadataProjectionEffects) metadataProjectionResult {
	if os.Getenv("SMARTVPN_RUST_METADATA_PROJECTION") == "1" {
		if result, used := runRustMetadataProjection(r, fx); used {
			return result
		}
	}
	return runGoMetadataProjection(r, fx)
}
