package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type settingsMemoryEffects struct {
	Trace        []string
	Running      []bool
	Fail         map[string]bool
	SaveFailures []bool
	Saves        []settingsServiceState
	CacheError   bool
	CacheNodes   []proxyNode
}

func (f *settingsMemoryEffects) KernelRunning() bool {
	f.Trace = append(f.Trace, "running")
	if len(f.Running) == 0 {
		return false
	}
	v := f.Running[0]
	f.Running = f.Running[1:]
	return v
}
func (f *settingsMemoryEffects) result(op string) error {
	f.Trace = append(f.Trace, op)
	if f.Fail[op] {
		return errors.New("synthetic " + op)
	}
	return nil
}
func (f *settingsMemoryEffects) RemoveCachedNodes()  { f.Trace = append(f.Trace, "remove-nodes") }
func (f *settingsMemoryEffects) ClearHealth() error  { return f.result("clear-health") }
func (f *settingsMemoryEffects) ClearRegions() error { return f.result("clear-regions") }
func (f *settingsMemoryEffects) SaveSettings(s settingsServiceState) error {
	f.Saves = append(f.Saves, s)
	f.Trace = append(f.Trace, "save")
	n := len(f.Saves) - 1
	if n < len(f.SaveFailures) && f.SaveFailures[n] {
		return errors.New("synthetic save")
	}
	return nil
}
func (f *settingsMemoryEffects) UseCachedProfile(id string, s *settingsServiceState) error {
	f.Trace = append(f.Trace, "cache:"+id)
	s.CachedNodes = f.CacheNodes
	if f.CacheError {
		return errors.New("synthetic partial cache")
	}
	return nil
}
func (f *settingsMemoryEffects) RemoveProfile(s string) {
	f.Trace = append(f.Trace, "remove-profile:"+s)
}
func (f *settingsMemoryEffects) SaveSubscriptions(s settingsServiceState) error {
	return f.result("save-subscriptions")
}
func (f *settingsMemoryEffects) Logf(s string, v ...any) {
	f.Trace = append(f.Trace, "log:"+fmt.Sprintf(s, v...))
}
func settingsFixture() settingsServiceState {
	return settingsServiceState{Settings: settings{SubscriptionURL: "https://old.invalid/sub", SelectedNode: "old", SelectionMode: "manual", LockedNode: "stored", LockedRegion: "stored"}, LockedNode: "live", LockedRegion: "JP", Blocked: true, BlockReason: "old", Health: map[string]nodeHealth{"old": {Name: "old"}}, Regions: map[string]regionRecord{"old": {Name: "old"}}, CachedNodes: []proxyNode{{Name: "old"}}, ProfileFromCache: true, StoreAvailable: true}
}
func settingsBool(v bool) *bool       { return &v }
func settingsString(v string) *string { return &v }
func settingsInt(v int) *int          { return &v }
func TestSettingsServicePriority(t *testing.T) {
	s := settingsFixture()
	fx := &settingsMemoryEffects{Running: []bool{true}}
	p := settingsPatch{ImportPolicy: settingsBool(true), SocksPort: settingsInt(-1)}
	r := runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
	if r.Status != 409 || len(fx.Trace) != 1 || s.Settings.ImportPolicy {
		t.Fatal("early running priority", r, fx.Trace)
	}
	s = settingsFixture()
	fx = &settingsMemoryEffects{Running: []bool{true}}
	p = settingsPatch{SocksPort: settingsInt(-1)}
	r = runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
	if r.Status != 400 || len(fx.Trace) != 0 {
		t.Fatal("port before final running", r, fx.Trace)
	}
	s = settingsFixture()
	fx = &settingsMemoryEffects{Running: []bool{false, true}}
	p = settingsPatch{ImportPolicy: settingsBool(true)}
	r = runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
	if r.Status != 409 || s.Settings.ImportPolicy || len(fx.Trace) != 2 {
		t.Fatal("two distinct running checks", r, fx.Trace)
	}
}
func TestSettingsServiceProtectionPrecedence(t *testing.T) {
	for _, initial := range []bool{false, true} {
		s := settingsFixture()
		s.Settings.DomesticWeb = initial
		fx := &settingsMemoryEffects{}
		p := settingsPatch{StrictPrivacy: settingsBool(false), DomesticWeb: settingsBool(true), DNSProtection: settingsBool(false)}
		r := runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
		if r.Status != 200 || s.Settings.DNSProtection || s.Settings.WebRTCProtection != (!initial) || s.Settings.LockedNode != "live" || s.Settings.LockedRegion != "JP" {
			t.Fatal("privacy/pin precedence", r, s)
		}
	}
}
func TestSettingsServiceOrderedClearAndFailure(t *testing.T) {
	s := settingsFixture()
	fx := &settingsMemoryEffects{Fail: map[string]bool{"clear-health": true, "clear-regions": true, "save-subscriptions": true}, SaveFailures: []bool{true, true}}
	p := settingsPatch{SubscriptionURL: settingsString(" https://new.invalid/sub ")}
	r := runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
	var trace []string
	for _, v := range fx.Trace {
		if !strings.HasPrefix(v, "log:") {
			trace = append(trace, v)
		}
	}
	want := []string{"running", "remove-nodes", "clear-health", "clear-regions", "save", "remove-profile:", "remove-profile:.source", "remove-profile:.format", "save-subscriptions", "save"}
	if !reflect.DeepEqual(trace, want) || r.Status != 500 {
		t.Fatal("order", trace, r)
	}
	if s.Settings.SubscriptionURL != "https://new.invalid/sub" || s.Settings.SelectedNode != "" || s.Settings.SelectionMode != "auto" || s.LockedRegion != "" || s.Blocked || len(s.Health) != 0 || len(s.Regions) != 0 || s.CachedNodes != nil || len(s.Subscriptions) != 1 {
		t.Fatal("state rolled back/uncleared", s)
	}
	if len(fx.Saves) != 2 || !fx.Saves[0].ProfileFromCache || fx.Saves[1].ProfileFromCache {
		t.Fatal("profile flag ordering")
	}
}
func TestSettingsServiceCachePartialAndMerged(t *testing.T) {
	for _, failure := range []bool{false, true} {
		s := settingsFixture()
		url := "https://cached.invalid/sub"
		s.Subscriptions = []subscriptionEntry{{ID: "cache-id", URL: url}}
		fx := &settingsMemoryEffects{CacheError: failure, CacheNodes: []proxyNode{{Name: "partial-new"}}}
		p := settingsPatch{SubscriptionURL: &url}
		r := runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
		if r.Status != 200 || len(s.CachedNodes) != 1 || s.CachedNodes[0].Name != "partial-new" || s.ProfileFromCache == failure {
			t.Fatal("partial cache lost", r, s)
		}
		trace := strings.Join(fx.Trace, "|")
		if strings.Contains(trace, "remove-profile:") != failure || strings.Contains(trace, "save-subscriptions") {
			t.Fatal("cache effects", trace)
		}
	}
	s := settingsFixture()
	fx := &settingsMemoryEffects{}
	p := settingsPatch{SubscriptionURL: settingsString(mergedSubscriptionURL)}
	r := runGoSettingsService(&s, prepareSettingsServiceRequest(p, true, false, 123, true), fx)
	if r.Status != 200 || s.Settings.SelectedNode != "old" || s.LockedNode != "live" || !reflect.DeepEqual(fx.Trace, []string{"running", "save"}) {
		t.Fatal("merged must not clear", r, fx.Trace)
	}
}
