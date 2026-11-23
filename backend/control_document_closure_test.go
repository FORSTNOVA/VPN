package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestControlDocumentPartialDecodeAndNilEmpty(t *testing.T) {
	for _, bypass := range [][]string{nil, {}, {"prior"}} {
		a := settingsDocumentTestApp(t, t.TempDir())
		a.patchControlLocked(controlGameBypass|controlSocksPort|controlRuntimeLockedNode, serviceControlState{GameBypass: bypass, SocksPort: 97, RuntimeLockedNode: "runtime"})
		a.settings.Chains = map[string][]string{"old": {"one"}}
		alias := a.settings.Chains
		want := a.settingsDocumentLocked()
		want.Chains = map[string][]string{"old": {"one"}}
		body := []byte(`{"socksPort":"bad","selectedNode":"decoded","gameBypass":[],"chains":{"new":["two"]},"pathChecks":{"intervalSec":71}}`)
		we := json.Unmarshal(body, &want)
		ge := a.decodeSettingsDocumentLocked(body)
		if we == nil || ge == nil || !reflect.DeepEqual(a.settingsDocumentLocked(), want) {
			t.Fatal("partial decode changed")
		}
		if a.controlLocked().RuntimeLockedNode != "runtime" {
			t.Fatal("disk changed runtime pins")
		}
		if !reflect.DeepEqual(alias["new"], []string{"two"}) {
			t.Fatal("map alias lost")
		}
		if a.settingsLocked().GameBypass == nil {
			t.Fatal("empty became nil")
		}
		if e := a.decodeSettingsDocumentLocked([]byte(`{"gameBypass":null}`)); e != nil {
			t.Fatal(e)
		}
		if a.settingsLocked().GameBypass != nil {
			t.Fatal("null became empty")
		}
	}
}
func TestControlDocumentStaleCatalogAndRepeatedCommit(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.patchControlLocked(controlSubscriptionURL|controlSelectedNode|controlRuntimeLockedNode, serviceControlState{SubscriptionURL: "old", SelectedNode: "old", RuntimeLockedNode: "old-pin"})
	s := settingsStateFromApp(a)
	s.Settings.MihomoPath = "synthetic.exe"
	s.Settings.Chain = proxyChain{Hops: []string{"stale"}}
	a.settings.Chain = proxyChain{Hops: []string{"new-live"}}
	a.patchControlLocked(controlSubscriptionURL|controlSelectedNode|controlAutoMergeHours|controlRuntimeLockedNode, serviceControlState{SubscriptionURL: "latest", SelectedNode: "latest", AutoMergeHours: 27, RuntimeLockedNode: "new-pin"})
	applySettingsServiceState(a, s)
	c := a.controlLocked()
	if c.SubscriptionURL != "latest" || c.SelectedNode != "latest" || c.AutoMergeHours != 27 || c.RuntimeLockedNode != "new-pin" || c.MihomoPath != "synthetic.exe" {
		t.Fatalf("stale write %#v", c)
	}
	if !reflect.DeepEqual(a.settings.Chain.Hops, []string{"new-live"}) {
		t.Fatal("stale chain restored")
	}
	a.patchControlLocked(controlMihomoPath, serviceControlState{MihomoPath: "newer.exe"})
	applySettingsServiceState(a, s)
	if a.controlLocked().MihomoPath != "newer.exe" {
		t.Fatal("replayed")
	}
	s.Settings.SocksPort = 51
	applySettingsServiceState(a, s)
	if a.controlLocked().MihomoPath != "newer.exe" || a.controlLocked().SocksPort != 51 {
		t.Fatal("later patch lost")
	}
}
func TestControlDocumentSaveFailureKeepsMemory(t *testing.T) {
	home := t.TempDir()
	if e := os.Mkdir(filepath.Join(home, "settings.json"), 0700); e != nil {
		t.Fatal(e)
	}
	a := settingsDocumentTestApp(t, home)
	s := settingsStateFromApp(a)
	s.Settings.SocksPort = 1234
	if e := (appSettingsEffects{a}).SaveSettings(s); e == nil {
		t.Fatal("expected save failure")
	}
	if a.controlLocked().SocksPort != 1234 {
		t.Fatal("memory rolled back")
	}
	a.patchControlLocked(controlSocksPort, serviceControlState{SocksPort: 4321})
	_ = (appSettingsEffects{a}).SaveSettings(s)
	if a.controlLocked().SocksPort != 4321 {
		t.Fatal("failed save replayed")
	}
}
func TestControlDocumentSnapshotCannotAliasControl(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.patchControlLocked(controlGameBypass, serviceControlState{GameBypass: []string{"live"}})
	s := settingsStateFromApp(a)
	s.Settings.GameBypass[0] = "changed"
	if a.controlLocked().GameBypass[0] != "live" {
		t.Fatal("snapshot alias")
	}
	applySettingsServiceState(a, s)
	if a.controlLocked().GameBypass[0] != "changed" {
		t.Fatal("slice diff lost")
	}
	s.Settings.GameBypass[0] = "later"
	if a.controlLocked().GameBypass[0] != "changed" {
		t.Fatal("apply alias")
	}
}
func TestControlDocumentExplicitSameValueIntentConsumedOnce(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.patchControlLocked(controlSocksPort, serviceControlState{SocksPort: 73})
	s := settingsStateFromApp(a)
	port := 73
	markSettingsControlIntent(&s, settingsRequestControlMask(settingsPatch{SocksPort: &port}, s.Settings))
	a.patchControlLocked(controlSocksPort|controlAutoMergeHours, serviceControlState{SocksPort: 99, AutoMergeHours: 12})
	applySettingsServiceState(a, s)
	if c := a.controlLocked(); c.SocksPort != 73 || c.AutoMergeHours != 12 {
		t.Fatal("explicit same value intent lost")
	}
	a.patchControlLocked(controlSocksPort, serviceControlState{SocksPort: 81})
	applySettingsServiceState(a, s)
	if a.controlLocked().SocksPort != 81 {
		t.Fatal("explicit intent replayed")
	}
}

func TestControlDocumentClosedSettingsHTTPFailsBeforeEffects(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	if e := a.service.Close(); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	a.updateSettings(w, httptest.NewRequest("POST", "/settings", strings.NewReader(`{"socksPort":1234}`)))
	if w.Code != 500 {
		t.Fatalf("closed authority returned %d", w.Code)
	}
	if !a.mu.TryLock() {
		t.Fatal("HTTP failure leaked mutex")
	}
	a.mu.Unlock()
}

func TestControlDocumentClosedDiagnosticsBeforeNetwork(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	if e := a.service.Close(); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	a.subscriptionDiagnostics(w, httptest.NewRequest("GET", "/diagnostics", nil))
	if w.Code != 500 {
		t.Fatalf("closed authority returned %d", w.Code)
	}
	if !a.mu.TryLock() {
		t.Fatal("diagnostics leaked mutex")
	}
	a.mu.Unlock()
}
