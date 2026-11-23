//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func pathDiffRust(t *testing.T) func(pathServiceRequest, pathServiceEffects) pathServiceResult {
	t.Helper()
	if !rustPathFlowAvailable() {
		t.Fatal("actual path DLL unavailable")
	}
	return func(q pathServiceRequest, f pathServiceEffects) pathServiceResult {
		n := pathFlowRecoveries.Load()
		r, used := runRustPathService(q, f)
		if !used || pathFlowRecoveries.Load() != n {
			t.Fatalf("op=%s mode=%s used=%v recovery=%d reason=%v", q.Op, q.Mode, used, pathFlowRecoveries.Load()-n, pathFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationPathActualMatrix(t *testing.T) {
	for _, q := range pathDiffRequests() {
		for m := 0; m < 128; m++ {
			t.Run(fmt.Sprintf("%s-%s/%d", q.Op, q.Mode, m), func(t *testing.T) {
				s := pathDiffBase()
				s.Running = m&1 != 0
				s.Enabled = m&2 != 0
				s.Busy = m&4 != 0
				s.Chain = m&8 != 0
				s.Demanded = m&16 != 0
				s.SaveFail = m&32 != 0
				if m&64 != 0 {
					s.Pass = map[string]bool{"a|Google": false, "a|Extra": false, "b|Google": false, "b|Extra": false, "c|Google": false, "c|Extra": false}
				}
				pathDiffCompare(t, q, s, pathDiffRust(t))
			})
		}
	}
}
func TestMigrationPathActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(240000 + i)))
			s := pathDiffBase()
			s.Running = r.Intn(5) != 0
			s.Enabled = r.Intn(3) != 0
			s.Busy = r.Intn(5) == 0
			s.Chain = r.Intn(4) == 0
			s.Demanded = r.Intn(2) == 0
			s.SaveFail = r.Intn(2) == 0
			s.GetFail = r.Intn(5) == 0
			s.Cooldown = r.Intn(2) == 0
			s.Expired = r.Intn(2) == 0
			s.FailPut = []string{"", "a", "b", "c"}[r.Intn(4)]
			s.Region = []string{"US", ""}[r.Intn(2)]
			s.Active = []string{"a", "", "b"}[r.Intn(3)]
			s.EditAt = []int{-1, -1, 0, 2}[r.Intn(4)]
			for _, n := range []string{"a", "b", "c"} {
				for _, label := range []string{"Google", "YouTube", "Extra"} {
					s.Pass[n+"|"+label] = r.Intn(2) == 0
				}
			}
			q := pathDiffRequests()[r.Intn(7)]
			pathDiffCompare(t, q, s, pathDiffRust(t))
		})
	}
}
func TestMigrationPathActualGoldens(t *testing.T)      { pathLegacyGoldens(t, pathDiffRust(t)) }
func TestMigrationPathActualRevalidation(t *testing.T) { pathRevalidationGoldens(t, pathDiffRust(t)) }
