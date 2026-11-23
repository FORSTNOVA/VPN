//go:build android && (amd64 || arm64) && cgo && rustcore

package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// Exercise the allocations and native calls seen in the Android sweep crash
// without starting the service, fetching a list or changing any routes.
func TestAndroidChinaFlowGC(t *testing.T) {
	if os.Getenv("SMARTVPN_RUST_CORE_LIB") == "" {
		t.Skip("requires a staged Rust core")
	}
	if !rustChinaFlowAvailable() {
		t.Fatal("native China flow unavailable")
	}
	var body strings.Builder
	for i := 0; i < 2048; i++ {
		fmt.Fprintf(&body, "10.%d.%d.1/24\n", i/256, i%256)
	}
	q := chinaServiceRequest{Op: "parse", Body: []byte(body.String())}
	a := new(app)
	expected := runGoChinaService(q, newAppChinaEffects(a, q))
	if expected.Err != nil || len(expected.Ranges) != 2048 {
		t.Fatal("invalid test fixture")
	}
	before := chinaFlowRecoveries.Load()
	old := debug.SetGCPercent(10)
	defer debug.SetGCPercent(old)
	var workers sync.WaitGroup
	for i := 0; i < 3; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 3; j++ {
				runtime.GC()
				got, used := runRustChinaService(q, newAppChinaEffects(a, q))
				if !used || got.Err != nil || len(got.Ranges) != len(expected.Ranges) {
					t.Errorf("unexpected native parse result: %v", got.Err)
					return
				}
				for idx, v := range got.Ranges {
					if v != expected.Ranges[idx] {
						t.Error("CIDR result mismatch")
						return
					}
				}
			}
		}()
	}
	workers.Wait()
	if before != chinaFlowRecoveries.Load() {
		t.Fatal("native flow fell back")
	}
}
