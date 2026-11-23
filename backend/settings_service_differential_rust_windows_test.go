//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This fixture is entirely in memory. Every observable effect captures the
// current use-case state, including state preceding ignored persistence errors.
type settingsDiffEvent struct {
	Kind  string
	Value any
	State settingsServiceState
}
type settingsDiffFX struct {
	state                                                               *settingsServiceState
	trace                                                               []settingsDiffEvent
	running                                                             []bool
	calls, saves                                                        int
	failHealth, failRegions, failSubscriptions, failCache, partialCache bool
	failSave                                                            int
}

func settingsDiffCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.New(v.Type().Elem())
		o.Elem().Set(settingsDiffCopy(v.Elem()))
		return o
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.New(v.Type()).Elem()
		o.Set(settingsDiffCopy(v.Elem()))
		return o
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			o.Index(i).Set(settingsDiffCopy(v.Index(i)))
		}
		return o
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			o.SetMapIndex(it.Key(), settingsDiffCopy(it.Value()))
		}
		return o
	case reflect.Struct:
		o := reflect.New(v.Type()).Elem()
		o.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if o.Field(i).CanSet() && v.Field(i).CanInterface() {
				o.Field(i).Set(settingsDiffCopy(v.Field(i)))
			}
		}
		return o
	default:
		return v
	}
}
func settingsDiffClone(s settingsServiceState) settingsServiceState {
	return settingsDiffCopy(reflect.ValueOf(s)).Interface().(settingsServiceState)
}
func (f *settingsDiffFX) emit(k string, v any) {
	f.trace = append(f.trace, settingsDiffEvent{k, v, settingsDiffClone(*f.state)})
}
func settingsDiffErr(fail bool) error {
	if fail {
		return errors.New("synthetic failure")
	}
	return nil
}
func (f *settingsDiffFX) KernelRunning() bool {
	v := false
	if len(f.running) > 0 {
		v = f.running[min(f.calls, len(f.running)-1)]
	}
	f.calls++
	f.emit("running", v)
	return v
}
func (f *settingsDiffFX) RemoveCachedNodes() { f.emit("remove-nodes", nil) }
func (f *settingsDiffFX) ClearHealth() error {
	f.emit("clear-health", nil)
	return settingsDiffErr(f.failHealth)
}
func (f *settingsDiffFX) ClearRegions() error {
	f.emit("clear-regions", nil)
	return settingsDiffErr(f.failRegions)
}
func (f *settingsDiffFX) SaveSettings(s settingsServiceState) error {
	f.saves++
	f.emit("save-settings", settingsDiffClone(s))
	return settingsDiffErr(f.failSave < 0 || f.failSave == f.saves)
}
func (f *settingsDiffFX) UseCachedProfile(id string, s *settingsServiceState) error {
	f.emit("cache", id)
	if !f.failCache || f.partialCache {
		s.CachedNodes = []proxyNode{{Name: "cached-node", SourceLabel: "cached-source"}}
	}
	return settingsDiffErr(f.failCache)
}
func (f *settingsDiffFX) RemoveProfile(suffix string) { f.emit("remove-profile", suffix) }
func (f *settingsDiffFX) SaveSubscriptions(s settingsServiceState) error {
	f.emit("save-subscriptions", settingsDiffClone(s))
	return settingsDiffErr(f.failSubscriptions)
}
func (f *settingsDiffFX) Logf(format string, a ...any) { f.emit("log", fmt.Sprintf(format, a...)) }
func settingsDiffBase() settingsServiceState {
	return settingsServiceState{
		Settings:     settings{SubscriptionURL: "https://old.example/sub", SocksPort: 7890, ConnectionMode: modeSystemProxy, SelectedNode: "old-node", SelectionMode: "manual", LockedRegion: "stale", LockedNode: "stale-node", StrictPrivacy: true, AutoMergeHours: 17, Chains: map[string][]string{"untouched": {"a", "b"}}, trafficPolicy: trafficPolicy{GameBypass: []string{"old.exe"}, DomesticWeb: true, WebRTCProtection: true, DNSProtection: true}},
		LockedRegion: "JP", LockedNode: "live-node", Blocked: true, BlockReason: "old reason", StoreAvailable: true, ProfileFromCache: true,
		Health:        map[string]nodeHealth{"old-node": {Name: "old-node", State: healthHealthy, ConsecutiveSuccesses: 9}},
		Regions:       map[string]regionRecord{"old-node": {Name: "old-node", Country: "JP", Status: regionVerified, VerifiedAt: time.Date(2026, 11, 16, 12, 0, 0, 123, time.UTC), Sources: []string{"first"}}},
		CachedNodes:   []proxyNode{{Name: "old-node", SourceLabel: "old"}},
		Subscriptions: []subscriptionEntry{{ID: subscriptionHash("https://cached.example/sub"), URL: "https://cached.example/sub", Label: "cached", Enabled: true}},
	}
}
func settingsDiffBool(v bool) *bool         { return &v }
func settingsDiffString(v string) *string   { return &v }
func settingsDiffInt(v int) *int            { return &v }
func settingsDiffList(v []string) *[]string { return &v }
func settingsDiffCompare(t *testing.T, initial settingsServiceState, p settingsPatch, script settingsDiffFX, windows, android bool) (settingsServiceState, settingsServiceResult, []settingsDiffEvent) {
	t.Helper()
	if !rustSettingsFlowAvailable() {
		t.Fatal("real Rust settings-flow DLL required")
	}
	modeOK := p.ConnectionMode == nil || connectionModeSupported(strings.TrimSpace(*p.ConnectionMode))
	req := prepareSettingsServiceRequest(p, windows, android, 48991, modeOK)
	g, r := settingsDiffClone(initial), settingsDiffClone(initial)
	gf, rf := script, script
	gf.state = &g
	rf.state = &r
	want := runGoSettingsService(&g, req, &gf)
	got, handled := runRustSettingsService(&r, req, &rf)
	if !handled {
		t.Fatal("Rust flow fell back/unavailable")
	}
	if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(g, r) || !reflect.DeepEqual(gf.trace, rf.trace) {
		t.Fatalf("settings parity mismatch\nrequest=%+v\nresult Go=%+v Rust=%+v\nstate Go=%#v\nstate Rust=%#v\ntrace Go=%#v\ntrace Rust=%#v", p, want, got, g, r, gf.trace, rf.trace)
	}
	return r, got, rf.trace
}

func TestMigrationSettingsFlowRandomParity(t *testing.T) {
	rng := rand.New(rand.NewSource(101234))
	pick := func(n int) int { return rng.Intn(n) }
	urls := []string{"", "https://old.example/sub", "https://new.example/sub", "https://cached.example/sub", mergedSubscriptionURL, "ftp://bad", " https://new.example/sub\n"}
	for i := 0; i < 2000; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := settingsDiffBase()
			s.StoreAvailable = pick(2) == 0
			s.Settings.DomesticWeb = pick(2) == 0
			s.Settings.WebRTCProtection = pick(2) == 0
			s.Settings.DNSProtection = pick(2) == 0
			s.ProfileFromCache = pick(2) == 0
			if pick(3) == 0 {
				s.Subscriptions = nil
			}
			if pick(4) == 0 {
				s.Health = nil
				s.Regions = nil
				s.CachedNodes = nil
			}
			if pick(4) == 0 {
				s.Settings.GameBypass = []string{}
			}
			p := settingsPatch{}
			bools := []**bool{&p.ImportPolicy, &p.DomesticWeb, &p.WebRTCProtection, &p.DNSProtection, &p.StrictPrivacy}
			for _, b := range bools {
				if pick(3) > 0 {
					*b = settingsDiffBool(pick(2) == 0)
				}
			}
			if pick(3) > 0 {
				p.SocksPort = settingsDiffInt([]int{-1, 0, 1, 7890, 48991, 65535, 65536}[pick(7)])
			}
			if pick(3) > 0 {
				p.SubscriptionURL = settingsDiffString(urls[pick(len(urls))])
			}
			if pick(3) > 0 {
				p.GameBypass = settingsDiffList([][]string{nil, {}, {""}, {"game.exe", "GAME.exe", " other.exe "}, {"not-executable"}, {"com.example.game"}}[pick(6)])
			}
			if pick(3) > 0 {
				p.ConnectionMode = settingsDiffString([]string{"", modeTUN, modeSystemProxy, "invalid", " " + modeSystemProxy + "\n"}[pick(5)])
			}
			if pick(3) > 0 {
				p.WintunPath = settingsDiffString([]string{"", " x.dll ", "bad.exe", "folder.dll/file", "C:\\x.DLL"}[pick(5)])
			}
			if pick(3) > 0 {
				p.MihomoPath = settingsDiffString([]string{"", " x.exe ", "bad.dll", "C:\\folder\\mihomo.EXE"}[pick(4)])
			}
			f := settingsDiffFX{running: []bool{pick(5) == 0, pick(4) == 0}, failHealth: pick(2) == 0, failRegions: pick(2) == 0, failSubscriptions: pick(2) == 0, failCache: pick(2) == 0, partialCache: pick(2) == 0, failSave: []int{-1, 0, 1, 2}[pick(4)]}
			settingsDiffCompare(t, s, p, f, pick(3) > 0, pick(4) == 0)
		})
	}
}

func TestMigrationSettingsFlowURLTransitions(t *testing.T) {
	for i := 0; i < 256; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := settingsDiffBase()
			s.StoreAvailable = i&1 != 0
			urls := []string{"https://new.example/sub", "https://cached.example/sub", "", mergedSubscriptionURL}
			address := urls[(i>>1)&3]
			f := settingsDiffFX{failHealth: i&8 != 0, failRegions: i&16 != 0, failCache: i&32 != 0, partialCache: true, failSubscriptions: i&64 != 0, failSave: 0}
			if i&128 != 0 {
				f.failSave = 2
			}
			out, res, trace := settingsDiffCompare(t, s, settingsPatch{SubscriptionURL: &address}, f, true, false)
			if address != mergedSubscriptionURL {
				if out.LockedNode != "" || out.LockedRegion != "" || out.Blocked || len(out.Health) != 0 || len(out.Regions) != 0 {
					t.Fatal("old node-bound state retained")
				}
				if out.Settings.SelectionMode != "auto" || out.Settings.SelectedNode != "" {
					t.Fatal("selection not reset")
				}
				saves := 0
				removed := []string{}
				for _, e := range trace {
					if e.Kind == "save-settings" {
						saves++
					}
					if e.Kind == "remove-profile" {
						removed = append(removed, e.Value.(string))
					}
				}
				if saves != 2 {
					t.Fatalf("URL change saves=%d, want inner+final", saves)
				}
				cached := address == "https://cached.example/sub"
				if cached && !f.failCache {
					if !out.ProfileFromCache || len(removed) != 0 {
						t.Fatal("successful cached profile removed")
					}
				} else if !reflect.DeepEqual(removed, []string{"", ".source", ".format"}) || out.ProfileFromCache {
					t.Fatalf("cache miss cleanup=%v cached=%v", removed, out.ProfileFromCache)
				}
				if cached && f.failCache && len(out.CachedNodes) != 1 {
					t.Fatal("cache failure must retain in-memory partial nodes")
				}
				if f.failSave == 2 && res.Status != 500 {
					t.Fatal("final save failure must be 500 without rollback")
				}
			} else if out.LockedNode != "live-node" || len(out.Health) != 1 || out.Settings.SelectionMode != "manual" {
				t.Fatal("merged URL must not clear node state")
			}
			if out.Settings.SubscriptionURL != address {
				t.Fatal("failed persistence rolled back committed URL")
			}
		})
	}
}

func TestMigrationSettingsFlowPrivacyOverrides(t *testing.T) {
	for i := 0; i < 64; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := settingsDiffBase()
			s.Settings.DomesticWeb = i&1 != 0
			strict, domestic, rtc, dns := i&2 != 0, i&4 != 0, i&8 != 0, i&16 != 0
			p := settingsPatch{StrictPrivacy: &strict, DomesticWeb: &domestic}
			wantRTC, wantDNS := strict, strict
			if domestic && !s.Settings.DomesticWeb {
				wantRTC, wantDNS = true, true
			}
			if i&32 != 0 {
				p.WebRTCProtection = &rtc
				p.DNSProtection = &dns
				wantRTC, wantDNS = rtc, dns
			}
			out, res, _ := settingsDiffCompare(t, s, p, settingsDiffFX{}, true, false)
			if res.Status != 200 || out.Settings.WebRTCProtection != wantRTC || out.Settings.DNSProtection != wantDNS {
				t.Fatal("strict/domestic/explicit override precedence")
			}
			if out.Settings.StrictPrivacy != s.Settings.StrictPrivacy {
				t.Fatal("legacy StrictPrivacy stored field must stay untouched")
			}
			if out.Settings.LockedNode != "live-node" || out.Settings.LockedRegion != "JP" {
				t.Fatal("in-memory lock must win stale stored lock")
			}
		})
	}
}

func TestMigrationSettingsFlowHashLookupIsNotEnsure(t *testing.T) {
	s := settingsDiffBase()
	url := "https://new.example/sub"
	s.Subscriptions = []subscriptionEntry{{ID: subscriptionHash(url), URL: "https://different.example/sub", Label: "collision fixture", Enabled: true}}
	out, res, trace := settingsDiffCompare(t, s, settingsPatch{SubscriptionURL: &url}, settingsDiffFX{failCache: true, partialCache: true, failSave: 2}, true, false)
	if res.Status != 500 || len(out.Subscriptions) != 2 || out.Subscriptions[1].URL != url || len(out.CachedNodes) != 1 || out.ProfileFromCache {
		t.Fatalf("hash hit / distinct URL ensure / partial cache lost: %#v %+v", out, res)
	}
	cache, ensure := 0, 0
	for _, e := range trace {
		if e.Kind == "cache" {
			cache++
			if e.Value != subscriptionHash(url) {
				t.Fatal("cache lookup must accept hash-ID")
			}
		}
		if e.Kind == "save-subscriptions" {
			ensure++
		}
	}
	if cache != 1 || ensure != 1 {
		t.Fatalf("cache=%d ensure=%d", cache, ensure)
	}
}

func TestMigrationSettingsFlowBytesAndSequence(t *testing.T) {
	for _, name := range []string{"simple", "节点🧪", "node\xff\x00"} {
		t.Run(fmt.Sprintf("%x", name), func(t *testing.T) {
			s := settingsDiffBase()
			s.LockedNode = name
			s.Settings.SelectedNode = name
			s.Settings.Chains = map[string][]string{name: {name}}
			s.Health = map[string]nodeHealth{name: {Name: name, State: healthHealthy}}
			s.Regions = map[string]regionRecord{name: {Name: name, Country: "JP", Sources: []string{name}}}
			s.Subscriptions[0].Label = name
			requests := []settingsPatch{
				{}, {GameBypass: settingsDiffList([]string{})}, {SocksPort: settingsDiffInt(0), ImportPolicy: settingsDiffBool(false)},
				{WintunPath: settingsDiffString(" " + name + ".dll "), MihomoPath: settingsDiffString(" " + name + ".exe ")},
				{SubscriptionURL: settingsDiffString("https://cached.example/sub")},
				{SubscriptionURL: settingsDiffString(mergedSubscriptionURL)},
				{SubscriptionURL: settingsDiffString("")},
				{StrictPrivacy: settingsDiffBool(false), DNSProtection: settingsDiffBool(true)},
			}
			for i, p := range requests {
				var res settingsServiceResult
				s, res, _ = settingsDiffCompare(t, s, p, settingsDiffFX{failCache: true, partialCache: true, failSave: 2}, true, false)
				if i != 4 && i != 6 && res.Status != 200 {
					t.Fatalf("sequence step %d unexpected result %+v", i, res)
				}
				if !reflect.DeepEqual(s.Settings.Chains, map[string][]string{name: {name}}) {
					t.Fatal("unrelated configuration bytes changed")
				}
			}
		})
	}
}
