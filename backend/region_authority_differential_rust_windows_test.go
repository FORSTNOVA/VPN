//go:build windows && rustcore

package main

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestServiceStateActualRegionContracts(t *testing.T) {
	authorityRequireRust(t)
	regionAuthorityGolden(t, authorityRustFactory)
	regionAuthorityShapeGolden(t, authorityRustFactory)
	regionAuthorityNarrowGolden(t, authorityRustFactory)
	regionAuthorityFaultGolden(t, authorityRustFactory)
}
func regionAuthorityRustJob(t *testing.T) regionAuthorityJobRun {
	authorityRequireRust(t)
	if !rustRegionJobFlowAvailable() {
		t.Fatal("region job planner unavailable")
	}
	return func(q regionJobServiceRequest, f regionJobServiceEffects) regionJobServiceResult {
		n := regionJobFlowRecoveries.Load()
		r, used := runRustRegionJobService(q, f)
		if !used || regionJobFlowRecoveries.Load() != n {
			t.Fatalf("job fallback/recovery op=%s used=%v reason=%v", q.Op, used, regionJobFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestServiceStateActualRegionWorkflows(t *testing.T) {
	authorityRequireRust(t)
	if !rustHealthFlowAvailable() {
		t.Fatal("health planner unavailable")
	}
	regionAuthorityProtectedWithRun(t, authorityRustFactory, func(s *healthServiceState, q healthServiceRequest, fx healthServiceEffects) {
		n := healthFlowRecoveries.Load()
		if !runRustHealthService(s, q, fx) {
			t.Fatal("health planner prestart fallback")
		}
		if healthFlowRecoveries.Load() != n {
			t.Fatal("health planner normal tail recovery")
		}
	})
	regionAuthorityJobGolden(t, authorityRustFactory, regionAuthorityRustJob(t))
	regionAuthorityReadFailureGolden(t, authorityRustFactory)
}
func TestServiceStateActualRegionGoPlannerNativeOwner(t *testing.T) {
	authorityRequireRust(t)
	regionAuthorityJobGolden(t, authorityRustFactory, runGoRegionJobService)
}
func TestServiceStateActualRegionRandom(t *testing.T) {
	authorityRequireRust(t)
	g, r := regionAuthorityNew(t, authorityGoFactory), regionAuthorityNew(t, authorityRustFactory)
	rnd := rand.New(rand.NewSource(300000))
	now := time.Now()
	for i := 0; i < 1400; i++ {
		key := []string{"a", "b", "different key", ""}[rnd.Intn(4)]
		at := now.Add(time.Duration(i) * time.Millisecond)
		action := rnd.Intn(12)
		for _, a := range []*app{g, r} {
			switch action {
			case 0:
				a.writeHealthRecordLocked(key, nodeHealth{Name: "not-key", State: healthHealthy, LatencyMs: i, LastSuccessAt: at, UpdatedAt: at, CooldownUntil: at.Add(time.Second)})
			case 1:
				a.writeRegionRecordLocked(key, regionRecord{Name: "record-name", Country: "JP", Status: regionVerified, VerifiedAt: at, Sources: []string{"source"}})
			case 2:
				a.setHealthBlockedLocked(i%2 == 0)
			case 3:
				a.setHealthReasonLocked("reason")
			case 4:
				a.setHealthNoteLocked("note")
			case 5:
				a.setHealthLastSwitchLocked(at)
			case 6:
				a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateCreateJob, Job: &regionJob{Running: true, Total: i, LastError: "old"}}, nil)
			case 7:
				if a.healthMetaLocked().Job != nil {
					a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobAdvance, Text: "verified"}, nil)
				}
			case 8:
				a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobFinish, Text: "finished", Flag: i%2 == 0, Detail: "failure"}, nil)
			case 9:
				if e := a.service.baseline(trafficSample{at: at, up: int64(i), down: int64(i * 2)}); e != nil {
					t.Fatal(e)
				}
			case 10:
				a.pathClockCommandLocked(pathStateAt, at, "", 0)
			case 11:
				a.replaceHealthParamsLocked(defaultHealthParams())
			}
		}
		gp, rp := g.healthDomainLocked(), r.healthDomainLocked()
		if !reflect.DeepEqual(gp, rp) {
			t.Fatalf("typed health state step%d\nGo=%+v\nRust=%+v", i, gp, rp)
		}
		gs, rs := authoritySnapshot(t, g.service), authoritySnapshot(t, r.service)
		gs.Epoch = 0
		rs.Epoch = 0
		if gs != rs {
			t.Fatalf("three-domain metadata differs step%d", i)
		}
		if _, ok := r.service.backend.(*rustServiceStateBackend); !ok {
			t.Fatal("native owner replaced")
		}
		if len(r.service.clocks) > 19 {
			t.Fatal("clock ledger grew", len(r.service.clocks))
		}
	}
}
func TestServiceStateActualRegionReport(t *testing.T) {
	authorityRequireRust(t)
	if !rustRegionFlowAvailable() {
		t.Fatal("region planner unavailable")
	}
	regionAuthorityReportGolden(t, authorityRustFactory, func(q regionServiceRequest, f regionServiceEffects) regionServiceResult {
		n := regionFlowRecoveries.Load()
		r, used := runRustRegionService(q, f)
		if !used || regionFlowRecoveries.Load() != n {
			t.Fatal("normal region planner recovery")
		}
		return r
	})
}
