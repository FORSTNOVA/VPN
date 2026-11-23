package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func regionAuthoritySeed(t *testing.T, a *app, factory authorityFactory) {
	t.Helper()
	p := a.healthDomainLocked()
	control := a.controlLocked()
	catalog := a.catalogEntriesLocked()
	fromCache := a.profileFromCacheLocked()
	old := a.service
	a.service = authorityNew(t, factory)
	a.patchControlLocked(controlAll, control)
	a.restoreCatalogEntriesLocked(catalog)
	a.setProfileFromCacheLocked(fromCache)
	testEditHealth(a, func(h *healthDomainProjection) { *h = p })
	if old != nil {
		_ = old.Close()
	}
}
func regionAuthorityProtectedGolden(t *testing.T, factory authorityFactory) {
	regionAuthorityProtectedWithRun(t, factory, runSelectedHealthService)
}
func regionAuthorityProtectedWithRun(t *testing.T, factory authorityFactory, run func(*healthServiceState, healthServiceRequest, healthServiceEffects)) {
	for _, passes := range []bool{true, false} {
		t.Run(map[bool]string{true: "independent-passes", false: "exhausted"}[passes], func(t *testing.T) {
			a, calls, confirms := migrationHealthApp(t)
			regionAuthoritySeed(t, a, factory)
			owner := a.service.backend
			now := time.Unix(1900000000, 0)
			for _, key := range []string{"active", "standby"} {
				a.writeRegionRecordLocked(key, regionRecord{Name: key, Country: "JP", Status: regionVerified, VerifiedAt: now})
			}
			a.writeRegionRecordLocked("foreign", regionRecord{Name: "foreign", Country: "US", Status: regionVerified, VerifiedAt: now})
			a.confirmProbe = func() bool { *confirms++; return passes }
			params := a.healthMetaLocked().Params
			rounds := params.FailureThreshold
			if passes {
				rounds += 2
			}
			for i := 0; i < rounds; i++ {
				state := healthStateFromApp(a)
				run(&state, healthServiceRequest{Kind: "patrol", Active: "active", Delays: map[string]int{}, Now: now.Add(time.Duration(i) * time.Minute)}, appHealthEffects{a})
				applyHealthServiceState(a, state)
				if a.service.backend != owner {
					t.Fatal("health workflow changed authority")
				}
			}
			meta := a.healthMetaLocked()
			if a.controlLocked().RuntimeLockedRegion != "JP" || a.controlLocked().RuntimeLockedNode != "active" {
				t.Fatal("cross region/selection changed")
			}
			puts := 0
			for _, c := range *calls {
				if strings.HasPrefix(c, "PUT ") {
					puts++
					if !strings.Contains(c, blockedChoice) {
						t.Fatal("exhaustion crossed region")
					}
				}
			}
			if passes {
				if meta.Blocked || puts != 0 || meta.SweepNote == "" {
					t.Fatal("batch allfail blocked independently passing tunnel")
				}
			} else {
				if !meta.Blocked || puts != 1 {
					t.Fatal("exhausted region not single failclosed", puts)
				}
			}
			if meta.Params != params {
				t.Fatal("cadence changed")
			}
		})
	}
}

type regionAuthorityJobRun func(regionJobServiceRequest, regionJobServiceEffects) regionJobServiceResult

func regionAuthorityJobGolden(t *testing.T, factory authorityFactory, run regionAuthorityJobRun) {
	a := regionAuthorityNew(t, factory)
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	a.store = &store{}
	owner := a.service.backend
	fx := safeRegionJobFX(a)
	r := run(regionJobServiceRequest{Op: "start"}, fx)
	if r.Err != nil || r.Status != 200 || !a.healthMetaLocked().Job.Running {
		t.Fatal("start failed", r)
	}
	fx = safeRegionJobFX(a)
	fx.list = func() ([]proxyNode, error) { return []proxyNode{{Name: "a"}, {Name: "b"}}, nil }
	fx.delays = func(time.Duration) (map[string]int, error) { return map[string]int{"a": 10, "b": 20}, nil }
	saves := 0
	fx.save = func(regionRecord) error { saves++; return errors.New("synthetic store fail") }
	r = run(regionJobServiceRequest{Op: "run"}, fx)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	p := a.healthDomainLocked()
	if saves != 2 || len(p.Regions) != 2 || p.Job.Running || p.Job.Done != 2 || p.Job.Verified != 2 || a.service.backend != owner {
		t.Fatal("save failure rolled memory back or lost shared job", p)
	}
	for _, name := range []string{"a", "b"} {
		record, ok := a.regionRecordLocked(name)
		if !ok || record.Country != "US" {
			t.Fatal("per node result lost")
		}
	}
}
func regionAuthorityReadFailureGolden(t *testing.T, factory authorityFactory) {
	a, calls, _ := migrationHealthApp(t)
	regionAuthoritySeed(t, a, factory)
	a.service.backend = &regionAuthoritySpy{serviceStateBackend: a.service.backend, fail: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("patrol suppressed unavailable authority")
			}
		}()
		a.applyPatrolLocked("active", nil, time.Now())
	}()
	if len(*calls) != 0 {
		t.Fatal("authority failure triggered controller or REJECT", *calls)
	}
}
func TestServiceStateSafeRegionWorkflows(t *testing.T) {
	regionAuthorityProtectedGolden(t, newSelectedServiceStateBackend)
	regionAuthorityJobGolden(t, newSelectedServiceStateBackend, runSelectedRegionJobService)
	regionAuthorityReadFailureGolden(t, newSelectedServiceStateBackend)
}

type regionAuthorityRun func(regionServiceRequest, regionServiceEffects) regionServiceResult

func regionAuthorityReportGolden(t *testing.T, factory authorityFactory, run regionAuthorityRun) {
	a, fx, _ := regionFixture()
	regionAuthoritySeed(t, a, factory)
	owner := a.service.backend
	r := run(regionServiceRequest{Op: "report"}, fx)
	nodes := r.Data["nodes"].([]regionReportNode)
	if len(nodes) != 1 || nodes[0].Name != "a" {
		t.Fatal("report uses record.Name instead of key", nodes)
	}
	fx.save = func() error { return errors.New("save failure") }
	r = run(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 500 || a.controlLocked().RuntimeLockedRegion != "US" || a.settingsLocked().LockedRegion != "US" {
		t.Fatal("save failure rolled back region", r)
	}
	if a.service.backend != owner {
		t.Fatal("report/lock replaced owner")
	}
	if !a.mu.TryLock() {
		t.Fatal("region failed save leaked lock")
	}
	a.mu.Unlock()
}
func TestServiceStateSafeRegionReport(t *testing.T) {
	regionAuthorityReportGolden(t, newSelectedServiceStateBackend, runSelectedRegionService)
}
