package main

import (
	"reflect"
	"testing"
	"time"
)

func controlAuthorityGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	other := regionAuthorityNew(t, factory)
	owner := a.service.backend
	now := time.Now()
	if err := a.service.baseline(trafficSample{at: now, up: 17, down: 29}); err != nil {
		t.Fatal(err)
	}
	a.replacePathConfigLocked(pathCheckSettings{Enabled: true, IntervalSec: 47})
	a.pathClockCommandLocked(pathStateAt, now, "", 0)
	a.writeRegionRecordLocked("record-key", regionRecord{Name: "different", Country: "JP", Status: regionVerified, VerifiedAt: now, Sources: []string{"source"}})
	a.setHealthBlockedLocked(true)
	old := authoritySnapshot(t, a.service)
	a.patchControlLocked(controlAll, serviceControlState{SubscriptionURL: "synthetic", SelectedNode: "chosen", SelectionMode: "manual", LockedRegion: "JP", LockedNode: "disk-pin", RuntimeLockedRegion: "US", RuntimeLockedNode: "live-pin", GameBypass: []string{}, SocksPort: 1024})
	c := a.controlLocked()
	if c.LockedNode == c.RuntimeLockedNode || c.GameBypass == nil {
		t.Fatal("distinct pin or empty shape lost")
	}
	if other.controlLocked().SubscriptionURL != "" {
		t.Fatal("cross instance")
	}
	after := authoritySnapshot(t, a.service)
	if after.Revision <= old.Revision || after.Up != 17 || after.TimeToken != old.TimeToken || a.pathStatusLocked().At != now || !a.healthMetaLocked().Blocked {
		t.Fatal("four-domain interference")
	}
	saved := a.settingsLocked()
	saved.GameBypass = append(saved.GameBypass, "mutated")
	if len(a.controlLocked().GameBypass) != 0 {
		t.Fatal("projection alias")
	}
	a.patchControlLocked(controlGameBypass, serviceControlState{GameBypass: nil})
	if a.controlLocked().GameBypass != nil {
		t.Fatal("nil lost")
	}
	a.patchControlLocked(controlGameBypass, serviceControlState{GameBypass: []string{}})
	if a.service.backend != owner {
		t.Fatal("owner changed")
	}
	beforeCP := a.controlLocked()
	if err := a.service.transferToGo(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.controlLocked(), beforeCP) || a.pathStatusLocked().At != now || !a.healthMetaLocked().Blocked {
		t.Fatal("checkpoint omitted domain")
	}
}

func controlAuthorityStaleGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	owner := a.service.backend
	testSetSettings(a, settings{SubscriptionURL: "old", SelectedNode: "before", SelectionMode: "auto"})
	before := a.settingsLocked()
	next := before
	next.MihomoPath = "fake.exe"
	a.patchControlLocked(controlSelectedNode|controlSubscriptionURL|controlAutoMergeHours, serviceControlState{SelectedNode: "concurrent", SubscriptionURL: "latest", AutoMergeHours: 9})
	a.patchControlLocked(controlRuntimeLockedRegion|controlRuntimeLockedNode, serviceControlState{RuntimeLockedRegion: "JP", RuntimeLockedNode: "new"})
	a.applySettingsControlDiffLocked(before, next)
	c := a.controlLocked()
	if c.SelectedNode != "concurrent" || c.SubscriptionURL != "latest" || c.AutoMergeHours != 9 || c.MihomoPath != "fake.exe" || c.RuntimeLockedNode != "new" {
		t.Fatalf("stale snapshot applied: %+v", c)
	}
	// A second unchanged invocation must not replay the old selection or URL.
	a.applySettingsControlDiffLocked(before, next)
	if a.controlLocked().SelectedNode != "concurrent" || a.service.backend != owner {
		t.Fatal("replay/owner")
	}
}

func controlAuthorityPartialGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	hops := make([]string, 2, 5)
	copy(hops, []string{"a", "b"})
	profiles := map[string][]string{"legacy": hops}
	testSetSettings(a, settings{SubscriptionURL: "initial", SocksPort: 1234, Chain: proxyChain{Name: "legacy", Hops: hops}, Chains: profiles})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "live"})
	err := a.decodeSettingsDocumentLocked([]byte(`{"subscriptionUrl":"partial","socksPort":"bad","selectionMode":"manual","gameBypass":[]}`))
	if err == nil {
		t.Fatal("expected type error")
	}
	v := a.settingsLocked()
	if v.SubscriptionURL != "partial" || v.SocksPort != 1234 || v.SelectionMode != "manual" || v.GameBypass == nil || a.controlLocked().RuntimeLockedNode != "live" {
		t.Fatalf("partial result: %+v", v)
	}
	// The deliberately retained Go chain graph remains write-through.
	hops[0] = "external"
	profiles["new"] = hops
	if a.settings.Chain.Hops[0] != "external" || a.settings.Chains["new"][0] != "external" || cap(a.settings.Chain.Hops) != 5 {
		t.Fatal("Go resource graph alias changed")
	}
}

func TestServiceStateSafeControlDomains(t *testing.T) {
	controlAuthorityGolden(t, func() (serviceStateBackend, error) { return newSelectedServiceStateBackend() })
}
func TestServiceStateSafeControlStale(t *testing.T) {
	controlAuthorityStaleGolden(t, func() (serviceStateBackend, error) { return newSelectedServiceStateBackend() })
}
func TestServiceStateSafeControlPartial(t *testing.T) {
	controlAuthorityPartialGolden(t, func() (serviceStateBackend, error) { return newSelectedServiceStateBackend() })
}
func controlAuthorityCatalogGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	owner := a.service.backend
	testSetSettings(a, settings{SubscriptionURL: "old", SelectedNode: "old", AutoMergeHours: 3})
	snap := settingsStateFromApp(a)
	snap.Settings.AutoMergeHours = 6
	a.patchControlLocked(controlSubscriptionURL|controlSelectedNode|controlRuntimeLockedNode, serviceControlState{SubscriptionURL: "new", SelectedNode: "current", RuntimeLockedNode: "live"})
	chain := map[string][]string{"fresh": {"a", "b"}}
	a.settings.Chains = chain
	applySettingsServiceState(a, snap)
	a.patchControlLocked(controlAutoMergeHours, serviceControlState{AutoMergeHours: 12})
	applySettingsServiceState(a, snap)
	c := a.controlLocked()
	if c.SubscriptionURL != "new" || c.SelectedNode != "current" || c.RuntimeLockedNode != "live" || c.AutoMergeHours != 12 || reflect.ValueOf(a.settings.Chains).UnsafePointer() != reflect.ValueOf(chain).UnsafePointer() {
		t.Fatal("stale catalog snapshot or repeated effect clobbered unrelated state", c)
	}
	if a.service.backend != owner {
		t.Fatal("owner changed")
	}
}
func TestServiceStateSafeControlCatalog(t *testing.T) {
	controlAuthorityCatalogGolden(t, newSelectedServiceStateBackend)
}
