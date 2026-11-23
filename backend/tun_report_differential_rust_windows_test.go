//go:build windows && rustcore

package main

import "testing"

func tunIndependentRust(t *testing.T, q tunReportRequest, fx tunReportEffects) tunReportResult {
	t.Helper()
	if !rustTunReportAvailable() {
		t.Fatal("native TUN report unavailable")
	}
	before := tunReportRecoveries.Load()
	r, ok := runRustTunReport(q, fx)
	if !ok || tunReportRecoveries.Load() != before {
		if f, yes := fx.(*tunIndependentFX); yes {
			t.Logf("request=%+v executed trace=%v", q, f.trace)
		}
		t.Fatalf("native TUN did not complete handled=%v recovery=%d->%d reason=%v", ok, before, tunReportRecoveries.Load(), tunReportRecoveryReason.Load())
	}
	return r
}
func TestTunReportActualIndependentMatrix(t *testing.T) { tunIndependentMatrix(t, tunIndependentRust) }
func TestTunReportActualIndependentGolden(t *testing.T) { tunIndependentGolden(t, tunIndependentRust) }

func TestTunReportActualIndependentHTTPWriter(t *testing.T) {
	if !rustTunReportAvailable() {
		t.Fatal("native TUN report unavailable")
	}
	t.Setenv("SMARTVPN_RUST_TUN_REPORT", "1")
	before := tunReportRecoveries.Load()
	TestTunReportIndependentSelectedHTTPWriter(t)
	if tunReportRecoveries.Load() != before {
		t.Fatalf("HTTP normal planner recovery %d -> %d", before, tunReportRecoveries.Load())
	}
}
