//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func labelsDiffRust(t *testing.T) labelsDiffRun {
	t.Helper()
	if !rustLabelsFlowAvailable() {
		t.Fatal("labels DLL unavailable")
	}
	return func(q labelsServiceRequest, f labelsServiceEffects) labelsServiceResult {
		n := labelsFlowRecoveries.Load()
		r, used := runRustLabelsService(q, f)
		if !used || labelsFlowRecoveries.Load() != n {
			t.Fatalf("labels used=%v recovery=%d reason=%v", used, labelsFlowRecoveries.Load()-n, labelsFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationLabelsActualMatrix(t *testing.T) {
	faults := []string{"", "read-original", "decode-original", "rebuild", "index-original", "index-rebuilt", "decode-meta", "marshal", "write-temp", "rename", "save-cache"}
	for _, fault := range faults {
		for mask := 0; mask < 32; mask++ {
			t.Run(fmt.Sprintf("%s/%d", fault, mask), func(t *testing.T) {
				s := labelsDiffBase()
				s.Fail = fault
				s.State.Merged = mask&1 != 0
				s.State.Empty = mask&2 != 0
				s.Valid = mask&4 == 0
				s.Policy = mask&8 != 0
				s.Equal = mask&16 == 0
				labelsDiffCompare(t, s, labelsDiffRust(t))
			})
		}
	}
}
func TestMigrationLabelsActualRandom(t *testing.T) {
	for i := 0; i < 600; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(270000 + i)))
			s := labelsDiffBase()
			s.State.Merged = r.Intn(4) != 0
			s.Count = r.Intn(4)
			s.State.Sources = nil
			for j := 0; j < r.Intn(5); j++ {
				s.State.Sources = append(s.State.Sources, labelsSource{Enabled: r.Intn(2) == 0, Nonempty: r.Intn(2) == 0, Matches: r.Intn(2) == 0, Label: []string{"", "label", "other"}[r.Intn(3)]})
			}
			s.State.Nodes = nil
			for j := 0; j < r.Intn(7); j++ {
				s.State.Nodes = append(s.State.Nodes, labelsNode{Name: []string{"a", "b", "duplicate"}[r.Intn(3)], Label: []string{"", "kept"}[r.Intn(2)], Manual: r.Intn(3) == 0})
			}
			for _, key := range []string{"proxy-groups", "smartvpn-source-rules", "smartvpn-source-labels", "smartvpn-policy-sections", "dns"} {
				s.Existing[key] = r.Intn(2) == 0
				s.Meta[key] = r.Intn(2) == 0
			}
			labelsDiffCompare(t, s, labelsDiffRust(t))
		})
	}
}
func TestMigrationLabelsActualGolden(t *testing.T)        { labelsDiffGolden(t, labelsDiffRust(t)) }
func TestMigrationLabelsActualAdapterGolden(t *testing.T) { labelsAdapterGolden(t, labelsDiffRust(t)) }
