package main

import (
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func labelsAdapterFixture(t *testing.T) (*app, *appLabelsEffects, string) {
	t.Helper()
	a := &app{home: t.TempDir()}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = mergedSubscriptionURL })
	a.cachedNodes = []proxyNode{{Name: "n", SourceLabel: "old"}, {Name: "missing", SourceLabel: "old"}, {Name: "manual", SourceLabel: "keep", Manual: true}}
	e := newAppLabelsEffects(a)
	original := []byte("proxies:\n  - {name: n, type: http, server: example.invalid, port: 443}\ndns: null\ncustom: preserved\n")
	rebuilt := []byte("proxies:\n  - {name: n, type: http, server: example.invalid, port: 443}\nsmartvpn-source-labels: {n: restored}\nsmartvpn-policy-sections: []\nproxy-groups: []\ndns: {enable: true}\n")
	file := a.profilePath()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	e.cached = func() []SubscriptionSource { return nil }
	e.merge = func([]SubscriptionSource) ([]byte, []proxyNode, error) { return rebuilt, nil, nil }
	e.save = func([]proxyNode) error { return nil }
	return a, e, file
}
func TestLabelsServiceSinglePreservesBacking(t *testing.T) {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid" })
	a.cachedNodes = []proxyNode{{Name: "n"}, {Name: "existing", SourceLabel: "keep"}, {Name: "manual", Manual: true}}
	// The label resolver deliberately does not require Enabled.
	testCatalogEdit(a, func(entries *[]subscriptionEntry) {
		(*entries) = []subscriptionEntry{{URL: a.settingsLocked().SubscriptionURL, Label: "chosen", Enabled: false}}
	})
	e := newAppLabelsEffects(a)
	e.read = func(string) ([]byte, error) { t.Fatal("single source read profile"); return nil, nil }
	backing := a.cachedNodes
	runGoLabelsService(labelsServiceRequest{Op: "restore"}, e)
	if backing[0].SourceLabel != "chosen" || backing[1].SourceLabel != "keep" || backing[2].SourceLabel != "" {
		t.Fatal(backing)
	}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "" })
	backing[0].SourceLabel = ""
	runGoLabelsService(labelsServiceRequest{Op: "restore"}, e)
	if backing[0].SourceLabel != "" {
		t.Fatal("empty source changed labels")
	}
}
func TestLabelsServiceMergedTempFileAndAliases(t *testing.T) {
	a, e, file := labelsAdapterFixture(t)
	backing := a.cachedNodes
	saves := 0
	e.save = func(nodes []proxyNode) error {
		saves++
		if &nodes[0] != &backing[0] {
			t.Fatal("cache slice cloned")
		}
		return errors.New("ignored save")
	}
	r := runGoLabelsService(labelsServiceRequest{Op: "restore"}, e)
	if r.Err != nil || saves != 1 {
		t.Fatalf("save/result %d %v", saves, r.Err)
	}
	if backing[0].SourceLabel != "restored" || backing[1].SourceLabel != "" || backing[2].SourceLabel != "keep" {
		t.Fatal(backing)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = yaml.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["dns"]; !ok || v != nil {
		t.Fatal("existing nil DNS replaced", got)
	}
	if got["custom"] != "preserved" {
		t.Fatal(got)
	}
	if _, err = os.Stat(file + ".repair.tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary not renamed", err)
	}
}
func TestLabelsServiceRenameFailureRemovesTemp(t *testing.T) {
	a, e, file := labelsAdapterFixture(t)
	before, _ := os.ReadFile(file)
	removed := 0
	saves := 0
	e.rename = func(from, to string) error {
		if from != file+".repair.tmp" || to != file {
			t.Fatal("rename paths")
		}
		return errors.New("synthetic rename")
	}
	e.remove = func(p string) error { removed++; return os.Remove(p) }
	e.save = func([]proxyNode) error { saves++; return nil }
	runGoLabelsService(labelsServiceRequest{Op: "restore"}, e)
	after, _ := os.ReadFile(file)
	if removed != 1 || saves != 0 || !reflect.DeepEqual(before, after) || a.cachedNodes[0].SourceLabel != "old" {
		t.Fatal("rename failure mutated state")
	}
}
func TestLabelsServiceWriteFailureDoesNotCleanPartial(t *testing.T) {
	_, e, file := labelsAdapterFixture(t)
	e.write = func(p string, b []byte, m os.FileMode) error {
		if m != 0600 {
			t.Fatal(m)
		}
		if err := os.WriteFile(p, b, m); err != nil {
			t.Fatal(err)
		}
		return errors.New("partial write")
	}
	e.remove = func(string) error { t.Fatal("old behavior does not remove on write error"); return nil }
	runGoLabelsService(labelsServiceRequest{Op: "restore"}, e)
	if _, err := os.Stat(file + ".repair.tmp"); err != nil {
		t.Fatal("partial temp semantics changed", err)
	}
}
func TestLabelsServiceGraphMismatchSkipsRepair(t *testing.T) {
	a, e, _ := labelsAdapterFixture(t)
	e.merge = func([]SubscriptionSource) ([]byte, []proxyNode, error) {
		return []byte("proxies:\n  - {name: n, type: http, server: different.invalid, port: 443}\n"), nil, nil
	}
	e.write = func(string, []byte, os.FileMode) error { t.Fatal("changed active graph repaired"); return nil }
	runGoLabelsService(labelsServiceRequest{Op: "restore"}, e)
	if a.cachedNodes[0].SourceLabel != "old" {
		t.Fatal("changed labels on mismatch")
	}
}
func TestLabelsServicePatchKeepsGraphIdentity(t *testing.T) {
	e := newAppLabelsEffects(&app{})
	nested := map[string]any{"enabled": true}
	e.current = map[string]any{}
	e.meta = map[string]any{"dns": nested}
	r := e.Call(labelsEffect{Kind: "key-facts", Name: "dns"})
	if r.Bool || !r.Other {
		t.Fatal(r)
	}
	e.Call(labelsEffect{Kind: "patch-key", Name: "dns"})
	nested["enabled"] = false
	if e.current["dns"].(map[string]any)["enabled"] != false {
		t.Fatal("patch copied graph")
	}
}
