//go:build windows && rustcore

package main

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestServiceStateActualCatalogContracts(t *testing.T) {
	authorityRequireRust(t)
	catalogAuthorityGolden(t, authorityRustFactory)
	catalogAuthorityShapeGolden(t, authorityRustFactory)
	catalogAuthorityBoundedGolden(t, authorityRustFactory)
}
func TestServiceStateActualCatalogWorkflows(t *testing.T) {
	authorityRequireRust(t)
	if !rustSubscriptionFlowAvailable() || !rustQuotaFlowAvailable() {
		t.Fatal("planner unavailable")
	}
	sr := func(s *subscriptionServiceState, q subscriptionServiceRequest, f subscriptionServiceEffects) subscriptionServiceResult {
		n := subscriptionFlowRecoveries.Load()
		r, ok := runRustSubscriptionService(s, q, f)
		if !ok || subscriptionFlowRecoveries.Load() != n {
			t.Fatal("registry recovery", subscriptionFlowRecoveryReason.Load())
		}
		return r
	}
	qr := func(q quotaServiceRequest, f quotaServiceEffects) quotaServiceResult {
		n := quotaFlowRecoveries.Load()
		r, ok := runRustQuotaService(q, f)
		if !ok || quotaFlowRecoveries.Load() != n {
			t.Fatal("quota recovery")
		}
		return r
	}
	catalogRegistryGolden(t, authorityRustFactory, sr)
	catalogQuotaGolden(t, authorityRustFactory, qr)
	catalogRegistryGolden(t, authorityRustFactory, runGoSubscriptionService)
	catalogQuotaGolden(t, authorityRustFactory, runGoQuotaService)
}
func TestServiceStateActualCatalogRandomFiveDomains(t *testing.T) {
	authorityRequireRust(t)
	a := regionAuthorityNew(t, authorityRustFactory)
	b := regionAuthorityNew(t, func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
	owner := a.service.backend
	shared := &subscriptionUserInfo{Total: 99}
	for _, x := range []*app{a, b} {
		testCatalogWrite(x, []subscriptionEntry{{ID: "dup", URL: "a", UserInfo: shared}, {ID: "dup", URL: "b", UserInfo: shared}})
	}
	rng := rand.New(rand.NewSource(320091))
	now := time.Now()
	for i := 0; i < 1800; i++ {
		op := rng.Intn(7)
		v := rng.Intn(9999)
		for _, x := range []*app{a, b} {
			switch op {
			case 0:
				x.patchCatalogEntryLocked(i%2, catalogNodeCount, subscriptionEntry{NodeCount: v})
			case 1:
				x.setProfileFromCacheLocked(i%2 == 0)
			case 2:
				x.patchControlLocked(controlAutoMergeHours, serviceControlState{AutoMergeHours: v})
			case 3:
				x.replacePathConfigLocked(pathCheckSettings{IntervalSec: v})
			case 4:
				x.writeHealthRecordLocked("key", nodeHealth{Name: "different", UpdatedAt: now})
			case 5:
				if e := x.service.baseline(trafficSample{at: now.Add(time.Duration(i)), up: int64(v)}); e != nil {
					t.Fatal(e)
				}
			case 6:
				shared.Total = int64(v)
			}
		}
		if !reflect.DeepEqual(testCatalogRead(a), testCatalogRead(b)) || !reflect.DeepEqual(a.controlLocked(), b.controlLocked()) || !reflect.DeepEqual(a.healthDomainLocked(), b.healthDomainLocked()) || !reflect.DeepEqual(a.pathConfigLocked(), b.pathConfigLocked()) {
			t.Fatalf("five domain mismatch %d", i)
		}
		av, bv := authoritySnapshot(t, a.service), authoritySnapshot(t, b.service)
		av.Epoch = 0
		bv.Epoch = 0
		if !reflect.DeepEqual(av, bv) {
			t.Fatalf("metadata mismatch %d", i)
		}
		if a.service.backend != owner || !catalogQuotaMatches(a.service, testCatalogRead(a)[0].UserInfo, shared, subscriptionUserInfo{Total: 99}) {
			t.Fatalf("owner/resource changed %d", i)
		}
	}
}
func TestServiceStateActualCatalogStale(t *testing.T) {
	authorityRequireRust(t)
	catalogAuthorityStaleGolden(t, authorityRustFactory)
}
