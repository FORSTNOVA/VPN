package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogResourceStaleSnapshotsDoNotRestore(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.cachedNodes = []proxyNode{{Name: "old"}}
	stale := settingsStateFromApp(a)
	current := settingsStateFromApp(a)
	markCatalogAppend(&current, subscriptionEntry{ID: "live", URL: "synthetic:live"})
	markCatalogFromCache(&current, true)
	applySettingsServiceState(a, current)
	live := []proxyNode{{Name: "live"}}
	a.cachedNodes = live
	stale.Subscriptions = []subscriptionEntry{{ID: "stale"}}
	stale.ProfileFromCache = false
	stale.CachedNodes = []proxyNode{{Name: "stale"}}
	applySettingsServiceState(a, stale)
	if got := a.catalogEntriesLocked(); len(got) != 1 || got[0].ID != "live" {
		t.Fatal("catalog snapshot restored")
	}
	if !a.profileFromCacheLocked() {
		t.Fatal("from-cache snapshot restored")
	}
	if &a.cachedNodes[0] != &live[0] {
		t.Fatal("cache snapshot restored")
	}
}
func TestCatalogResourceCacheReplaceRetainsBackingAndCapacity(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	s := settingsStateFromApp(a)
	nodes := make([]proxyNode, 1, 7)
	nodes[0].Name = "first"
	s.CachedNodes = nodes
	markSettingsCachedNodes(&s)
	applySettingsServiceState(a, s)
	if &a.cachedNodes[0] != &nodes[0] || cap(a.cachedNodes) != 7 {
		t.Fatal("resource slice cloned")
	}
	nodes[0].SourceLabel = "external"
	if a.cachedNodes[0].SourceLabel != "external" {
		t.Fatal("write-through lost")
	}
	newest := []proxyNode{{Name: "newest"}}
	a.cachedNodes = newest
	applySettingsServiceState(a, s)
	if &a.cachedNodes[0] != &newest[0] {
		t.Fatal("replace intent replayed")
	}
}
func TestCatalogResourceCacheClearIsExplicitAndOnce(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.cachedNodes = []proxyNode{{Name: "old"}}
	s := settingsStateFromApp(a)
	s.CachedNodes = nil
	markSettingsCachedNodes(&s)
	applySettingsServiceState(a, s)
	if a.cachedNodes != nil {
		t.Fatal("clear lost")
	}
	a.cachedNodes = []proxyNode{{Name: "later"}}
	applySettingsServiceState(a, s)
	if len(a.cachedNodes) != 1 {
		t.Fatal("clear replayed")
	}
	s = settingsStateFromApp(a)
	s.CachedNodes = []proxyNode{}
	markSettingsCachedNodes(&s)
	applySettingsServiceState(a, s)
	if a.cachedNodes == nil || len(a.cachedNodes) != 0 {
		t.Fatal("empty resource became nil")
	}
}
func TestCatalogResourceFromCacheSameValueIntentOnce(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	s := settingsStateFromApp(a)
	markCatalogFromCache(&s, false)
	a.setProfileFromCacheLocked(true)
	applySettingsServiceState(a, s)
	if a.profileFromCacheLocked() {
		t.Fatal("explicit false lost")
	}
	a.setProfileFromCacheLocked(true)
	applySettingsServiceState(a, s)
	if !a.profileFromCacheLocked() {
		t.Fatal("from-cache intent replayed")
	}
}
func TestCatalogResourceAppendPreservesConcurrentEntriesAndQuota(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	s := settingsStateFromApp(a)
	info := &subscriptionUserInfo{}
	en := subscriptionEntry{ID: "duplicate", URL: "synthetic:one", UserInfo: info}
	markCatalogAppend(&s, en)
	other := settingsStateFromApp(a)
	markCatalogAppend(&other, subscriptionEntry{ID: "duplicate", URL: "synthetic:two", UserInfo: info})
	applySettingsServiceState(a, other)
	applySettingsServiceState(a, s)
	got := a.catalogEntriesLocked()
	if len(got) != 2 || got[0].URL != "synthetic:two" || got[1].URL != "synthetic:one" {
		t.Fatal("append lost order/duplicates")
	}
	for _, row := range got {
		if row.UserInfo == nil || *row.UserInfo != *info || (row.UserInfo == info) != (a.service.quotaStore == nil) {
			t.Fatal("appended quota ownership")
		}
	}
	applySettingsServiceState(a, s)
	if len(a.catalogEntriesLocked()) != 2 {
		t.Fatal("append replayed")
	}
}
func TestCatalogResourceSettingsSaveFailKeepsIntentNotSnapshot(t *testing.T) {
	home := t.TempDir()
	if e := os.Mkdir(filepath.Join(home, "settings.json"), 0700); e != nil {
		t.Fatal(e)
	}
	a := settingsDocumentTestApp(t, home)
	s := settingsStateFromApp(a)
	markCatalogFromCache(&s, true)
	if e := (appSettingsEffects{a}).SaveSettings(s); e == nil {
		t.Fatal("expected save failure")
	}
	if !a.profileFromCacheLocked() {
		t.Fatal("save failure rolled back intent")
	}
	a.setProfileFromCacheLocked(false)
	a.cachedNodes = []proxyNode{{Name: "new"}}
	_ = (appSettingsEffects{a}).SaveSettings(s)
	if a.profileFromCacheLocked() || len(a.cachedNodes) != 1 {
		t.Fatal("save retry replayed stale state")
	}
}
