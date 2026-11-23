//go:build windows && rustcore

package main

import "testing"

func resolverActualRunner(t *testing.T) resolverIndependentRunner {
	if !rustAndroidResolverAvailable() {
		t.Fatal("Android resolver native unavailable")
	}
	return func(q androidResolverRequest, fx androidResolverEffects) androidResolverResult {
		n := androidResolverRecoveries.Load()
		r, ok := runRustAndroidResolver(q, fx)
		if !ok || androidResolverRecoveries.Load() != n {
			t.Fatalf("resolver recovery %v", androidResolverRecoveryReason.Load())
		}
		return r
	}
}
func TestAndroidResolverFlowIndependentMatrix(t *testing.T) {
	resolverIndependentMatrix(t, resolverActualRunner(t))
}
func TestAndroidResolverFlowIndependentResources(t *testing.T) {
	resolverIndependentResources(t, resolverActualRunner(t))
}
