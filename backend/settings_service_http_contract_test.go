package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// This tests the real decoder and invalid-JSON handler entry only. A valid
// request is never sent to app.updateSettings here (it can persist settings).
func TestMigrationSettingsHTTPDecodeContract(t *testing.T) {
	for _, body := range []string{`{"unknown":true}`, `{"socksPort":"0"}`, `{"strictPrivacy":1}`, `{"gameBypass":"x.exe"}`, `{`} {
		a := &app{}
		w := httptest.NewRecorder()
		a.updateSettings(w, httptest.NewRequest("POST", "/api/settings", strings.NewReader(body)))
		if w.Code != 400 || w.Body.String() != "invalid JSON\n" {
			t.Fatalf("%q: status=%d body=%q", body, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{}`, `null`, `{"importPolicy":null,"socksPort":null,"gameBypass":null,"subscriptionUrl":null}`} {
		var p settingsPatch
		if err := decodeJSON(strings.NewReader(body), &p); err != nil {
			t.Fatal(err)
		}
		if p.ImportPolicy != nil || p.SocksPort != nil || p.GameBypass != nil || p.SubscriptionURL != nil {
			t.Fatalf("absent/null must remain unspecified: %+v", p)
		}
	}
	var p settingsPatch
	if err := decodeJSON(strings.NewReader(`{"importPolicy":false,"socksPort":0,"gameBypass":[],"subscriptionUrl":"","strictPrivacy":false}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ImportPolicy == nil || *p.ImportPolicy || p.SocksPort == nil || *p.SocksPort != 0 || p.GameBypass == nil || len(*p.GameBypass) != 0 || p.SubscriptionURL == nil || *p.SubscriptionURL != "" || p.StrictPrivacy == nil || *p.StrictPrivacy {
		t.Fatalf("explicit zero values lost: %+v", p)
	}
	// Keep existing decoder semantics: duplicate keys use the last value and a
	// second JSON value after the first is not rejected by this single Decode.
	if err := decodeJSON(strings.NewReader(`{"socksPort":12,"socksPort":0} {"unknown":true}`), &p); err != nil || p.SocksPort == nil || *p.SocksPort != 0 {
		t.Fatalf("duplicate/trailing contract: %+v %v", p, err)
	}
}

func TestMigrationSettingsHTTPErrorPriority(t *testing.T) {
	tests := []struct {
		name, body, contains string
		code                 int
	}{
		{"import-before-port", `{"importPolicy":false,"socksPort":-1}`, "", 409},
		{"range-before-conflict", `{"socksPort":-1,"connectionMode":"invalid"}`, "", 400},
		{"api-conflict-before-mode", `{"socksPort":48991,"connectionMode":"invalid"}`, "", 409},
		{"mode-before-wintun", `{"connectionMode":"invalid","wintunPath":"bad.txt"}`, "connection mode must be one this platform offers", 400},
		{"wintun-before-url", `{"wintunPath":"bad.txt","subscriptionUrl":"ftp://bad"}`, "wintun path must point to a .dll file", 400},
		{"url-before-mihomo", `{"subscriptionUrl":"ftp://bad","mihomoPath":"bad.txt"}`, "subscription URL must be an HTTP(S) URL", 400},
		{"mihomo-before-running", `{"mihomoPath":"bad.txt"}`, "Mihomo path must point to an .exe file", 400},
		{"running-after-validation", `{}`, "disconnect before changing settings", 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A permanently-running memory-only sentinel guarantees no successful
			// request can reach persistence, even if validation changes accidentally.
			k := &settingsContractRunningKernel{}
			a := testControlApp(&app{kernel: k, apiPort: 48991}, func(a *app) {
				testSetSettings(a, settings{SubscriptionURL: "https://old.example/sub", LockedRegion: "old", LockedNode: "old-node"})
				a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
				a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "live-node"})
			})
			before := a.settingsLocked()
			w := httptest.NewRecorder()
			a.updateSettings(w, httptest.NewRequest("POST", "/api/settings", strings.NewReader(tt.body)))
			if w.Code != tt.code || (tt.contains != "" && w.Body.String() != tt.contains+"\n") {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			if a.settingsLocked().SubscriptionURL != before.SubscriptionURL || a.settingsLocked().LockedRegion != before.LockedRegion || a.controlLocked().RuntimeLockedRegion != "JP" || a.controlLocked().RuntimeLockedNode != "live-node" {
				t.Fatal("failed validation changed application state")
			}
		})
	}
}

type settingsContractRunningKernel struct{}

func (*settingsContractRunningKernel) Running() bool { return true }
func (*settingsContractRunningKernel) Start() error  { panic("test must not start kernel") }
func (*settingsContractRunningKernel) Stop() error   { panic("test must not stop kernel") }
