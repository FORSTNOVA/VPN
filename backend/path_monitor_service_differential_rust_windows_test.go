//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func monitorDiffRust(t *testing.T) func(pathMonitorServiceRequest, pathMonitorServiceEffects) pathMonitorServiceResult {
	t.Helper()
	if !rustPathMonitorFlowAvailable() {
		t.Fatal("actual scheduler DLL unavailable")
	}
	return func(q pathMonitorServiceRequest, f pathMonitorServiceEffects) pathMonitorServiceResult {
		n := pathMonitorFlowRecoveries.Load()
		r, used := runRustPathMonitorService(q, f)
		if !used || pathMonitorFlowRecoveries.Load() != n {
			t.Fatalf("op=%s used=%v recovery=%d reason=%v", q.Op, used, pathMonitorFlowRecoveries.Load()-n, pathMonitorFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationPathMonitorActualMatrix(t *testing.T) {
	for _, op := range []string{"start", "stop", "loop", "demand", "retry", "normalize", "update", "snapshot", "manual", "cancel"} {
		for m := 0; m < 128; m++ {
			t.Run(fmt.Sprintf("%s/%d", op, m), func(t *testing.T) {
				s := monitorDiffBase()
				s.Running = m&1 != 0
				s.Enabled = m&2 != 0
				s.Busy = m&4 != 0
				s.Cancel = m&8 != 0
				s.Old = m&16 != 0
				s.FetchFail = m&32 != 0
				s.SaveFail = m&64 != 0
				monitorDiffCompare(t, op, s, monitorDiffRust(t))
			})
		}
	}
}
func TestMigrationPathMonitorActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(250000 + i)))
			s := monitorDiffBase()
			s.Running = r.Intn(4) != 0
			s.Enabled = r.Intn(3) != 0
			s.Busy = r.Intn(5) == 0
			s.Cancel = r.Intn(3) != 0
			s.Old = r.Intn(5) == 0
			s.FetchFail = r.Intn(5) == 0
			s.SaveFail = r.Intn(3) == 0
			s.ReplaceFetch = r.Intn(5) == 0
			s.ReplaceWait = r.Intn(7) - 1
			s.Grow = r.Intn(2) == 0
			s.Step = time.Duration(r.Intn(32)) * time.Second
			s.Events = nil
			for j := 0; j < r.Intn(7); j++ {
				s.Events = append(s.Events, []string{"baseline", "demand", "retry"}[r.Intn(3)])
			}
			s.Events = append(s.Events, "cancel")
			s.Retry = []string{"", "bad", monitorDiffNow.Add(-time.Second).Format(time.RFC3339Nano), monitorDiffNow.Add(time.Hour).Format(time.RFC3339Nano)}[r.Intn(4)]
			op := []string{"start", "stop", "loop", "demand", "retry", "normalize", "update", "snapshot", "manual", "cancel"}[r.Intn(10)]
			monitorDiffCompare(t, op, s, monitorDiffRust(t))
		})
	}
}
func TestMigrationPathMonitorActualGoldens(t *testing.T) {
	monitorLegacyGoldens(t, monitorDiffRust(t))
	monitorThrottleGolden(t, monitorDiffRust(t))
}
func TestMigrationPathMonitorActualWorkerAdmission(t *testing.T) {
	monitorWorkerGolden(t, monitorDiffRust(t))
}
func TestMigrationPathMonitorActualNormalizeLegacy(t *testing.T) {
	run := monitorDiffRust(t)
	for i, in := range migrationMonitorNormalizeCases() {
		q := monitorDiffRequest("normalize")
		q.Settings = in
		r, _ := monitorDiffRun(t, q, monitorDiffBase(), run)
		want, err := migrationOriginalNormalizePathChecks(in)
		if fmt.Sprint(r.Err) != fmt.Sprint(err) || !reflect.DeepEqual(r.Settings, want) {
			t.Fatalf("normalize%d got=%#v %v original=%#v %v", i, r.Settings, r.Err, want, err)
		}
	}
}
func TestMigrationPathMonitorActualDemandLegacy(t *testing.T) {
	run := monitorDiffRust(t)
	for i := 0; i < 100; i++ {
		r := rand.New(rand.NewSource(int64(252000 + i)))
		s := monitorDiffBase()
		s.Traffic.Connections = nil
		for j := 0; j < 20; j++ {
			s.Traffic.Connections = append(s.Traffic.Connections, kernelConnection{ID: []string{"", "old", fmt.Sprint(j)}[r.Intn(3)], Start: "start", Upload: int64(r.Intn(15) - 2), Metadata: kernelMetadata{Process: []string{"browser", "SMARTVPN", "mysmartvpn-helper"}[r.Intn(3)], Host: []string{"sub.synthetic.invalid", "synthetic.invalid.", "notsynthetic.invalid", "", "GITHUB.COM"}[r.Intn(5)], DestinationPort: "443"}})
		}
		q := monitorDiffRequest("demand")
		previous := monitorCopySamples(q.Previous)
		actual, f := monitorDiffRun(t, q, s, run)
		targets := otherPathTargets(f.a.enabledPathTargetsLocked())
		want := migrationOriginalPathDemand(s.Traffic, targets, previous)
		if !reflect.DeepEqual(actual.Labels, want) || !reflect.DeepEqual(q.Previous, previous) {
			t.Fatalf("demand%d legacylabels=%v actual=%v previous=%v actualprev=%v", i, want, actual.Labels, previous, q.Previous)
		}
	}
}
func TestMigrationPathMonitorActualSnapshotShape(t *testing.T) {
	monitorShapeGolden(t, monitorDiffRust(t))
}
