//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func diagnosticDiffRust(t *testing.T) func(diagnoseServiceRequest, diagnoseServiceEffects) diagnoseServiceResult {
	t.Helper()
	if !rustDiagnosticFlowAvailable() {
		t.Fatal("diagnostic actual DLL unavailable")
	}
	return func(q diagnoseServiceRequest, f diagnoseServiceEffects) diagnoseServiceResult {
		before := diagnosticFlowRecoveries.Load()
		r, used := runRustDiagnoseService(q, f)
		if !used || diagnosticFlowRecoveries.Load() != before {
			t.Fatalf("op=%s used=%v recoveries=%d reason=%v", q.Op, used, diagnosticFlowRecoveries.Load()-before, diagnosticFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationDiagnosticActualMatrix(t *testing.T) {
	for _, op := range []string{"diagnose", "ensure", "details", "dns", "stun"} {
		for m := 0; m < 128; m++ {
			t.Run(fmt.Sprintf("%s/%d", op, m), func(t *testing.T) {
				s := diagnosticDiffBase()
				s.Running = m&1 != 0
				s.StartFail = m&2 != 0
				s.Chain = m&4 != 0
				s.PutFail = m&8 != 0
				s.ResolverFail = m&16 != 0
				s.BuildFail = m&32 != 0
				if m&64 != 0 {
					s.IPv4 = ""
					s.Trace4 = ""
				}
				diagnosticDiffCompare(t, diagnoseServiceRequest{Op: op}, s, diagnosticDiffRust(t))
			})
		}
	}
}
func TestMigrationDiagnosticActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(220000 + i)))
			s := diagnosticDiffBase()
			s.Running = r.Intn(2) == 0
			s.Chain = r.Intn(4) == 0
			s.StartFail = r.Intn(5) == 0
			s.StopFail = r.Intn(3) == 0
			s.ListFail = r.Intn(3) == 0
			s.PutFail = r.Intn(3) == 0
			s.GetFail = r.Intn(5) == 0
			s.CountryFail = r.Intn(3) == 0
			s.ResolverFail = r.Intn(4) == 0
			s.BuildFail = r.Intn(4) == 0
			s.DetailSuccess = r.Intn(4) - 1
			s.STUNSuccess = r.Intn(5) - 1
			s.Choice = []string{"node", "", autoGroup, fallbackGroup, chainExit}[r.Intn(5)]
			s.Active = []string{"node", "", blockedChoice, autoGroup, fallbackGroup}[r.Intn(5)]
			s.IPv4 = []string{"", "192.0.2.1"}[r.Intn(2)]
			s.Trace4 = []string{"", "192.0.2.2"}[r.Intn(2)]
			s.IPv6 = []string{"", "2001:db8::1"}[r.Intn(2)]
			s.Resolver = []string{"", "not-ip", "198.51.100.1"}[r.Intn(3)]
			s.Country = []string{"", "us", "US ", "FR"}[r.Intn(4)]
			s.Mapped = []string{"", "192.0.2.1:123", "[2001:db8::1]:123", "bad"}[r.Intn(4)]
			s.Body = []byte(fmt.Sprintf(`{"security":{"hosting":%t,"proxy":%t,"vpn":%t,"tor":%t,"anonymous":%t}}`, r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0))
			q := diagnoseServiceRequest{Op: []string{"diagnose", "ensure", "details", "dns", "stun"}[r.Intn(5)]}
			diagnosticDiffCompare(t, q, s, diagnosticDiffRust(t))
		})
	}
}
func TestMigrationDiagnosticActualGoldens(t *testing.T) {
	diagnosticLegacyGoldens(t, diagnosticDiffRust(t))
}
func TestMigrationDiagnosticActualConcurrency(t *testing.T) {
	diagnosticConcurrentGolden(t, diagnosticDiffRust(t))
}
func TestMigrationDiagnosticActualQualityGolden(t *testing.T) {
	run := diagnosticDiffRust(t)
	var bodies [][]byte
	for mask := 0; mask < 32; mask++ {
		bodies = append(bodies, []byte(fmt.Sprintf(`{"security":{"hosting":%t,"proxy":%t,"vpn":%t,"tor":%t,"anonymous":%t}}`, mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0, mask&16 != 0)))
	}
	for _, word := range []string{"Cloudflare", "AMAZON", "digitalocean", "Microsoft", "Google", "Oracle", "Alibaba", "Tencent", "Hetzner", "OVH", "Vultr", "Linode", "Choopa", "m247", "Data Center", "Hosting"} {
		bodies = append(bodies, []byte(fmt.Sprintf(`{"connection":{"isp":%q,"org":"synthetic"}}`, word)))
	}
	bodies = append(bodies, []byte(`{invalid`))
	for i, body := range bodies {
		s := diagnosticDiffBase()
		s.Body = body
		r, _ := diagnosticDiffRun(t, diagnoseServiceRequest{Op: "details"}, s, run)
		want := parseIpQuality(body)
		if r.Quality != want || r.Err != nil {
			t.Fatalf("quality case %d actual=%#v original=%#v err=%v", i, r.Quality, want, r.Err)
		}
	}
}
