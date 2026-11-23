package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

type migrationChainRunningKernel struct{}

func (*migrationChainRunningKernel) Running() bool { return true }
func (*migrationChainRunningKernel) Start() error  { panic("real start forbidden") }
func (*migrationChainRunningKernel) Stop() error   { panic("real stop forbidden") }
func TestMigrationChainSafeHTTPRejections(t *testing.T) {
	for _, body := range []string{`{`, `{"enabled":"wrong"}`, `{"unknown":1}`, `{"hops":["1","2","3","4","5","6","7","8","9"]}`} {
		a := &app{}
		w := httptest.NewRecorder()
		a.updateChain(w, httptest.NewRequest("PUT", "/api/chain", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("status=%d body=%s", w.Code, body)
		}
	}
	for _, kernel := range []bool{false, true} {
		a := &app{}
		if kernel {
			a.kernel = &migrationChainRunningKernel{}
		} else {
			testEditHealth(a, func(p *healthDomainProjection) { p.Job = &regionJob{Running: true} })
		}
		w := httptest.NewRecorder()
		a.updateChain(w, httptest.NewRequest("PUT", "/api/chain", strings.NewReader(`{"enabled":false}`)))
		if w.Code != 409 {
			t.Fatal("disabled update must still reject running job/kernel", w.Code)
		}
		if !a.mu.TryLock() {
			t.Fatal("update leaked lock")
		}
		a.mu.Unlock()
	}
	for _, body := range []string{`{`, `{"name":" "}`, `{"name":"` + strings.Repeat("界", 81) + `"}`, `{"name":"x","original":" old ","hops":[]}`} {
		a := &app{}
		a.settings.Chains = map[string][]string{"old": {"a", "b"}}
		w := httptest.NewRecorder()
		a.saveChainProfile(w, httptest.NewRequest("POST", "/api/chains", strings.NewReader(body)))
		want := 400
		if strings.Contains(body, "original") {
			want = 409
		}
		if w.Code != want {
			t.Fatalf("status=%d want=%d", w.Code, want)
		}
		if !a.mu.TryLock() {
			t.Fatal("save profile leaked lock")
		}
		a.mu.Unlock()
	}
}

func TestMigrationChainSafeCompileLegacyIdentity(t *testing.T) {
	nested := map[string]any{"path": "/old"}
	first := map[string]any{"name": "a", "type": "trojan", "server": "a.invalid", "nested": nested}
	second := map[string]any{"name": "b", "type": "trojan", "server": "b.invalid"}
	index := map[string][]map[string]any{"a": {first}, "b": {second}}
	result, err := compilePrefix([]string{"a", "b"}, index)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0]["name"] != "SmartVPNChainHop1" || result[1]["name"] != chainExit || result[1]["dialer-proxy"] != "SmartVPNChainHop1" {
		t.Fatal("compiled aliases changed")
	}
	if first["name"] != "a" || second["name"] != "b" || second["dialer-proxy"] != nil {
		t.Fatal("compile mutated original top-level map")
	}
	result[0]["new-top"] = "private"
	if first["new-top"] != nil {
		t.Fatal("top-level map not cloned")
	}
	result[0]["nested"].(map[string]any)["path"] = "/updated"
	if nested["path"] != "/updated" {
		t.Fatal("maps.Clone legacy shallow nested identity changed")
	}
	for _, field := range []string{"type", "server"} {
		p := map[string]any{"name": "a", "type": "trojan", "server": "a.invalid"}
		p[field] = 123
		if _, err := compilePrefix([]string{"a"}, map[string][]map[string]any{"a": {p}}); err == nil {
			t.Fatal("non-string required field accepted", field)
		}
	}
}
