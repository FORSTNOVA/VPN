//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var nodeQueryFlowRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustNodeQueryFlowAvailable() bool {
	nodeQueryFlowRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_node_query_flow_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		nodeQueryFlowRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_node_query_flow_start_v1")
		nodeQueryFlowRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_node_query_flow_next_v1")
		nodeQueryFlowRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_node_query_flow_advance_v1")
		nodeQueryFlowRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_node_query_flow_close_v1")
	})
	return nodeQueryFlowRust.start != 0 && nodeQueryFlowRust.next != 0 && nodeQueryFlowRust.advance != 0 && nodeQueryFlowRust.close != 0
}

type rustNodeQueryFlowSession struct{ id uint64 }

func startRustNodeQueryFlow(input []byte) (nodeQueryFlowSession, error) {
	if !rustNodeQueryFlowAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("nodeQuery flow unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(nodeQueryFlowRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("nodeQuery flow start failed")
	}
	return &rustNodeQueryFlowSession{id}, nil
}
func (s *rustNodeQueryFlowSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(nodeQueryFlowRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("subscription frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(nodeQueryFlowRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("subscription next rejected")
	}
	return out[:written], step, nil
}
func (s *rustNodeQueryFlowSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("nodeQuery flow reply too large")
	}
	status, _, _ := nativeCall(nodeQueryFlowRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("nodeQuery flow advance rejected")
	}
	return nil
}
func (s *rustNodeQueryFlowSession) close() {
	if s.id != 0 {
		nativeCall(nodeQueryFlowRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustNodeQueryService(r nodeQueryServiceRequest, fx nodeQueryServiceEffects) (nodeQueryServiceResult, bool) {
	return runRustNodeQueryServiceWithStart(r, fx, startRustNodeQueryFlow)
}
func runSelectedNodeQueryService(r nodeQueryServiceRequest, fx nodeQueryServiceEffects) nodeQueryServiceResult {
	if os.Getenv("SMARTVPN_RUST_NODE_QUERY_FLOW") == "1" {
		if result, used := runRustNodeQueryService(r, fx); used {
			return result
		}
	}
	return runGoNodeQueryService(r, fx)
}
