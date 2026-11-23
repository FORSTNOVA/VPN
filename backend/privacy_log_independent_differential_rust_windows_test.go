//go:build windows && rustcore

package main

import "testing"

func privacyActualRunner(t *testing.T) privacyIndependentRunner {
	if !rustPrivacyLogAvailable() {
		t.Fatal("privacy native unavailable")
	}
	return func(q privacyLogRequest, fx privacyLogEffects) privacyLogResult {
		n := privacyLogRecoveries.Load()
		r, ok := runRustPrivacyLog(q, fx)
		if !ok || privacyLogRecoveries.Load() != n {
			t.Fatalf("privacy continuation %s %v", q.Op, privacyLogRecoveryReason.Load())
		}
		return r
	}
}
func TestPrivacyLogFlowIndependentMatrix(t *testing.T) {
	privacyIndependentMatrix(t, privacyActualRunner(t))
}
func TestPrivacyLogFlowIndependentGoldens(t *testing.T) {
	privacyIndependentGoldens(t, privacyActualRunner(t))
}
func TestPrivacyLogFlowIndependentResources(t *testing.T) {
	privacyIndependentResources(t, privacyActualRunner(t))
}
