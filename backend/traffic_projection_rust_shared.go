//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

var trafficProjectionRust struct {
	once sync.Once
	call uintptr
}
var trafficProjectionRecoveries atomic.Uint64

func rustTrafficProjectionAvailable() bool {
	trafficProjectionRust.once.Do(func() {
		if rustCoreAvailable() {
			trafficProjectionRust.call, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_projection_v1")
		}
	})
	return trafficProjectionRust.call != 0
}
func runRustTrafficProjection(q trafficProjectionRequest) (trafficProjectionResult, bool) {
	if !rustTrafficProjectionAvailable() {
		return trafficProjectionResult{}, false
	}
	in, err := trafficProjectionEncode(q)
	if err != nil {
		return trafficProjectionResult{}, false
	}
	out := make([]byte, 65536)
	for i := 0; i < 2; i++ {
		var n uintptr
		s, _, _ := nativeCall(trafficProjectionRust.call, uintptr(unsafe.Pointer(&in[0])), uintptr(len(in)), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&n)))
		runtime.KeepAlive(in)
		runtime.KeepAlive(out)
		if int32(s) == 1 && n > uintptr(len(out)) && n <= trafficProjectionMaxBytes {
			out = make([]byte, int(n))
			continue
		}
		if int32(s) != 0 || n > uintptr(len(out)) {
			return trafficProjectionResult{}, false
		}
		r, e := trafficProjectionDecode(out[:n])
		if e != nil {
			return r, false
		}
		return r, true
	}
	return trafficProjectionResult{}, false
}
func runSelectedTrafficProjection(q trafficProjectionRequest) trafficProjectionResult {
	want := runGoTrafficProjection(q)
	if os.Getenv("SMARTVPN_RUST_TRAFFIC_PROJECTION") != "1" {
		return want
	}
	got, ok := runRustTrafficProjection(q)
	if ok && reflect.DeepEqual(got, want) {
		return got
	}
	trafficProjectionRecoveries.Add(1)
	return want
}
