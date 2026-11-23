//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func lifeDiffRust(t *testing.T) lifeDiffRun {
	t.Helper()
	if !rustLifecycleFlowAvailable() {
		t.Fatal("lifecycle DLL unavailable")
	}
	return func(q lifecycleServiceRequest, f lifecycleServiceEffects) lifecycleServiceResult {
		n := lifecycleFlowRecoveries.Load()
		r, used := runRustLifecycleService(q, f)
		if !used || lifecycleFlowRecoveries.Load() != n {
			t.Fatalf("%s used=%v recovery=%d reason=%v", q.Op, used, lifecycleFlowRecoveries.Load()-n, lifecycleFlowRecoveryReason.Load())
		}
		return r
	}
}

var lifeDiffOps = []string{"new", "load-settings", "cache", "bundle", "sweep", "prepare", "serve", "cleanup", "shutdown", "identify", "reapply"}

func TestMigrationLifecycleActualMatrix(t *testing.T) {
	for _, op := range lifeDiffOps {
		for m := 0; m < 64; m++ {
			t.Run(fmt.Sprintf("%s/%d", op, m), func(t *testing.T) {
				s := lifeDiffBase()
				s.Portable = m&1 != 0
				s.Android = m&2 != 0
				s.Other = m&4 != 0
				s.Running = m&8 != 0
				s.ProxyOn = m&16 != 0
				s.Collision = m&32 != 0
				lifeDiffCompare(t, lifecycleServiceRequest{Op: op}, s, lifeDiffRust(t))
			})
		}
	}
}
func TestMigrationLifecycleActualFaults(t *testing.T) {
	for _, op := range lifeDiffOps {
		for _, fault := range []string{"read-settings", "cache-read", "open-secret", "glob", "stat", "bundle-read", "bundle-write", "store-open", "load-params", "load-health", "load-regions", "proxy", "listen-1", "listen-2", "stop-kernel", "store-close", "disconnect"} {
			t.Run(op+"/"+fault, func(t *testing.T) {
				s := lifeDiffBase()
				s.Fail = fault
				s.Collision = true
				s.Running = true
				s.ProxyOn = true
				lifeDiffCompare(t, lifecycleServiceRequest{Op: op}, s, lifeDiffRust(t))
			})
		}
	}
}
func TestMigrationLifecycleActualRandom(t *testing.T) {
	for i := 0; i < 700; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(271000 + i)))
			s := lifeDiffBase()
			s.Portable = r.Intn(2) == 0
			s.Android = r.Intn(2) == 0
			s.Running = r.Intn(2) == 0
			s.ProxyOn = r.Intn(2) == 0
			s.Collision = r.Intn(2) == 0
			s.Other = r.Intn(2) == 0
			s.BundleExists = r.Intn(2) == 0
			s.HashMatch = r.Intn(2) == 0
			s.Mode = []string{modeSystemProxy, modeTUN, ""}[r.Intn(3)]
			s.Timeout = []int{0, 4999, 5000, 6000}[r.Intn(4)]
			s.Threshold = r.Intn(5)
			s.Settings = []string{`{}`, `null`, `{"selectionMode":"manual","pathChecks":{"intervalSec":20}}`, `{"subscriptionUrl":"sealed","socksPort":"invalid","selectedNode":"b","strictPrivacy":true}`, `{"chain":{"hops":["a","b"]},"chains":{}}`, `{"subscriptionUrl":"sealed","chain":{"hops":["a","b"]}}`}[r.Intn(6)]
			lifeDiffCompare(t, lifecycleServiceRequest{Op: lifeDiffOps[r.Intn(len(lifeDiffOps))]}, s, lifeDiffRust(t))
		})
	}
}
func TestMigrationLifecycleActualGolden(t *testing.T) { lifeDiffGoldens(t, lifeDiffRust(t)) }
func TestMigrationLifecycleActualOriginalSettings(t *testing.T) {
	lifeSettingsGolden(t, lifeDiffRust(t))
}
