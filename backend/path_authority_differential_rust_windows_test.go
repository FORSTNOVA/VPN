//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestServiceStateActualPathContracts(t *testing.T) {
	authorityRequireRust(t)
	pathAuthorityGolden(t, authorityRustFactory)
	pathAuthorityJSONGolden(t, authorityRustFactory)
	pathAuthorityInvalidGolden(t, authorityRustFactory)
}
func TestServiceStateActualPathSharedRandom(t *testing.T) {
	authorityRequireRust(t)
	g, r := authorityNew(t, authorityGoFactory), authorityNew(t, authorityRustFactory)
	ga, ra := &app{service: g}, &app{service: r}
	random := rand.New(rand.NewSource(290000))
	now := time.Now()
	for i := 0; i < 1600; i++ {
		p := pathAuthorityState(t, g)
		c := servicePathCommand{}
		switch random.Intn(16) {
		case 0:
			c = servicePathCommand{Action: pathStateConfig, Config: pathCheckSettings{Enabled: random.Intn(2) == 0, IntervalSec: random.Intn(300), Targets: []pathTarget{{Label: "fake", URL: "https://synthetic.invalid"}}}}
		case 1:
			if p.MonitorActive {
				c.Action = pathStateRevokeMonitor
			} else {
				c.Action = pathStateOpenMonitor
			}
		case 2:
			if p.Busy {
				c = servicePathCommand{Action: pathStateFinishRound, LeaseID: p.LeaseID, Flag: random.Intn(2) == 0}
			} else {
				c.Action = pathStateBeginRound
			}
		case 3:
			c = servicePathCommand{Action: pathStateRunning, Flag: random.Intn(2) == 0}
		case 4:
			c = servicePathCommand{Action: pathStateNote, Text: []string{"", "synthetic note", "非ASCII\x00"}[random.Intn(3)]}
		case 5:
			c = servicePathCommand{Action: pathStateAlert, Text: "alert"}
		case 6:
			c = servicePathCommand{Action: pathStateDemanded, Flag: random.Intn(2) == 0}
		case 7:
			c = servicePathCommand{Action: pathStateRetryMode, Text: "scan"}
		case 8:
			c = servicePathCommand{Action: pathStateIncrementAlert}
		case 9:
			c = servicePathCommand{Action: pathStateResults, Results: []siteCheckResult{{Name: "fake", State: "ok", HTTPCode: 403}}}
		case 10:
			c = servicePathCommand{Action: pathStateFailures, Strings: []string{"a", "b"}}
		case 11:
			c = servicePathCommand{Action: pathStateReplaceSwitched, Strings: []string{"a", "b"}}
		case 12:
			c = servicePathCommand{Action: pathStateSamples, Generation: p.Generation, Samples: []servicePathSample{{ID: "opaque", Upload: int64(i)}}}
			if !p.MonitorActive {
				c = servicePathCommand{Action: pathStateNote, Text: "off"}
			}
		case 13:
			ga.pathClockCommandLocked(pathStateAt, now.Add(time.Duration(i)*time.Nanosecond), "", 0)
			ra.pathClockCommandLocked(pathStateAt, now.Add(time.Duration(i)*time.Nanosecond), "", 0)
		case 14:
			sample := trafficSample{at: now.Add(time.Duration(i) * time.Second), up: int64(i), down: int64(i * 2)}
			if e := g.baseline(sample); e != nil {
				t.Fatal(e)
			}
			if e := r.baseline(sample); e != nil {
				t.Fatal(e)
			}
		case 15:
			if e := g.samplingError("history"); e != nil {
				t.Fatal(e)
			}
			if e := r.samplingError("history"); e != nil {
				t.Fatal(e)
			}
		}
		if c.Action != 0 {
			pathAuthorityApply(t, g, c)
			pathAuthorityApply(t, r, c)
		}
		gs, rs := authoritySnapshot(t, g), authoritySnapshot(t, r)
		gs.Epoch = 0
		rs.Epoch = 0
		if gs != rs {
			t.Fatalf("step %d outer state differs", i)
		}
		if !reflect.DeepEqual(pathAuthorityState(t, g), pathAuthorityState(t, r)) {
			t.Fatalf("step %d typed path state differs", i)
		}
		if _, ok := r.backend.(*rustServiceStateBackend); !ok {
			t.Fatal("normal path request fell back owner")
		}
		if len(r.clocks) > 2 {
			t.Fatalf("clock ledger unbounded %d", len(r.clocks))
		}
	}
}
func TestServiceStateActualPathFaults(t *testing.T) {
	authorityRequireRust(t)
	pathAuthorityFaultGolden(t, authorityRustFactory)
}
func pathAuthorityRustRun(t *testing.T) pathAuthorityRun {
	authorityRequireRust(t)
	if !rustPathFlowAvailable() {
		t.Fatal("path flow unavailable")
	}
	return func(q pathServiceRequest, f pathServiceEffects) pathServiceResult {
		n := pathFlowRecoveries.Load()
		r, used := runRustPathService(q, f)
		if !used || pathFlowRecoveries.Load() != n {
			t.Fatalf("path planner normal recovery %v %v", used, pathFlowRecoveryReason.Load())
		}
		return r
	}
}
func monitorAuthorityRustRun(t *testing.T) monitorAuthorityRun {
	authorityRequireRust(t)
	if !rustPathMonitorFlowAvailable() {
		t.Fatal("monitor flow unavailable")
	}
	return func(q pathMonitorServiceRequest, f pathMonitorServiceEffects) pathMonitorServiceResult {
		n := pathMonitorFlowRecoveries.Load()
		r, used := runRustPathMonitorService(q, f)
		if !used || pathMonitorFlowRecoveries.Load() != n {
			t.Fatalf("monitor planner normal recovery %v %v", used, pathMonitorFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestServiceStateActualPathWorkflows(t *testing.T) {
	pathAuthorityRoundGolden(t, authorityRustFactory, pathAuthorityRustRun(t))
	pathAuthorityMonitorGolden(t, authorityRustFactory, monitorAuthorityRustRun(t))
	pathAuthorityFreshCompare(t, authorityRustFactory, pathAuthorityRustRun(t))
}
func TestServiceStateActualPathGoPlannerNativeOwner(t *testing.T) {
	authorityRequireRust(t)
	pathAuthorityRoundGolden(t, authorityRustFactory, runGoPathService)
	pathAuthorityMonitorGolden(t, authorityRustFactory, runGoPathMonitorService)
}
func TestServiceStateActualPathMonitorMatrix(t *testing.T) {
	for _, op := range []string{"start", "stop", "loop", "demand", "retry", "normalize", "update", "snapshot", "manual", "cancel"} {
		for mask := 0; mask < 32; mask++ {
			t.Run(fmt.Sprintf("%s/%d", op, mask), func(t *testing.T) {
				s := monitorDiffBase()
				s.Enabled = mask&1 != 0
				s.Busy = mask&2 != 0
				s.Old = mask&4 != 0
				s.FetchFail = mask&8 != 0
				s.ReplaceFetch = mask&16 != 0
				monitorAuthorityDiff(t, op, s, authorityRustFactory, monitorAuthorityRustRun(t))
			})
		}
	}
}
func TestServiceStateActualPathWorkerOwnership(t *testing.T) {
	monitorWorkerGoldenWithFactory(t, monitorAuthorityRustRun(t), authorityRustFactory)
}
func TestServiceStateActualPathThrottleAndStaleGET(t *testing.T) {
	for _, step := range []time.Duration{29 * time.Second, 30 * time.Second} {
		s := monitorDiffBase()
		s.Grow = true
		s.Step = step
		s.Events = []string{"demand", "demand", "cancel"}
		_, f := monitorAuthorityDiffRun(t, monitorDiffRequest("loop"), s, authorityRustFactory, monitorAuthorityRustRun(t))
		n := 0
		for _, x := range f.trace {
			if x.Effect.Kind == "round" && x.Effect.Name == "demand" {
				n++
			}
		}
		want := 1
		if step == 30*time.Second {
			want = 2
		}
		if n != want {
			t.Fatalf("native throttle %v got=%d want=%d", step, n, want)
		}
	}
	s := monitorDiffBase()
	s.ReplaceFetch = true
	s.Events = []string{"demand", "cancel"}
	_, f := monitorAuthorityDiffRun(t, monitorDiffRequest("loop"), s, authorityRustFactory, monitorAuthorityRustRun(t))
	p := pathAuthorityState(t, f.a.service)
	if len(p.Previous) != 0 || p.Status.Note != "concurrent edit" {
		t.Fatal("stale GET committed or overwrote current native state")
	}
}
