//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var nodeFrontRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustNodeFrontAvailable() bool {
	nodeFrontRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_node_front_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		nodeFrontRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_node_front_start_v1")
		nodeFrontRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_node_front_next_v1")
		nodeFrontRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_node_front_advance_v1")
		nodeFrontRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_node_front_close_v1")
	})
	return nodeFrontRust.start != 0 && nodeFrontRust.next != 0 && nodeFrontRust.advance != 0 && nodeFrontRust.close != 0
}

type rustNodeFrontSession struct{ id uint64 }

func startRustNodeFront(input []byte) (nodeFrontSession, error) {
	if !rustNodeFrontAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("node frontend unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(nodeFrontRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("node frontend start failed")
	}
	return &rustNodeFrontSession{id}, nil
}
func (s *rustNodeFrontSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(nodeFrontRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("node frontend frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(nodeFrontRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("node frontend next rejected")
	}
	return out[:written], step, nil
}
func (s *rustNodeFrontSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("node frontend reply too large")
	}
	status, _, _ := nativeCall(nodeFrontRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("node frontend advance rejected")
	}
	return nil
}
func (s *rustNodeFrontSession) close() {
	if s.id != 0 {
		nativeCall(nodeFrontRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustNodeFront(r nodeFrontRequest, fx nodeFrontEffects) (nodeFrontResult, bool) {
	return runRustNodeFrontWithStart(r, fx, startRustNodeFront)
}
func runSelectedNodeFront(r nodeFrontRequest, fx nodeFrontEffects) nodeFrontResult {
	if os.Getenv("SMARTVPN_RUST_NODE_FRONT") == "1" {
		if result, used := runRustNodeFront(r, fx); used {
			return result
		}
	}
	return runGoNodeFront(r, fx)
}
