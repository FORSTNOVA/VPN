//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func trafficDiffRust(t *testing.T) func(trafficServiceRequest, trafficServiceEffects) trafficServiceResult {
	t.Helper()
	if !rustTrafficFlowAvailable() {
		t.Fatal("actual traffic DLL unavailable")
	}
	return func(q trafficServiceRequest, fx trafficServiceEffects) trafficServiceResult {
		before := trafficFlowRecoveries.Load()
		r, used := runRustTrafficService(q, fx)
		if !used || trafficFlowRecoveries.Load() != before {
			t.Fatalf("op %s not strict Rust: used=%v recovery=%v reason=%v", q.Op, used, trafficFlowRecoveries.Load()-before, trafficFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationTrafficActualMatrix(t *testing.T) {
	for _, op := range []string{"start", "stop", "collect", "loop", "current", "query"} {
		for mask := 0; mask < 64; mask++ {
			t.Run(fmt.Sprintf("%s/%d", op, mask), func(t *testing.T) {
				s := trafficDiffBase()
				s.Store = mask&1 != 0
				s.Cancel = mask&2 != 0
				s.Running = mask&4 != 0
				s.GetFail = mask&8 != 0
				s.RecordFail = mask&16 != 0
				s.ReadFail = mask&32 != 0
				trafficDiffCompare(t, trafficServiceRequest{Op: op}, s, trafficDiffRust(t))
			})
		}
	}
}
func TestMigrationTrafficActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(210000 + i)))
			s := trafficDiffBase()
			s.Store = r.Intn(2) == 0
			s.Cancel = r.Intn(2) == 0
			s.Running = r.Intn(2) == 0
			s.GetFail = r.Intn(3) == 0
			s.RecordFail = r.Intn(3) == 0
			s.ReadFail = r.Intn(3) == 0
			s.ReplaceAt = r.Intn(5) - 1
			s.Events = nil
			for j := 0; j < r.Intn(6); j++ {
				s.Events = append(s.Events, true)
			}
			s.Events = append(s.Events, false)
			s.Snapshot.UploadTotal = r.Int63()
			s.Snapshot.DownloadTotal = r.Int63()
			q := trafficServiceRequest{Op: []string{"start", "stop", "collect", "loop", "current", "query"}[r.Intn(6)]}
			if q.Op == "query" {
				q.Period = []string{"", "day", "hour", "month", "year", "day ", "invalid"}[r.Intn(7)]
				q.Group = []string{"", "app", "connection", " app"}[r.Intn(4)]
				q.Date = []string{"", "2026-03-08", "wrong"}[r.Intn(3)]
				q.Limit = []string{"", "+1", "200", "201", " 1", "-1"}[r.Intn(6)]
				q.Offset = []string{"", "0", "+1", "9223372036854775807", "9223372036854775808"}[r.Intn(5)]
				q.App = " raw app "
			}
			trafficDiffCompare(t, q, s, trafficDiffRust(t))
		})
	}
}
func TestMigrationTrafficActualGoldens(t *testing.T) { trafficLegacyGoldens(t, trafficDiffRust(t)) }
func TestMigrationTrafficActualSharedLifecycle(t *testing.T) {
	trafficLifecycleGolden(t, trafficDiffRust(t))
}
