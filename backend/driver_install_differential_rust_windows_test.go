//go:build windows && rustcore

package main

import "testing"

func driverIndependentRust(t *testing.T, q driverInstallRequest, fx driverInstallEffects) driverInstallResult {
	t.Helper()
	if !rustDriverInstallAvailable() {
		t.Fatal("native driver flow unavailable")
	}
	before := driverInstallRecoveries.Load()
	r, ok := runRustDriverInstall(q, fx)
	if !ok || driverInstallRecoveries.Load() != before {
		t.Fatalf("native driver not complete: handled=%v recovery=%d->%d", ok, before, driverInstallRecoveries.Load())
	}
	return r
}
func TestWintunFlowIndependentMatrix(t *testing.T) {
	if !rustDriverInstallAvailable() {
		t.Fatal("native driver unavailable before matrix")
	}
	driverIndependentMatrix(t, driverIndependentRust)
}
func TestWintunFlowIndependentGolden(t *testing.T) { driverIndependentGolden(t, driverIndependentRust) }

func TestWintunFlowIndependentHTTPWriter(t *testing.T) {
	if !rustDriverInstallAvailable() {
		t.Fatal("native driver unavailable")
	}
	t.Setenv("SMARTVPN_RUST_DRIVER_INSTALL", "1")
	before := driverInstallRecoveries.Load()
	TestWintunServiceIndependentSelectedHTTPWriter(t)
	if driverInstallRecoveries.Load() != before {
		t.Fatal("HTTP native recovered")
	}
}
