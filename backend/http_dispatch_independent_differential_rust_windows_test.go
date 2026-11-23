//go:build windows && rustcore

package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestHTTPDispatchActualIndependentPlans(t *testing.T) {
	if !rustHTTPDispatchAvailable() {
		t.Fatal("native HTTP unavailable")
	}
	count := 0
	for i, p := range dispatchIndependentPatterns {
		method, path, _ := strings.Cut(p, " ")
		methods := []string{method}
		if method == "GET" {
			methods = append(methods, "HEAD")
		}
		for _, m := range methods {
			for _, token := range []string{"synthetic", "", "İ\xff"} {
				for _, h := range []string{"", "Bearer " + token, "bearer " + token, "Bearer  " + token, "Bearer " + token + " "} {
					q := httpDispatchRequest{Route: uint32(i + 1), Method: m, Path: path, Header: h, Token: token, Public: i == 0}
					t.Run(fmt.Sprint(count), func(t *testing.T) {
						before := httpDispatchRecoveries.Load()
						r, ok := runRustHTTPDispatch(q)
						g := runGoHTTPDispatch(q)
						if !ok || r != g || httpDispatchRecoveries.Load() != before {
							t.Fatalf("direct route%d %s token%q header%q handled%v want%+v got%+v", q.Route, m, token, h, ok, g, r)
						}
					})
					count++
				}
			}
		}
	}
	t.Logf("direct plans %d", count)
}
func TestHTTPDispatchActualIndependentMux(t *testing.T) {
	if !rustHTTPDispatchAvailable() {
		t.Fatal("native HTTP unavailable")
	}
	t.Setenv("SMARTVPN_RUST_HTTP_DISPATCH", "1")
	before := httpDispatchRecoveries.Load()
	dispatchIndependentMatrix(t)
	TestHTTPDispatchIndependentFixedGoldens(t)
	TestHTTPDispatchIndependentPanicOnce(t)
	if httpDispatchRecoveries.Load() != before {
		t.Fatalf("native mux recovered %d", httpDispatchRecoveries.Load()-before)
	}
}
