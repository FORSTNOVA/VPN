//go:build windows && rustcore

package main

import "testing"

func speedIndependentRust(t *testing.T, q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
	t.Helper()
	if !rustSpeedFlowAvailable() {
		t.Fatal("native speed flow unavailable")
	}
	before := speedFlowRecoveries.Load()
	r, ok := runRustSpeedService(q, fx)
	if !ok || speedFlowRecoveries.Load() != before {
		t.Fatalf("native speed not complete: handled=%v recoveries=%d->%d", ok, before, speedFlowRecoveries.Load())
	}
	return r
}
func TestSpeedFlowIndependentMatrix(t *testing.T) { speedIndependentMatrix(t, speedIndependentRust) }
func TestSpeedFlowIndependentGolden(t *testing.T) { speedIndependentGolden(t, speedIndependentRust) }
