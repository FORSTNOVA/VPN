package main

import (
	"errors"
	"reflect"
	"testing"
)

type subscriptionMemoryEffects struct {
	Trace        []string
	Fail         map[string]int
	Running      bool
	Read         []subscriptionLoadedEntry
	Writes       [][]subscriptionEntry
	CachePartial bool
	Tick         int64
}

func (f *subscriptionMemoryEffects) Call(kind, arg string, s *subscriptionServiceState) subscriptionEffectReply {
	f.Trace = append(f.Trace, kind+":"+arg)
	r := subscriptionEffectReply{}
	switch kind {
	case "describe-url":
		r.Entry = subscriptionEntry{ID: "hash:" + arg, Label: "label:" + arg}
	case "seal-secret":
		r.Text = "sealed:" + arg
	case "open-secret":
		r.Text = "opened:" + arg
	case "read-registry":
		r.Loaded = f.Read
	case "write-registry":
		f.Writes = append(f.Writes, append([]subscriptionEntry(nil), s.Subscriptions...))
	case "running":
		r.Bool = f.Running
	case "now":
		f.Tick++
		r.Timestamp = f.Tick
	case "use-cache", "fetch", "merge-caches", "merge":
		if f.CachePartial {
			s.CachedNodes = []proxyNode{{Name: "partial"}}
			s.Settings.SelectedNode = "partial-choice"
		}
		r.Count = 2
	}
	if f.Fail[kind] > 0 {
		f.Fail[kind]--
		r.Err = errors.New("failed:" + kind)
	}
	return r
}
func subscriptionTestState() subscriptionServiceState {
	return subscriptionServiceState{Settings: settings{SubscriptionURL: "https://old.test/", SelectedNode: "old", LockedNode: "old"}, StoreAvailable: true, Subscriptions: []subscriptionEntry{{ID: "old", URL: "https://old.test/", Label: "old", Enabled: true}}, CachedNodes: []proxyNode{{Name: "old"}}, ProfileFromCache: true, LockedNode: "old", Health: map[string]nodeHealth{}, Regions: map[string]regionRecord{}}
}
func TestSubscriptionServiceSaveAndLoad(t *testing.T) {
	s := subscriptionTestState()
	f := &subscriptionMemoryEffects{}
	r := runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "save"}, f)
	if r.Err != nil || s.Subscriptions[0].URL != "https://old.test/" || f.Writes[0][0].URL != "sealed:https://old.test/" {
		t.Fatal(r, s, f.Writes)
	}
	s.Settings.SubscriptionURL = ""
	f = &subscriptionMemoryEffects{Read: []subscriptionLoadedEntry{{Entry: subscriptionEntry{URL: ""}}, {Entry: subscriptionEntry{URL: "one"}}, {Entry: subscriptionEntry{ID: "x", URL: "two", Label: "x"}, HasEnabled: true}}}
	runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "load"}, f)
	if len(s.Subscriptions) != 2 || !s.Subscriptions[0].Enabled || s.Subscriptions[1].Enabled || s.Subscriptions[0].ID != "hash:opened:one" {
		t.Fatal(s.Subscriptions)
	}
	if f.Trace[len(f.Trace)-1] != "start-loop:" {
		t.Fatal(f.Trace)
	}
}
func TestSubscriptionServiceMutationRollback(t *testing.T) {
	s := subscriptionTestState()
	f := &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}}
	req := prepareSubscriptionServiceRequest(subscriptionServiceRequest{Op: "add", URL: "https://new.test/"})
	if r := runGoSubscriptionService(&s, req, f); r.Status != 500 || len(s.Subscriptions) != 1 {
		t.Fatal(r, s.Subscriptions)
	}
	s.Subscriptions = append(s.Subscriptions, s.Subscriptions[0])
	f = &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}}
	req = prepareSubscriptionServiceRequest(subscriptionServiceRequest{Op: "update", ID: " old ", Label: "changed", URL: "not-a-url"})
	if r := runGoSubscriptionService(&s, req, f); r.Status != 500 || s.Subscriptions[0].Label != "changed" || s.Subscriptions[1].Label != "old" || s.Subscriptions[0].URL != "https://old.test/" {
		t.Fatal(r, s.Subscriptions)
	}
	req = prepareSubscriptionServiceRequest(subscriptionServiceRequest{Op: "rename", ID: "old", Label: "both"})
	runGoSubscriptionService(&s, req, f)
	if s.Subscriptions[0].Label != "both" || s.Subscriptions[1].Label != "both" {
		t.Fatal(s.Subscriptions)
	}
}
func TestSubscriptionServiceRecordClocks(t *testing.T) {
	s := subscriptionTestState()
	s.Subscriptions = append(s.Subscriptions, s.Subscriptions[0])
	f := &subscriptionMemoryEffects{}
	runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "record-use", ID: "old", NodeCount: 4, Info: &subscriptionUserInfo{Total: 99}}, f)
	if s.Subscriptions[0].UpdatedAt != 1 || s.Subscriptions[0].QuotaUpdatedAt != 2 || s.Subscriptions[1].UpdatedAt != 3 || s.Subscriptions[1].QuotaUpdatedAt != 4 {
		t.Fatal(s.Subscriptions)
	}
	f.Trace = nil
	runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "record-error", ID: "missing"}, f)
	if len(f.Writes) != 2 {
		t.Fatal("no-match must still save", f.Trace)
	}
}
func TestSubscriptionServiceDeletePartialRollback(t *testing.T) {
	s := subscriptionTestState()
	f := &subscriptionMemoryEffects{Fail: map[string]int{"write-registry": 1}}
	r := runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "delete", ID: "old"}, f)
	if r.Status != 500 || len(s.Subscriptions) != 1 || s.Settings.SubscriptionURL != "" || s.CachedNodes != nil {
		t.Fatal(r, s)
	}
	want := []string{"running:", "remove-nodes:", "clear-health:", "clear-regions:", "save-settings:", "remove-config:", "remove-profile:", "remove-profile:.source", "remove-profile:.format", "write-registry:", "seal-secret:https://old.test/", "write-registry:"}
	if !reflect.DeepEqual(f.Trace, want) {
		t.Fatalf("%q", f.Trace)
	}
	s = subscriptionTestState()
	s.Settings.SubscriptionURL = mergedSubscriptionURL
	s.Subscriptions = append(s.Subscriptions, subscriptionEntry{ID: "other", Enabled: true})
	f = &subscriptionMemoryEffects{Fail: map[string]int{"merge-caches": 1}, CachePartial: true}
	r = runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "delete", ID: "old"}, f)
	if r.Status != 502 || len(s.Subscriptions) != 2 || s.CachedNodes[0].Name != "partial" {
		t.Fatal(r, s)
	}
}
func TestSubscriptionServiceActivatePartial(t *testing.T) {
	s := subscriptionTestState()
	f := &subscriptionMemoryEffects{Fail: map[string]int{"fetch": 1, "use-cache": 1}, CachePartial: true}
	r := runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "activate", ID: "old"}, f)
	if r.Status != 502 || r.Message != "failed:fetch" || s.Subscriptions[0].LastError != "failed:fetch" || s.CachedNodes[0].Name != "partial" || s.Settings.SelectedNode != "partial-choice" {
		t.Fatal(r, s)
	}
	s = subscriptionTestState()
	f = &subscriptionMemoryEffects{Fail: map[string]int{"fetch": 1}, CachePartial: true}
	r = runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "activate", ID: "old"}, f)
	if r.Status != 200 || !s.ProfileFromCache || s.Subscriptions[0].NodeCount != 1 {
		t.Fatal(r, s)
	}
}

func TestSubscriptionServiceWideTimestamp(t *testing.T) {
	s := subscriptionTestState()
	f := &subscriptionMemoryEffects{Tick: 1 << 40}
	runGoSubscriptionService(&s, subscriptionServiceRequest{Op: "record-use", ID: "old", Info: &subscriptionUserInfo{}}, f)
	if s.Subscriptions[0].UpdatedAt != (1<<40)+1 || s.Subscriptions[0].QuotaUpdatedAt != (1<<40)+2 {
		t.Fatalf("timestamps truncated: %+v", s.Subscriptions[0])
	}
}
