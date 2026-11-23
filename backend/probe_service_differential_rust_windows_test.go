//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func probeDiffRust(t *testing.T) func(chainProbeServiceRequest, chainProbeServiceEffects) chainProbeServiceResult {
	t.Helper()
	if !rustChainProbeFlowAvailable() {
		t.Fatal("actual chain probe DLL unavailable")
	}
	return func(q chainProbeServiceRequest, f chainProbeServiceEffects) chainProbeServiceResult {
		n := chainProbeFlowRecoveries.Load()
		r, used := runRustChainProbeService(q, f)
		if !used || chainProbeFlowRecoveries.Load() != n {
			t.Fatalf("op=%s used=%v recovery=%d reason=%v", q.Op, used, chainProbeFlowRecoveries.Load()-n, chainProbeFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationChainProbeActualMatrix(t *testing.T) {
	for _, op := range []string{"probe", "list", "measure-both", "measure-prefix", "timed-requests"} {
		for m := 0; m < 8; m++ {
			for _, fail := range []string{"", "index", "resolver", "ech", "adapter", "request-new", "request-do", "save"} {
				t.Run(fmt.Sprintf("%s/%d/%s", op, m, fail), func(t *testing.T) {
					s := probeDiffBase()
					s.Store = m&1 != 0
					s.Reused = m&2 != 0
					s.Deadline = m&4 != 0
					s.FailKind = fail
					s.Records = probeDiffRecords()
					probeDiffCompare(t, op, s, probeDiffRust(t))
				})
			}
		}
	}
}
func TestMigrationChainProbeActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(230000 + i)))
			s := probeDiffBase()
			s.Store = r.Intn(3) != 0
			s.Subscription = r.Intn(2) == 0
			s.TUN = r.Intn(2) == 0
			s.StrictDNS = s.TUN && r.Intn(2) == 0
			s.Reused = r.Intn(2) == 0
			s.Resolver = r.Intn(2) == 0
			s.Deadline = r.Intn(2) == 0
			s.FailKind = []string{"", "index", "resolver", "ech", "adapter", "request-new", "request-do", "save", "records"}[r.Intn(9)]
			s.FailAt = r.Intn(2)
			s.Cold = int64(r.Intn(100) - 5)
			s.Warm = int64(r.Intn(100) - 5)
			s.Status = []int{204, 200, 302, 500}[r.Intn(4)]
			s.IP = []string{"", "192.0.2.1", "::ffff:192.0.2.8", "bad"}[r.Intn(4)]
			s.Records = probeDiffRecords()
			if r.Intn(3) == 0 {
				s.Records[0].Details = "{"
			}
			if r.Intn(2) == 0 {
				s.Policy = nil
			}
			op := []string{"probe", "list", "measure-both", "measure-prefix", "timed-requests"}[r.Intn(5)]
			probeDiffCompare(t, op, s, probeDiffRust(t))
		})
	}
}
func TestMigrationChainProbeActualGoldens(t *testing.T) { probeLegacyGoldens(t, probeDiffRust(t)) }
func TestMigrationChainProbeActualAlias(t *testing.T)   { probeAliasGoldens(t, probeDiffRust(t)) }
func TestMigrationChainProbeActualEdges(t *testing.T)   { probeEdgeGoldens(t, probeDiffRust(t)) }
