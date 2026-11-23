package main

import (
	"errors"
	"testing"
)

type catalogRegistryRun func(*subscriptionServiceState, subscriptionServiceRequest, subscriptionServiceEffects) subscriptionServiceResult
type catalogQuotaRun func(quotaServiceRequest, quotaServiceEffects) quotaServiceResult

func catalogRegistryGolden(t *testing.T, factory authorityFactory, run catalogRegistryRun) {
	a := regionAuthorityNew(t, factory)
	owner := a.service.backend
	original := &subscriptionUserInfo{Total: 5}
	testCatalogWrite(a, []subscriptionEntry{{ID: "dup", URL: "one", Label: "first", UserInfo: original}, {ID: "dup", URL: "two", Label: "second", UserInfo: original}})
	call := func(q subscriptionServiceRequest, fx *subscriptionMemoryEffects) subscriptionServiceResult {
		s := settingsStateFromApp(a)
		s.StoreAvailable = true
		r := run(&s, prepareSubscriptionServiceRequest(q), fx)
		applySettingsServiceState(a, s)
		if a.service.backend != owner {
			t.Fatal("planner changed owner")
		}
		return r
	}
	info := &subscriptionUserInfo{Total: 1 << 60}
	r := call(subscriptionServiceRequest{Op: "record-use", ID: "dup", Info: info, NodeCount: 4}, &subscriptionMemoryEffects{Tick: 1 << 40})
	got := testCatalogRead(a)
	native := a.service.quotaStore != nil
	expected := *info
	matches := func(p *subscriptionUserInfo) bool {
		if native {
			return p != nil && p != info && *p == expected
		}
		return p == info
	}
	if r.Status != 200 || !matches(got[0].UserInfo) || !matches(got[1].UserInfo) || got[0].NodeCount != 4 || got[0].QuotaUpdatedAt == got[1].QuotaUpdatedAt {
		t.Fatal("record all sharing/time order", got)
	}
	info.Download = 77
	if !matches(testCatalogRead(a)[1].UserInfo) || (!native && testCatalogRead(a)[1].UserInfo.Download != 77) {
		t.Fatal("record-use pointer detached")
	}
	found := call(subscriptionServiceRequest{Op: "find", ID: "dup"}, &subscriptionMemoryEffects{})
	if !found.Found || found.Entry.URL != "one" || !matches(found.Entry.UserInfo) {
		t.Fatal("find identity/first")
	}
	listed := call(subscriptionServiceRequest{Op: "list"}, &subscriptionMemoryEffects{})
	if len(listed.Entries) != 2 || !matches(listed.Entries[0].UserInfo) || !matches(listed.Entries[1].UserInfo) {
		t.Fatal("list pointer sharing")
	}
	found.Entry.UserInfo.Upload = 88
	if !matches(testCatalogRead(a)[1].UserInfo) || (!native && testCatalogRead(a)[1].UserInfo.Upload != 88) {
		t.Fatal("find write-through")
	}
	r = call(subscriptionServiceRequest{Op: "rename", ID: "dup", Label: "renamed"}, &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}})
	if r.Status != 500 || testCatalogRead(a)[0].Label != "renamed" || testCatalogRead(a)[1].Label != "renamed" {
		t.Fatal("rename failure rolled back")
	}
	r = call(subscriptionServiceRequest{Op: "update", ID: "dup", Label: "first-only"}, &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}})
	if r.Status != 500 || testCatalogRead(a)[0].Label != "first-only" || testCatalogRead(a)[1].Label != "renamed" {
		t.Fatal("update first/savefail")
	}
	r = call(subscriptionServiceRequest{Op: "delete", ID: "dup"}, &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}})
	got = testCatalogRead(a)
	if r.Status != 500 || len(got) != 2 || !matches(got[0].UserInfo) || !matches(got[1].UserInfo) {
		t.Fatal("delete rollback resource")
	}
	testCatalogWrite(a, nil)
	r = call(subscriptionServiceRequest{Op: "add", URL: "https://added.invalid/sub"}, &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}})
	if r.Status != 500 || testCatalogRead(a) == nil || len(testCatalogRead(a)) != 0 {
		t.Fatal("failed add truncate must retain nonnil empty")
	}
	loaded := []subscriptionLoadedEntry{{Entry: subscriptionEntry{URL: ""}}, {Entry: subscriptionEntry{URL: "one"}}, {Entry: subscriptionEntry{ID: "explicit", URL: "two", Label: "kept", Enabled: false}, HasEnabled: true}}
	r = call(subscriptionServiceRequest{Op: "load"}, &subscriptionMemoryEffects{Read: loaded})
	got = testCatalogRead(a)
	if r.Status != 200 || len(got) != 2 || !got[0].Enabled || got[1].Enabled || got[1].ID != "explicit" {
		t.Fatal("load filtering/enabled presence", got)
	}
}
func catalogQuotaGolden(t *testing.T, factory authorityFactory, run catalogQuotaRun) {
	for _, mode := range []string{"rename", "aba", "url"} {
		t.Run(mode, func(t *testing.T) {
			a := regionAuthorityNew(t, factory)
			owner := a.service.backend
			old := &subscriptionUserInfo{Total: 5}
			testCatalogWrite(a, []subscriptionEntry{{ID: "id", URL: "https://synthetic.invalid/sub", Label: "before", UserInfo: old, QuotaUpdatedAt: 7}})
			incoming := &subscriptionUserInfo{Total: 99}
			fx := quotaTestEffects(a)
			saves := 0
			fx.fetch = func(string, string) (*subscriptionUserInfo, error) {
				a.mu.Lock()
				defer a.mu.Unlock()
				switch mode {
				case "rename":
					a.patchCatalogEntryLocked(0, catalogLabel, subscriptionEntry{Label: "edited"})
				case "aba":
					testCatalogWrite(a, []subscriptionEntry{{ID: "id", URL: "https://synthetic.invalid/sub", Label: "reborn"}})
				case "url":
					a.patchCatalogEntryLocked(0, catalogURL, subscriptionEntry{URL: "https://changed.invalid"})
				}
				return incoming, nil
			}
			fx.save = func() error { saves++; return errors.New("synthetic save") }
			r := run(quotaServiceRequest{Op: "refresh", ID: "id"}, fx)
			got := testCatalogRead(a)
			if mode == "url" {
				if r.Status != 409 || saves != 0 || !catalogQuotaMatches(a.service, got[0].UserInfo, old, *old) {
					t.Fatal("URL guard", r)
				}
			} else {
				if r.Status != 500 || saves != 1 || got[0].UserInfo == incoming || got[0].UserInfo.Total != 99 {
					t.Fatal("quota clone/savefail", r)
				}
				incoming.Total = 0
				if got[0].UserInfo.Total != 99 {
					t.Fatal("quota aliases incoming")
				}
			}
			if mode != "url" {
				next := quotaTestEffects(a)
				response := run(quotaServiceRequest{Op: "refresh", ID: "id"}, next)
				live := testCatalogRead(a)[0].UserInfo
				if response.Status != 200 || response.Entry.UserInfo == nil || response.Entry.UserInfo == live {
					t.Fatal("quota response aliases live resource", response)
				}
				live.Total = 12345
				if response.Entry.UserInfo.Total != 99 {
					t.Fatal("quota response changed after live edit")
				}
			}
			if a.service.backend != owner {
				t.Fatal("quota owner changed")
			}
			quotaAssertUnlocked(t, a)
		})
	}
}
func TestServiceStateSafeCatalogRegistry(t *testing.T) {
	catalogRegistryGolden(t, newSelectedServiceStateBackend, runSelectedSubscriptionService)
}
func TestServiceStateSafeCatalogQuota(t *testing.T) {
	catalogQuotaGolden(t, newSelectedServiceStateBackend, runSelectedQuotaService)
}
