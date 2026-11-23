package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func lifeDiffGoldens(t *testing.T, run lifeDiffRun) {
	t.Helper()
	for i := 0; i < 12; i++ {
		s := lifeDiffBase()
		op := "load-settings"
		switch i {
		case 0:
			s.Settings = `{"subscriptionUrl":"sealed","socksPort":"bad","selectedNode":"b","strictPrivacy":true,"chain":{"hops":["a","b"]}}`
		case 1:
			s.Fail = "open-secret"
		case 2:
			s.HashMatch = false
		case 3:
			s.Portable = true
			op = "new"
		case 4:
			s.BundleExists = true
			op = "bundle"
		case 5:
			s.Other = true
			op = "prepare"
		case 6:
			s.Collision = true
			op = "serve"
		case 7:
			s.Collision = true
			s.Fail = "listen-2"
			op = "serve"
		case 8:
			s.Fail = "load-health"
			s.Android = true
			op = "new"
		case 9:
			s.ProxyOn = true
			s.Fail = "stop-kernel"
			op = "cleanup"
		case 10:
			op = "identify"
			s.Running = true
		case 11:
			op = "shutdown"
		}
		f := lifeDiffNew(s, lifecycleServiceRequest{Op: op})
		r := run(lifecycleServiceRequest{Op: op}, f)
		a := f.e.a
		kinds := []string{}
		for _, v := range f.trace {
			kinds = append(kinds, v.Effect.Kind)
		}
		has := func(k string) bool {
			for _, x := range kinds {
				if x == k {
					return true
				}
			}
			return false
		}
		switch i {
		case 0:
			if a.settingsLocked().SelectedNode != "b" || a.settingsLocked().SubscriptionURL != "plain" || a.settingsLocked().StrictPrivacy || !a.settingsLocked().WebRTCProtection || a.settings.Chain.Name == "" {
				t.Fatal("partial JSON discarded")
			}
			a.settings.Chain.Hops[0] = "mutated"
			if a.settings.Chains[a.settings.Chain.Name][0] != "mutated" {
				t.Fatal("legacy hop alias lost")
			}
		case 1:
			if a.settingsLocked().SubscriptionURL != "" || len(a.cachedNodes) != 2 {
				t.Fatal("secret failure clear must precede cache hash")
			}
		case 2:
			if len(a.cachedNodes) != 0 || !has("cache-remove") {
				t.Fatal("cache mismatch not invalidated")
			}
		case 3:
			if has("bundle-source") {
				t.Fatal("portable copied bundle")
			}
		case 4:
			if has("bundle-write") || has("bundle-read") {
				t.Fatal("existing bundle overwritten")
			}
		case 5:
			if has("proxy") || !has("china-refresh") {
				t.Fatal("other instance proxy disturbed")
			}
		case 6:
			if r.Port != 8002 || r.Server == nil || f.closed != 1 || f.served != 1 || f.e.serverOwned {
				t.Fatal("server handoff")
			}
			pos := []int{}
			for j, v := range f.trace {
				if v.Effect.Kind == "listen" || v.Effect.Kind == "listener-close" {
					pos = append(pos, j)
				}
			}
			if len(pos) != 3 || f.trace[pos[1]].Effect.Kind != "listen" {
				t.Fatal("reserved listener closed too early")
			}
		case 7:
			if r.Err == nil || f.closed != 1 || f.served != 0 {
				t.Fatal("listen failure cleanup")
			}
		case 8:
			if a.healthDomainLocked().Params.ConnectTimeoutMs != 6000 || a.healthDomainLocked().Params.FailureThreshold != 3 || len(a.healthDomainLocked().Health) != 0 || len(a.healthDomainLocked().Regions) != 1 || !has("source-labels") {
				t.Fatal("independent store loads or android defaults")
			}
		case 9:
			if a.proxyOn || !has("proxy") {
				t.Fatal("stop error prevented proxy cleanup")
			}
		case 10:
			if len(f.trace) < 4 || f.trace[len(f.trace)-1].Locked {
				t.Fatal("identify response held lock")
			}
		case 11:
			if !reflect.DeepEqual(f.extra, []string{"disconnect-response", "signal"}) {
				t.Fatal("shutdown response order")
			}
		}
		if f.e.locked {
			t.Fatal("lock leaked")
		}
	}
}
func TestMigrationLifecycleSafeGoldens(t *testing.T) { lifeDiffGoldens(t, runSelectedLifecycleService) }
func TestMigrationLifecycleSafeShutdownChannelOrder(t *testing.T) {
	q := lifecycleServiceRequest{Op: "shutdown"}
	f := lifeDiffNew(lifeDiffBase(), q)
	ch := make(chan struct{})
	entered := make(chan struct{})
	done := make(chan struct{})
	f.e.signal = func() { close(entered); ch <- struct{}{} }
	go func() { defer close(done); runSelectedLifecycleService(q, f) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("signal not reached")
	}
	if f.w.Body.Len() == 0 {
		t.Fatal("signal before response")
	}
	select {
	case <-done:
		t.Fatal("unbuffered signal unexpectedly nonblocking")
	default:
	}
	<-ch
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown stuck")
	}
}

// This oracle is copied from the pre-migration loadSettings rules, not from the new service flow.
func lifeOriginalSettings(raw string, readError, secretError bool) settingsDocument {
	s := settingsDocument{settings: settings{SocksPort: 1080}}
	if !readError {
		_ = json.Unmarshal([]byte(raw), &s)
		if s.Chains == nil && len(s.Chain.Hops) >= 2 {
			s.Chain.Name = lifecycleLegacyChainName()
			s.Chains = map[string][]string{s.Chain.Name: s.Chain.Hops}
		}
		if s.StrictPrivacy {
			s.WebRTCProtection, s.DNSProtection = true, true
			s.StrictPrivacy = false
		}
	}
	if s.SubscriptionURL != "" {
		s.SubscriptionURL = "plain"
		if secretError {
			s.SubscriptionURL = ""
		}
	}
	if s.SelectionMode == "" {
		s.SelectionMode = "auto"
	}
	if s.PathChecks.IntervalSec == 0 && len(s.PathChecks.Targets) == 0 {
		s.PathChecks = defaultPathChecks()
	}
	return s
}
func lifeSettingsGolden(t *testing.T, run lifeDiffRun) {
	for _, raw := range []string{`{}`, `null`, `{"subscriptionUrl":"cipher","socksPort":"bad","selectedNode":"b"}`, `{"subscriptionUrl":"cipher","chain":{"hops":["a","b"]},"strictPrivacy":true}`, `{"chain":{"hops":["a","b"]},"chains":{}}`, `{"pathChecks":{"intervalSec":0,"targets":[{"url":"synthetic"}]},"selectionMode":"manual"}`, `{"subscriptionUrl":"cipher",`} {
		for _, fail := range []string{"", "read-settings", "open-secret"} {
			s := lifeDiffBase()
			s.Settings = raw
			s.Fail = fail
			f := lifeDiffNew(s, lifecycleServiceRequest{Op: "load-settings"})
			run(lifecycleServiceRequest{Op: "load-settings"}, f)
			want := lifeOriginalSettings(raw, fail == "read-settings", fail == "open-secret")
			if !reflect.DeepEqual(want, f.e.a.settingsDocumentLocked()) {
				t.Fatalf("original settings mismatch raw=%s fail=%s\nwant=%+v\ngot=%+v", raw, fail, want, f.e.a.settingsDocumentLocked())
			}
			for _, n := range f.e.a.cachedNodes {
				if n.Selected != (n.Name == want.SelectedNode) {
					t.Fatal("selected cache projection")
				}
			}
		}
	}
}
func TestMigrationLifecycleSafeOriginalSettings(t *testing.T) {
	lifeSettingsGolden(t, runSelectedLifecycleService)
}
