package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthSettingsStaleSnapshotCannotRestoreObservations(t *testing.T) {
	a := settingsDocumentTestApp(t, "")
	a.replaceHealthParamsLocked(defaultHealthParams())
	a.restoreHealthRecordsLocked(map[string]nodeHealth{"old": {Name: "old"}})
	s := settingsStateFromApp(a)
	a.restoreHealthRecordsLocked(map[string]nodeHealth{"fresh": {Name: "fresh"}})
	applySettingsServiceState(a, s)
	h := a.healthDomainLocked()
	if _, ok := h.Health["fresh"]; !ok || len(h.Health) != 1 {
		t.Fatal("stale snapshot overwrote domain", h.Health)
	}
}
func TestHealthSettingsClearIntentIsConsumedOnce(t *testing.T) {
	a := settingsDocumentTestApp(t, "")
	a.replaceHealthParamsLocked(defaultHealthParams())
	a.restoreHealthRecordsLocked(map[string]nodeHealth{"old": {Name: "old"}})
	a.restoreRegionRecordsLocked(map[string]regionRecord{"old": {Name: "old", Country: "US"}})
	s := settingsStateFromApp(a)
	s.ClearObservations = &settingsObservationClear{}
	applySettingsServiceState(a, s)
	h := a.healthDomainLocked()
	if h.Health == nil || h.Regions == nil || len(h.Health) != 0 || len(h.Regions) != 0 {
		t.Fatal("clear shape")
	}
	a.restoreHealthRecordsLocked(map[string]nodeHealth{"fresh": {Name: "fresh"}})
	applySettingsServiceState(a, s)
	if _, ok := a.healthDomainLocked().Health["fresh"]; !ok {
		t.Fatal("consumed intent cleared later observation")
	}
}
func TestHealthSettingsFailedSaveKeepsExplicitClear(t *testing.T) {
	a := settingsDocumentTestApp(t, filepath.Join(t.TempDir(), "missing"))
	a.replaceHealthParamsLocked(defaultHealthParams())
	a.restoreHealthRecordsLocked(map[string]nodeHealth{"old": {Name: "old"}})
	s := settingsStateFromApp(a)
	s.ClearObservations = &settingsObservationClear{}
	if err := (appSettingsEffects{a}).SaveSettings(s); err == nil {
		t.Fatal("expected fake directory error")
	}
	if len(a.healthDomainLocked().Health) != 0 {
		t.Fatal("save failure rolled back clear")
	}
}

func TestHealthSettingsHTTPClosedAuthorityFailsBeforeEffects(t *testing.T) {
	for _, name := range []string{"state", "params", "patrol", "region-report", "region-start"} {
		t.Run(name, func(t *testing.T) {
			a := settingsDocumentTestApp(t, "")
			if err := a.service.Close(); err != nil {
				t.Fatal(err)
			}
			var handler http.HandlerFunc
			switch name {
			case "state":
				handler = a.state
			case "params":
				handler = a.updateHealthParams
			case "patrol":
				handler = a.patrolNow
			case "region-report":
				handler = a.regionReport
			case "region-start":
				handler = a.startRegionVerification
			}
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest("POST", "/synthetic", strings.NewReader(`{}`)))
			if w.Code != 500 || strings.Contains(w.Body.String(), "connected") || strings.Contains(w.Body.String(), "success") {
				t.Fatal("authority failure presented as usable state", w.Code, w.Body.String())
			}
			if !a.mu.TryLock() {
				t.Fatal("handler leaked mutex")
			}
			a.mu.Unlock()
		})
	}
}
