package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationManualSafeHTTP(t *testing.T) {
	for _, body := range []string{`{`, `{"uri":5}`, `{"uri":"bad","other":1}`, `{"uri":"bad","preview":true}`} {
		a := &app{}
		w := httptest.NewRecorder()
		a.addManualNode(w, httptest.NewRequest("POST", "/api/manual", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	a := &app{}
	w := httptest.NewRecorder()
	a.deleteManualNode(w, httptest.NewRequest("DELETE", "/api/manual?name=%20%20", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	a.addManualNode(w, httptest.NewRequest("POST", "/api/manual", strings.NewReader(`{"uri":"trojan://synthetic@test.invalid:443#original","name":" renamed ","preview":true}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["node"].(map[string]any)["name"] != "renamed" || len(a.manualNodes) != 0 {
		t.Fatal("preview mutated state/name")
	}
	if !a.mu.TryLock() {
		t.Fatal("HTTP leaked mutex")
	}
	a.mu.Unlock()
}
func TestMigrationManualSafeProviderTempDir(t *testing.T) {
	for _, scenario := range []string{"write", "remove-absent", "remove-file", "remove-error", "mkdir-error", "write-error", "rename-error"} {
		t.Run(scenario, func(t *testing.T) {
			a := &app{home: t.TempDir(), manualNodes: []manualNode{{Name: "synthetic", URI: "not-used", Proxy: map[string]any{"name": "synthetic", "type": "trojan", "server": "test.invalid", "port": 443, "password": "synthetic"}}}}
			path := a.manualPath()
			dir := filepath.Dir(path)
			switch scenario {
			case "remove-absent":
				a.manualNodes = nil
			case "remove-file":
				a.manualNodes = nil
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			case "remove-error":
				a.manualNodes = nil
				fallthrough
			case "rename-error":
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "child"), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			case "mkdir-error":
				if err := os.WriteFile(dir, []byte("not-directory"), 0600); err != nil {
					t.Fatal(err)
				}
			case "write-error":
				if err := os.MkdirAll(path+".tmp", 0700); err != nil {
					t.Fatal(err)
				}
			}
			a.mu.Lock()
			err := a.writeManualProviderLocked()
			a.mu.Unlock()
			fails := strings.HasSuffix(scenario, "error")
			if (err != nil) != fails {
				t.Fatalf("expectedFail=%t err=%v", fails, err)
			}
			if scenario == "write" {
				body, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(body), "synthetic") {
					t.Fatal("provider missing", err)
				}
				if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
					t.Fatal("success left tmp")
				}
			}
			if scenario == "rename-error" {
				if body, err := os.ReadFile(path + ".tmp"); err != nil || len(body) == 0 {
					t.Fatal("rename failure must leave temp", err)
				}
			}
			if scenario == "remove-file" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("empty provider was not removed")
				}
			}
		})
	}
}

// Exercise the production facade's actualNode materialization, not just planner
// results. URI credentials are synthetic; provider files stay inside TempDir.
func TestMigrationManualSafeFacadeIdentity(t *testing.T) {
	a := &app{home: t.TempDir()}
	a.mu.Lock()
	defer a.mu.Unlock()
	node, err := a.addManualNodeLocked("trojan://synthetic@test.invalid:443#first", "")
	if err != nil {
		t.Fatal(err)
	}
	node.Proxy["external"] = "visible"
	if a.manualNodes[0].Proxy["external"] != "visible" {
		t.Fatal("production add facade returned a cloned Proxy")
	}
	second := a.runManualService(manualServiceRequest{Op: "add", Borrowed: true, URI: "trojan://synthetic@test.invalid:443#second"})
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	second.Node.Proxy["second-external"] = "visible"
	if a.manualNodes[1].Proxy["second-external"] != "visible" {
		t.Fatal("runManualService omitted actualNode identity")
	}
	if err := a.removeManualNodeLocked("first"); err != nil {
		t.Fatal(err)
	}
	second.Node.Proxy["after-delete"] = "visible"
	if len(a.manualNodes) != 1 || a.manualNodes[0].Proxy["after-delete"] != "visible" {
		t.Fatal("production delete copied retained map")
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("production borrowed workflow unlocked caller")
	}
}
