//go:build windows && rustcore

package main

import "testing"

func bootActualRunner(t *testing.T) bootIndependentRunner {
	if !rustBootstrapFlowAvailable() {
		t.Fatal("bootstrap native unavailable")
	}
	return func(q bootstrapServiceRequest, fx bootstrapServiceEffects) bootstrapServiceResult {
		n := bootstrapFlowRecoveries.Load()
		r, ok := runRustBootstrapService(q, fx)
		if !ok || bootstrapFlowRecoveries.Load() != n {
			t.Fatalf("bootstrap continuation %s", q.Op)
		}
		return r
	}
}
func TestBootstrapActualIndependentMatrix(t *testing.T) {
	bootIndependentMatrix(t, bootActualRunner(t))
}
func TestBootstrapActualIndependentGoldens(t *testing.T) {
	bootIndependentGoldens(t, bootActualRunner(t))
}
func TestBootstrapActualIndependentResources(t *testing.T) {
	bootIndependentResources(t, bootActualRunner(t))
}
func TestBootstrapActualIndependentScriptFallback(t *testing.T) {
	bootIndependentScriptFallback(t, bootActualRunner(t))
}
