//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"encoding/binary"
	"runtime"
	"testing"
	"unsafe"
)

func TestHTTPDispatchFlowABI(t *testing.T) {
	if !rustHTTPDispatchAvailable() {
		t.Fatal("native unavailable")
	}
	var b []byte
	for _, s := range []string{"GET", "/api/state", "Bearer t", "t"} {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	b = append(b, 1)
	call := func(in []byte, cap uintptr) (int32, [7]byte) {
		out := [7]byte{9, 9, 9, 9, 9, 9, 9}
		var p *byte
		if len(in) > 0 {
			p = &in[0]
		}
		s, _, _ := nativeCall(httpDispatchNative.call, uintptr(unsafe.Pointer(p)), uintptr(len(in)), uintptr(unsafe.Pointer(&out[0])), cap)
		runtime.KeepAlive(in)
		return int32(s), out
	}
	for i := 0; i < len(b); i++ {
		s, out := call(b[:i], 7)
		if s != -1 || out != [7]byte{9, 9, 9, 9, 9, 9, 9} {
			t.Fatal("truncated input", i, s, out)
		}
	}
	for _, v := range []struct {
		in  []byte
		cap uintptr
	}{{b, 6}, {append(append([]byte(nil), b...), 0), 7}, {[]byte{255, 255, 255, 255}, 7}} {
		s, out := call(v.in, v.cap)
		if s != -1 || out != [7]byte{9, 9, 9, 9, 9, 9, 9} {
			t.Fatal("partial output", s, out)
		}
	}
	q := httpDispatchRequest{Route: 2, Method: "GET", Path: "/api/state", Header: "Bearer \xff", Token: "\xff"}
	got, ok := runRustHTTPDispatch(q)
	if !ok || got != runGoHTTPDispatch(q) {
		t.Fatal(got, ok)
	}
}
