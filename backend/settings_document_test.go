package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func settingsDocumentTestApp(t *testing.T, home string) *app {
	t.Helper()
	b, err := newSelectedServiceStateBackend()
	if err != nil {
		t.Fatal(err)
	}
	if expected, ok := map[string]string{"1": "*main.rustServiceStateBackend", "0": "*main.goServiceStateBackend"}[os.Getenv("SMARTVPN_TEST_EXPECT_DOCUMENT_RUST")]; ok && reflect.TypeOf(b).String() != expected {
		t.Fatalf("expected authority %s, got %T", expected, b)
	}
	a := &app{home: home, service: newServiceAuthority(b)}
	t.Cleanup(func() { _ = a.service.Close() })
	return a
}
func TestSettingsDocumentLegacyShapeAndPartialDecode(t *testing.T) {
	for _, targets := range [][]pathTarget{nil, {}, {{Label: "prior", URL: "https://synthetic.invalid", Enabled: true}}} {
		a := settingsDocumentTestApp(t, "")
		testEditSettings(a, func(s *settings) { s.SelectedNode = "existing" })
		testEditSettings(a, func(s *settings) { s.SocksPort = 99 })
		initial := pathCheckSettings{Enabled: true, IntervalSec: 71, Targets: targets}
		a.replacePathConfigLocked(initial)
		doc := a.settingsDocumentLocked()
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		// The old persisted shape had all settings fields and pathChecks at root.
		var root map[string]json.RawMessage
		if json.Unmarshal(b, &root) != nil {
			t.Fatal("bad projection")
		}
		if _, ok := root["settings"]; ok {
			t.Fatal("nested ordinary settings changed disk schema")
		}
		if _, ok := root["pathChecks"]; !ok {
			t.Fatal("path config omitted")
		}
		var diskPath pathCheckSettings
		if json.Unmarshal(root["pathChecks"], &diskPath) != nil || !reflect.DeepEqual(diskPath, initial) {
			t.Fatalf("nil/empty shape lost: %#v %#v", diskPath, initial)
		}
		want := settingsDocument{settings: a.settingsLocked(), PathChecks: initial}
		partial := []byte(`{"socksPort":"bad","selectedNode":"decoded","pathChecks":{"intervalSec":"bad","enabled":false,"targets":[{"label":"next","url":"https://next.invalid","enabled":true}]}}`)
		wantErr := json.Unmarshal(partial, &want)
		gotErr := a.decodeSettingsDocumentLocked(partial)
		if wantErr == nil || gotErr == nil || !reflect.DeepEqual(a.settingsLocked(), want.settings) || !reflect.DeepEqual(a.pathConfigLocked(), want.PathChecks) {
			t.Fatal("partial decoder semantics changed")
		}
	}
}
func TestSettingsDocumentStaleSettingsAndSubscriptionCannotOverwritePath(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid" })
	old := pathCheckSettings{IntervalSec: 60, Targets: []pathTarget{{Label: "old", URL: "https://old.invalid"}}}
	a.replacePathConfigLocked(old)
	stale := settingsStateFromApp(a)
	live := pathCheckSettings{Enabled: true, IntervalSec: 91, Targets: []pathTarget{{Label: "live", URL: "https://live.invalid", Enabled: true}}}
	a.replacePathConfigLocked(live)
	if err := (appSettingsEffects{a}).SaveSettings(stale); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.pathConfigLocked(), live) {
		t.Fatal("ordinary settings restored stale path")
	}
	stale.Settings.SelectedNode = "subscription-update"
	r := (appSubscriptionEffects{a}).Call("save-settings", "", &stale)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if !reflect.DeepEqual(a.pathConfigLocked(), live) {
		t.Fatal("subscription snapshot restored stale path")
	}
	b, err := os.ReadFile(filepath.Join(a.home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk settingsDocument
	if err = json.Unmarshal(b, &disk); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.PathChecks, live) || disk.SelectedNode != "subscription-update" {
		t.Fatal("save did not read latest authority")
	}
	entries, err := os.ReadDir(a.home)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatal("created alternate settings file", entries, err)
	}
}
func TestSettingsDocumentSaveFailureKeepsAuthority(t *testing.T) {
	a := settingsDocumentTestApp(t, filepath.Join(t.TempDir(), "nonexistent"))
	c := pathCheckSettings{Enabled: true, IntervalSec: 45, Targets: []pathTarget{}}
	a.replacePathConfigLocked(c)
	if err := a.saveSettings(); err == nil {
		t.Fatal("expected synthetic missing-directory error")
	}
	if !reflect.DeepEqual(a.pathConfigLocked(), c) {
		t.Fatal("save failure rolled back authority")
	}
}
func TestSettingsDocumentNoLivePathShadow(t *testing.T) {
	if _, ok := reflect.TypeOf(settings{}).FieldByName("PathChecks"); ok {
		t.Fatal("live settings retains path authority")
	}
	for _, name := range []string{"pathBusy", "pathStatus"} {
		if _, ok := reflect.TypeOf(app{}).FieldByName(name); ok {
			t.Fatal("app retains path shadow", name)
		}
	}
}

func TestSettingsDocumentProjectionCannotMutateAuthority(t *testing.T) {
	a := settingsDocumentTestApp(t, "")
	input := pathCheckSettings{Targets: []pathTarget{{Label: "owned", URL: "https://synthetic.invalid"}}}
	a.replacePathConfigLocked(input)
	input.Targets[0].Label = "caller-edited"
	doc := a.settingsDocumentLocked()
	if doc.PathChecks.Targets[0].Label != "owned" {
		t.Fatal("authority retained mutable input slice")
	}
	doc.PathChecks.Targets[0].Label = "projection-edited"
	if a.pathConfigLocked().Targets[0].Label != "owned" {
		t.Fatal("temporary settings projection mutated authority")
	}
}
