//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestRustHTTPParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_HTTP_RUST") == "1"
	if httpRustAvailable() != expected {
		t.Fatalf("HTTP DLL capability mismatch: expected %v", expected)
	}
	cases := []string{"", "sample", "中文", "\xff\x00", "Bearer sample", strings.Repeat("x", 8192)}
	for _, token := range cases {
		for _, header := range append(append([]string{}, cases...), "Bearer "+token, "bearer "+token, "Bearer "+token+" ") {
			if got := httpAuthorized(header, token); got != httpAuthorizedGo(header, token) {
				t.Fatal("authorization mismatch")
			}
			if _, ok := rustHTTPAuthorized(header, token); ok != expected {
				t.Fatal("unexpected auth fallback")
			}
		}
	}
	for _, connected := range []bool{false, true} {
		want, got := httptest.NewRecorder(), httptest.NewRecorder()
		writeIdentityJSONGo(want, connected)
		writeIdentityJSON(got, connected)
		if !bytes.Equal(want.Body.Bytes(), got.Body.Bytes()) || want.Header().Get("Content-Type") != got.Header().Get("Content-Type") {
			t.Fatalf("identity wire mismatch")
		}
		if _, ok := rustHTTPIdentity(connected); ok != expected {
			t.Fatal("unexpected identity fallback")
		}
	}
	if !expected {
		return
	}
	// Static route planning never executes a handler or opens a socket.
	routes := (&app{}).routes("synthetic")
	for _, path := range []string{"/api/state", "/api/identify", "/api/subscriptions", "/api/tun/wintun", "/api/unknown"} {
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"} {
			value, _, _ := syscall.SyscallN(httpRust.dispatch, uintptr(unsafe.Pointer(unsafe.StringData(method))), uintptr(len(method)), uintptr(unsafe.Pointer(unsafe.StringData(path))), uintptr(len(path)), 1)
			runtime.KeepAlive(method)
			runtime.KeepAlive(path)
			_, pattern := routes.Handler(httptest.NewRequest(method, path, nil))
			if (int32(value) > 0) != (pattern != "") {
				t.Fatalf("route availability mismatch: %s %s", method, path)
			}
		}
	}
}

func TestRustReachableParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_HTTP_RUST") == "1"
	for seed := 0; seed < 2000; seed++ {
		n := seed % 31
		nodes := make([]proxyNode, n)
		delays := map[string]int{}
		for i := range nodes {
			name := fmt.Sprintf("synthetic-%d", (i+seed)%17)
			nodes[i] = proxyNode{Name: name}
			delays[name] = (seed*13+i*7)%29 - 5
		}
		preferred := fmt.Sprintf("synthetic-%d", seed%19)
		wn, wd := chooseReachableNodeGo(nodes, delays, preferred)
		gn, gd := chooseReachableNode(nodes, delays, preferred)
		if wn != gn || wd != gd {
			t.Fatalf("choice mismatch seed %d", seed)
		}
		if _, ok := rustChooseReachable(nodes, delays, preferred); ok != expected {
			t.Fatalf("choice fallback seed %d", seed)
		}
	}
}
