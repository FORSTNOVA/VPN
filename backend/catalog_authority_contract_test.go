package main

import (
	"reflect"
	"testing"
	"time"
)

func catalogAuthorityGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	other := regionAuthorityNew(t, factory)
	owner := a.service.backend
	now := time.Now()
	if e := a.service.baseline(trafficSample{at: now, up: 41, down: 43}); e != nil {
		t.Fatal(e)
	}
	a.replacePathConfigLocked(pathCheckSettings{Enabled: true, IntervalSec: 55})
	a.writeRegionRecordLocked("key", regionRecord{Name: "different", Country: "JP", VerifiedAt: now})
	a.patchControlLocked(controlSubscriptionURL, serviceControlState{SubscriptionURL: "active"})
	info := &subscriptionUserInfo{Upload: 1, Download: 2, Total: 1 << 60, Expire: 1 << 40}
	registered := *info
	matches := func(p *subscriptionUserInfo) bool { return catalogQuotaMatches(a.service, p, info, registered) }
	entries := []subscriptionEntry{{ID: "same", URL: "one", Label: "first", UserInfo: info}, {ID: "same", URL: "two", Label: "second", UserInfo: info}, {ID: "other", URL: "one"}}
	testCatalogWrite(a, entries)
	testCatalogCacheWrite(a, true)
	got := testCatalogRead(a)
	if len(got) != 3 || !matches(got[0].UserInfo) || !matches(got[1].UserInfo) {
		t.Fatal("ordered duplicates/pointer ownership")
	}
	info.Total = 99
	if !matches(testCatalogRead(a)[1].UserInfo) {
		t.Fatal("external pointer mutation lost")
	}
	got[0].UserInfo.Upload = 71
	if (a.service.quotaStore == nil && info.Upload != 71) || (a.service.quotaStore != nil && info.Upload != registered.Upload) {
		t.Fatal("returned shared pointer lost")
	}
	got[0].Label = "local-only"
	if testCatalogRead(a)[0].Label != "first" {
		t.Fatal("catalog value projection aliases")
	}
	if len(testCatalogRead(other)) != 0 || testCatalogCacheRead(other) {
		t.Fatal("cross instance")
	}
	m := authoritySnapshot(t, a.service)
	if m.Up != 41 || a.pathConfigLocked().IntervalSec != 55 || a.healthDomainLocked().Regions["key"].Country != "JP" || a.controlLocked().SubscriptionURL != "active" {
		t.Fatal("five-domain interference")
	}
	if a.service.backend != owner {
		t.Fatal("owner transferred")
	}
	if e := a.service.transferToGo(); e != nil {
		t.Fatal(e)
	}
	got = testCatalogRead(a)
	if !matches(got[0].UserInfo) || !matches(got[1].UserInfo) || !testCatalogCacheRead(a) {
		t.Fatal("checkpoint changed Go resource identity")
	}
	info.Expire = 17
	if !matches(testCatalogRead(a)[0].UserInfo) {
		t.Fatal("handoff detached resource")
	}
}
func catalogAuthorityShapeGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	for _, entries := range [][]subscriptionEntry{nil, {}, {{ID: "x", UserInfo: nil}}, {{ID: "x", UserInfo: &subscriptionUserInfo{}}}} {
		testCatalogWrite(a, entries)
		got := testCatalogRead(a)
		if !reflect.DeepEqual(got, entries) || (got == nil) != (entries == nil) {
			t.Fatal("shape mismatch", got, entries)
		}
		if len(entries) > 0 && entries[0].UserInfo != nil && !catalogQuotaMatches(a.service, got[0].UserInfo, entries[0].UserInfo, *entries[0].UserInfo) {
			t.Fatal("resource identity")
		}
	}
}
func TestServiceStateSafeCatalogDomains(t *testing.T) {
	catalogAuthorityGolden(t, newSelectedServiceStateBackend)
}
func TestServiceStateSafeCatalogShape(t *testing.T) {
	catalogAuthorityShapeGolden(t, newSelectedServiceStateBackend)
}
func catalogAuthorityBoundedGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	owner := a.service.backend
	testCatalogWrite(a, []subscriptionEntry{{ID: "one", UserInfo: &subscriptionUserInfo{Total: 1}}})
	for i := 0; i < 10020; i++ {
		info := &subscriptionUserInfo{Total: int64(i)}
		a.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: info})
		e, ok := a.catalogEntryLocked(0)
		if !ok || !catalogQuotaMatches(a.service, e.UserInfo, info, *info) {
			t.Fatalf("resource changed %d", i)
		}
		if len(a.service.infos) > 2 || len(a.service.infoReverse) > 2 {
			t.Fatalf("unbounded resources at %d: %d/%d", i, len(a.service.infos), len(a.service.infoReverse))
		}
	}
	if a.service.backend != owner {
		t.Fatal("owner changed")
	}
}
func TestServiceStateSafeCatalogBounded(t *testing.T) {
	catalogAuthorityBoundedGolden(t, newSelectedServiceStateBackend)
}
func catalogAuthorityStaleGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	owner := a.service.backend
	testCatalogWrite(a, []subscriptionEntry{{ID: "original"}})
	a.cachedNodes = []proxyNode{{Name: "old"}}
	stale := settingsStateFromApp(a)
	live := settingsStateFromApp(a)
	markCatalogAppend(&live, subscriptionEntry{ID: "new"})
	markCatalogFromCache(&live, true)
	nodes := make([]proxyNode, 1, 8)
	nodes[0].Name = "live"
	live.CachedNodes = nodes
	markSettingsCachedNodes(&live)
	applySettingsServiceState(a, live)
	stale.Subscriptions = nil
	stale.ProfileFromCache = false
	stale.CachedNodes = nil
	applySettingsServiceState(a, stale)
	newer := []proxyNode{{Name: "newer"}}
	a.cachedNodes = newer
	applySettingsServiceState(a, live)
	if len(testCatalogRead(a)) != 2 || !testCatalogCacheRead(a) || &a.cachedNodes[0] != &newer[0] || a.service.backend != owner {
		t.Fatal("stale or repeated intent restored catalog/cache")
	}
}
func TestServiceStateSafeCatalogStale(t *testing.T) {
	catalogAuthorityStaleGolden(t, newSelectedServiceStateBackend)
}
