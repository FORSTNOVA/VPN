//go:build windows && rustcore

package main

import "testing"

func policyActualRunner(t *testing.T) policyIndependentRunner {
	if !rustPolicyDnsAvailable() {
		t.Fatal("policy native unavailable")
	}
	return func(q policyDnsRequest, fx policyDnsEffects) policyDnsResult {
		n := policyDnsRecoveries.Load()
		r, ok := runRustPolicyDns(q, fx)
		if !ok || policyDnsRecoveries.Load() != n {
			t.Fatalf("policy continuation %s path=%q handled=%v: %v", q.Op, q.Path, ok, policyDnsRecoveryReason.Load())
		}
		return r
	}
}
func TestPolicyDNSActualIndependentMatrix(t *testing.T) {
	policyIndependentMatrix(t, policyActualRunner(t))
}
func TestPolicyDNSActualIndependentGoldens(t *testing.T) {
	policyIndependentGoldens(t, policyActualRunner(t))
}
func TestPolicyDNSActualIndependentLimits(t *testing.T) {
	policyIndependentLimits(t, policyActualRunner(t))
}
func TestPolicyDNSActualIndependentFinalFacts(t *testing.T) {
	policyIndependentFinalFacts(t, policyActualRunner(t))
}
func TestPolicyDNSActualIndependentInvalidUTF8Fallback(t *testing.T) {
	if !rustPolicyDnsAvailable() {
		t.Fatal("native unavailable")
	}
	q := policyDnsRequest{Op: "suffix", Path: "\xff.COM"}
	e := &appPolicyDnsEffects{q: q}
	fx := &policyIndependentFX{Reply: e.source("suffix")}
	before := policyDnsRecoveries.Load()
	_, handled := runRustPolicyDns(q, fx)
	if handled || len(fx.Trace) != 0 || policyDnsRecoveries.Load() != before {
		t.Fatalf("invalid UTF8 must reject before effects handled=%v trace=%v", handled, fx.Trace)
	}
	TestPolicyDNSIndependentSelectedInvalidUTF8(t)
}
