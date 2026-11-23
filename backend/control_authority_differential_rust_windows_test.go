//go:build windows && rustcore

package main

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestServiceStateActualControlContracts(t *testing.T) {
	authorityRequireRust(t)
	controlAuthorityGolden(t, authorityRustFactory)
	controlAuthorityStaleGolden(t, authorityRustFactory)
	controlAuthorityPartialGolden(t, authorityRustFactory)
}
func TestServiceStateActualControlRandomFourDomains(t *testing.T) {
	authorityRequireRust(t)
	a := regionAuthorityNew(t, authorityRustFactory)
	b := regionAuthorityNew(t, func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
	owner := a.service.backend
	r := rand.New(rand.NewSource(310071))
	now := time.Now()
	for i := 0; i < 1800; i++ {
		action := r.Intn(8)
		value := r.Intn(10000)
		mask := uint64(1) << uint(r.Intn(18))
		v := serviceControlState{SubscriptionURL: "synthetic", SelectedNode: "chosen", LockedRegion: "JP", LockedNode: "disk", RuntimeLockedRegion: "US", RuntimeLockedNode: "live", AutoMergeHours: value, SocksPort: value, GameBypass: []string{}}
		for _, x := range []*app{a, b} {
			switch action {
			case 0, 1, 2:
				x.patchControlLocked(mask, v)
			case 3:
				if e := x.service.baseline(trafficSample{at: now.Add(time.Duration(i) * time.Second), up: int64(value), down: int64(i)}); e != nil {
					t.Fatal(e)
				}
			case 4:
				x.replacePathConfigLocked(pathCheckSettings{Enabled: i%2 == 0, IntervalSec: value})
			case 5:
				x.writeHealthRecordLocked("key", nodeHealth{Name: "different", State: healthHealthy, UpdatedAt: now})
			case 6:
				x.writeRegionRecordLocked("key", regionRecord{Name: "different", Country: "JP", VerifiedAt: now})
			case 7:
				x.setHealthBlockedLocked(i%2 == 0)
			}
		}
		if !reflect.DeepEqual(a.controlLocked(), b.controlLocked()) || !reflect.DeepEqual(a.healthDomainLocked(), b.healthDomainLocked()) || !reflect.DeepEqual(a.pathConfigLocked(), b.pathConfigLocked()) {
			t.Fatalf("four domain mismatch %d", i)
		}
		av, bv := authoritySnapshot(t, a.service), authoritySnapshot(t, b.service)
		av.Epoch = 0
		bv.Epoch = 0
		if !reflect.DeepEqual(av, bv) {
			t.Fatalf("metadata mismatch %d", i)
		}
		if a.service.backend != owner {
			t.Fatalf("owner transferred %d", i)
		}
	}
}
func TestServiceStateActualControlWorkflow(t *testing.T) {
	authorityRequireRust(t)
	if !rustSettingsFlowAvailable() || !rustChainFlowAvailable() {
		t.Fatal("planner unavailable")
	}
	sr := func(s *settingsServiceState, q settingsServiceRequest, f settingsServiceEffects) settingsServiceResult {
		before := settingsFlowRecoveries.Load()
		r, ok := runRustSettingsService(s, q, f)
		if !ok || settingsFlowRecoveries.Load() != before {
			t.Fatal("settings planner recovered")
		}
		return r
	}
	cr := func(q chainServiceRequest, f chainServiceEffects) chainServiceResult {
		before := chainFlowRecoveries.Load()
		r, ok := runRustChainService(q, f)
		if !ok || chainFlowRecoveries.Load() != before {
			t.Fatal("chain planner recovered")
		}
		return r
	}
	controlAuthorityWorkflowGolden(t, authorityRustFactory, sr, cr)
	controlAuthorityWorkflowGolden(t, authorityRustFactory, runGoSettingsService, runGoChainService)
}
func TestServiceStateActualControlCatalog(t *testing.T) {
	authorityRequireRust(t)
	controlAuthorityCatalogGolden(t, authorityRustFactory)
}
