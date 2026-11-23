//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var serviceStateRust struct {
	once              sync.Once
	open, call, close uintptr
}

func rustServiceStateAvailable() bool {
	serviceStateRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		v, _ := nativeLookup(rustCore.handle, "smartvpn_service_state_version_v6")
		if v == 0 {
			return
		}
		n, _, _ := nativeCall(v)
		if n != 6 {
			return
		}
		capProc, _ := nativeLookup(rustCore.handle, "smartvpn_service_state_capabilities_v6")
		if capProc == 0 {
			return
		}
		caps, _, _ := nativeCall(capProc)
		if !serviceStateFullCapabilities(uint64(n), uint64(caps)) {
			return
		}
		serviceStateRust.open, _ = nativeLookup(rustCore.handle, "smartvpn_service_state_open_v6")
		serviceStateRust.call, _ = nativeLookup(rustCore.handle, "smartvpn_service_state_call_v6")
		serviceStateRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_service_state_close_v6")
	})
	return serviceStateRust.open != 0 && serviceStateRust.call != 0 && serviceStateRust.close != 0
}

type nativeServiceStateTransport struct {
	id     uint64
	buffer []byte
}

func newRustServiceStateBackend() (serviceStateBackend, error) {
	if !rustServiceStateAvailable() {
		return nil, errors.New("Rust service state unavailable")
	}
	var id uint64
	s, _, _ := nativeCall(serviceStateRust.open, uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(&id)
	if int32(s) != 0 || id == 0 {
		return nil, errors.New("Rust service state open failed")
	}
	return &rustServiceStateBackend{transport: &nativeServiceStateTransport{id: id, buffer: make([]byte, 4096)}}, nil
}
func (t *nativeServiceStateTransport) call(op byte, input []byte) ([]byte, error) {
	if t.id == 0 {
		return nil, errServiceStateClosed
	}
	if len(input) > 64<<20 {
		return nil, errors.New("service state input too large")
	}
	var p *byte
	if len(input) > 0 {
		p = &input[0]
	}
	for attempt := 0; attempt < 2; attempt++ {
		var n uintptr
		status, _, _ := nativeCall(serviceStateRust.call, uintptr(t.id), uintptr(op), uintptr(unsafe.Pointer(p)), uintptr(len(input)), uintptr(unsafe.Pointer(&t.buffer[0])), uintptr(len(t.buffer)), uintptr(unsafe.Pointer(&n)))
		runtime.KeepAlive(input)
		runtime.KeepAlive(t)
		runtime.KeepAlive(&n)
		if int32(status) == -6 && (op == 1 || op == 2 || op == 4 || op == 5 || op == 7 || op == 8 || op == 9 || op == 10 || op == 11 || op == 12) && attempt == 0 && n > uintptr(len(t.buffer)) && n <= 128<<20 {
			t.buffer = make([]byte, int(n))
			continue
		}
		if int32(status) != 0 {
			return nil, fmt.Errorf("service state call rejected: %d", int32(status))
		}
		if n > uintptr(len(t.buffer)) {
			return nil, errors.New("invalid service state output size")
		}
		return append([]byte(nil), t.buffer[:n]...), nil
	}
	return nil, errors.New("service state size changed")
}

func (t *nativeServiceStateTransport) close() error {
	if t.id == 0 {
		return nil
	}
	id := t.id
	t.id = 0
	t.buffer = nil
	s, _, _ := nativeCall(serviceStateRust.close, uintptr(id))
	if int32(s) != 0 {
		return errors.New("service state close failed")
	}
	return nil
}
func newSelectedServiceStateBackend() (serviceStateBackend, error) {
	if os.Getenv("SMARTVPN_RUST_SERVICE_STATE") == "1" {
		b, e := newRustServiceStateBackend()
		if e == nil {
			return b, nil
		}
	}
	return newGoServiceStateBackend(), nil
}
