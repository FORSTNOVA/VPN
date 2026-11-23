//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"encoding/binary"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

var httpDispatchNative struct {
	once sync.Once
	call uintptr
}
var httpDispatchRecoveries atomic.Uint64

func rustHTTPDispatchAvailable() bool {
	httpDispatchNative.once.Do(func() {
		if rustCoreAvailable() {
			httpDispatchNative.call, _ = nativeLookup(rustCore.handle, "smartvpn_http_composition_v1")
		}
	})
	return httpDispatchNative.call != 0
}
func runRustHTTPDispatch(q httpDispatchRequest) (httpDispatchPlan, bool) {
	if !rustHTTPDispatchAvailable() {
		return httpDispatchPlan{}, false
	}
	var input []byte
	for _, s := range []string{q.Method, q.Path, q.Header, q.Token} {
		if len(s) > 8<<20 || len(input)+len(s)+4 > 8<<20 {
			return httpDispatchPlan{}, false
		}
		input = binary.LittleEndian.AppendUint32(input, uint32(len(s)))
		input = append(input, s...)
	}
	var flags byte
	if runtime.GOOS == "windows" {
		flags = 1
	}
	if q.Route == 0 {
		flags |= 2
	}
	input = append(input, flags)
	var out [7]byte
	status, _, _ := nativeCall(httpDispatchNative.call, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(out)
	if int32(status) != 0 || out[6] > 1 {
		return httpDispatchPlan{}, false
	}
	p := httpDispatchPlan{Route: binary.LittleEndian.Uint32(out[:4]), Status: int(binary.LittleEndian.Uint16(out[4:6])), JSON: out[6] == 1}
	return p, true
}
func runSelectedHTTPDispatch(q httpDispatchRequest) httpDispatchPlan {
	want := runGoHTTPDispatch(q)
	if os.Getenv("SMARTVPN_RUST_HTTP_DISPATCH") != "1" {
		return want
	}
	got, ok := runRustHTTPDispatch(q)
	if ok && got == want {
		return got
	}
	httpDispatchRecoveries.Add(1)
	return want
}

func supportsHostCapability(symbol string) bool {
	if !rustCoreAvailable() {
		return false
	}
	proc, _ := nativeLookup(rustCore.handle, symbol)
	if proc == 0 {
		return false
	}
	v, _, _ := nativeCall(proc)
	return v == 1
}

var readHostSupported = sync.OnceValue(func() bool { return supportsHostCapability("smartvpn_read_api_version_v1") })
var writeHostSupported = sync.OnceValue(func() bool { return supportsHostCapability("smartvpn_write_api_version_v1") })

var windowsKernelCapable = sync.OnceValue(func() bool {
	return runtime.GOOS == "windows" && supportsHostCapability("smartvpn_windows_kernel_version_v1")
})

func windowsKernelHostEnabled() bool {
	return windowsKernelCapable() && os.Getenv("SMARTVPN_MANAGED_HOST") != "off"
}
func windowsKernelFlowSelected(flag string) bool {
	if value, ok := os.LookupEnv(flag); ok {
		return value == "1"
	}
	return windowsKernelHostEnabled()
}
