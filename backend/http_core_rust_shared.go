//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"net/http"
	"runtime"
	"sync"
	"unsafe"
)

var httpRust struct {
	once                     sync.Once
	auth, identity, dispatch uintptr
	choice                   uintptr
}

func httpRustAvailable() bool {
	httpRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		httpRust.choice, _ = nativeLookup(rustCore.handle, "smartvpn_choose_reachable_v1")
		httpRust.auth, _ = nativeLookup(rustCore.handle, "smartvpn_http_authorized_v1")
		httpRust.identity, _ = nativeLookup(rustCore.handle, "smartvpn_http_identity_v1")
		httpRust.dispatch, _ = nativeLookup(rustCore.handle, "smartvpn_http_dispatch_v1")
	})
	return httpRust.auth != 0 && httpRust.identity != 0 && httpRust.dispatch != 0
}
func httpAuthorized(header, token string) bool {
	if result, ok := rustHTTPAuthorized(header, token); ok {
		return result
	}
	return httpAuthorizedGo(header, token)
}
func rustHTTPAuthorized(header, token string) (bool, bool) {
	if len(header) > 8<<20 || len(token) > 8<<20 || !httpRustAvailable() {
		return false, false
	}
	value, _, _ := nativeCall(httpRust.auth, uintptr(unsafe.Pointer(unsafe.StringData(header))), uintptr(len(header)), uintptr(unsafe.Pointer(unsafe.StringData(token))), uintptr(len(token)))
	runtime.KeepAlive(header)
	runtime.KeepAlive(token)
	if value > 1 {
		return false, false
	}
	return value == 1, true
}
func writeIdentityJSON(w http.ResponseWriter, connected bool) {
	if result, ok := rustHTTPIdentity(connected); ok {
		_, _ = w.Write(result)
		return
	}
	writeIdentityJSONGo(w, connected)
}
func rustHTTPIdentity(connected bool) ([]byte, bool) {
	if !httpRustAvailable() {
		return nil, false
	}
	out := make([]byte, 64)
	var written uintptr
	var flag uintptr
	if connected {
		flag = 1
	}
	value, _, _ := nativeCall(httpRust.identity, flag, uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)))
	runtime.KeepAlive(out)
	if int32(value) != 0 || written > uintptr(len(out)) {
		return nil, false
	}
	return out[:written], true
}

func chooseReachableNode(nodes []proxyNode, delays map[string]int, preferred string) (string, int) {
	if index, ok := rustChooseReachable(nodes, delays, preferred); ok {
		if index < 0 {
			return "", 0
		}
		return nodes[index].Name, delays[nodes[index].Name]
	}
	return chooseReachableNodeGo(nodes, delays, preferred)
}
func rustChooseReachable(nodes []proxyNode, delays map[string]int, preferred string) (int, bool) {
	httpRustAvailable()
	if httpRust.choice == 0 || len(nodes) > 1000000 {
		return 0, false
	}
	values := make([]int64, len(nodes))
	preferredIndex := int64(-1)
	for i, node := range nodes {
		values[i] = int64(delays[node.Name])
		if node.Name == preferred && preferredIndex < 0 {
			preferredIndex = int64(i)
		}
	}
	var input *int64
	if len(values) > 0 {
		input = &values[0]
	}
	value, _, _ := nativeCall(httpRust.choice, uintptr(unsafe.Pointer(input)), uintptr(len(values)), uintptr(preferredIndex))
	runtime.KeepAlive(values)
	index := int64(value)
	if index < -1 || index >= int64(len(nodes)) {
		return 0, false
	}
	return int(index), true
}
