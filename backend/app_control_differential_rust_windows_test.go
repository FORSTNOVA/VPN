//go:build windows && rustcore

package main

import "testing"

func appIndependentRust(t *testing.T, q appServiceRequest, fx appServiceEffects) appServiceResult {
	t.Helper()
	if !rustAppFlowAvailable() {
		t.Fatal("native application flow unavailable")
	}
	before := appFlowRecoveries.Load()
	r, ok := runRustAppService(q, fx)
	if !ok || appFlowRecoveries.Load() != before {
		t.Fatalf("native application not completed: handled=%v recovery=%d->%d", ok, before, appFlowRecoveries.Load())
	}
	return r
}
func TestAppFlowIndependentMatrix(t *testing.T) { appIndependentMatrix(t, appIndependentRust) }
func TestAppFlowIndependentGolden(t *testing.T) { appIndependentGolden(t, appIndependentRust) }

func TestAppFlowIndependentNativeChannelLoop(t *testing.T) {
	appIndependentChannelGolden(t, appIndependentRust)
}

func TestAppFlowIndependentStateResourceIdentity(t *testing.T) {
	appIndependentStateResourceGolden(t, appIndependentRust)
}

func TestAppFlowIndependentProtectedHealthChild(t *testing.T) {
	if !rustHealthFlowAvailable() {
		t.Fatal("native health child unavailable")
	}
	t.Setenv("SMARTVPN_RUST_HEALTH_FLOW", "1")
	before := healthFlowRecoveries.Load()
	appIndependentProtectedHealthGolden(t, appIndependentRust)
	if healthFlowRecoveries.Load() != before {
		t.Fatal("normal health child recovered")
	}
}
