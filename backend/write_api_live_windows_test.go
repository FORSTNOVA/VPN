package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveWriteAPI(t *testing.T) {
	mode, bin := os.Getenv("SMARTVPN_TEST_WRITE_API"), os.Getenv("SMARTVPN_TEST_MIHOMO")
	if mode == "" || bin == "" {
		t.Skip("requires selected core and local Mihomo")
	}
	a := storeApp(t)
	const token = "phase100-test-token"
	fallback := a.routes(token)
	h, stop, enabled := a.openHostAPI(token, fallback)
	defer stop()
	req := httptest.NewRequest("PUT", "/api/settings", nil)
	_, pattern := h.(*http.ServeMux).Handler(req)
	if (enabled && pattern == "PUT /api/settings") != (mode == "current") {
		t.Fatal("unexpected write dispatch", mode, enabled, pattern)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	call := func(verb, path, data, auth string, status int) []byte {
		t.Helper()
		req, err := http.NewRequest(verb, server.URL+"/api/"+path, strings.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s %v", verb, path, res.StatusCode, status, b, err)
		}
		return b
	}
	for _, r := range a.apiRoutes() {
		if r.method != "GET" {
			call(r.method, strings.TrimPrefix(r.path, "/api/"), "{}", "Bearer wrong", 401)
		}
	}
	api := func(verb, path, body string, status int) []byte {
		t.Helper()
		return call(verb, path, body, "Bearer "+token, status)
	}
	b, _ := json.Marshal(map[string]any{"connectionMode": modeSystemProxy, "mihomoPath": bin, "importPolicy": true})
	api("PUT", "settings", string(b), 200)
	settingsFile := filepath.Join(a.home, "settings.json")
	saved, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	api("PUT", "settings", `{"socksPort":-1}`, 400)
	invalidHome := func(fn func()) {
		t.Helper()
		a.mu.Lock()
		old := a.home
		a.home = filepath.Join(old, "missing")
		a.mu.Unlock()
		defer func() { a.mu.Lock(); a.home = old; a.mu.Unlock() }()
		fn()
	}
	invalidHome(func() {
		api("PUT", "settings", `{"importPolicy":false,"subscriptionUrl":"https://example.invalid/sub"}`, 500)
	})
	if !a.settingsLocked().ImportPolicy || a.settingsLocked().SubscriptionURL != "" || len(a.catalogEntriesLocked()) != 0 {
		t.Fatal("failed save published changes")
	}
	onDisk, err := os.ReadFile(settingsFile)
	if err != nil || !bytes.Equal(onDisk, saved) {
		t.Fatal("settings changed after failure")
	}
	badSub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline fixture", 503) }))
	defer badSub.Close()
	addr := badSub.URL
	id := subscriptionHash(addr)
	api("POST", "subscriptions/add", fmt.Sprintf(`{"url":%q,"label":"first"}`, addr), 200)
	api("POST", "subscriptions/add", fmt.Sprintf(`{"url":%q,"label":"duplicate"}`, addr), 409)
	api("POST", "subscriptions/update", fmt.Sprintf(`{"id":%q,"prefix":"test","label":"updated"}`, id), 200)
	if rows := a.catalogEntriesLocked(); len(rows) != 1 || rows[0].Label != "updated" || rows[0].Prefix != "test" {
		t.Fatal("catalog update")
	}
	// A closed fixture database rejects persistence after validation succeeds.
	broken, err := openStore(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	liveStore := a.store
	a.store = broken
	api("POST", "subscriptions/update", fmt.Sprintf(`{"id":%q,"label":"failed-update"}`, id), 500)
	api("PUT", "subscriptions", fmt.Sprintf(`{"id":%q,"label":"failed-rename"}`, id), 500)
	api("PUT", "settings", `{"subscriptionUrl":"https://another.invalid/new"}`, 500)
	a.store = liveStore
	if rows := a.catalogEntriesLocked(); len(rows) != 1 || rows[0].Label != "updated" || a.settingsLocked().SubscriptionURL != "" {
		t.Fatal("database failure published catalog or settings")
	}
	api("POST", "subscriptions/activate", fmt.Sprintf(`{"id":%q}`, id), 502)
	if a.settingsLocked().SubscriptionURL != "" {
		t.Fatal("unsuccessful activation replaced the active URL")
	}
	afterActivation, err := os.ReadFile(settingsFile)
	if err != nil || !bytes.Equal(afterActivation, saved) {
		t.Fatal("unsuccessful activation changed settings on disk")
	}
	for _, name := range []string{"first", "second", "del & +"} {
		api("POST", "nodes/manual", fmt.Sprintf(`{"uri":"trojan://fixture@127.0.0.1:9","name":%q}`, name), 200)
	}
	api("DELETE", "nodes/manual?name=del%20%26%20%2B", "", 200)
	api("PUT", "nodes/chain/profile", `{"name":"A","hops":["first","second"]}`, 200)
	api("PUT", "nodes/chain", `{"name":"A","enabled":true,"hops":["first","second"]}`, 200)
	api("PUT", "nodes/chain", `{"name":"A","enabled":true,"hops":["second","first"]}`, 409)
	if got := a.chainSnapshotLocked().Chain; got.Name != "A" || got.Hops[0] != "first" {
		t.Fatal("failed chain edit leaked")
	}
	api("PUT", "nodes/chain", `{"enabled":false}`, 200)
	path := a.manualPath() + ".tmp"
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	api("POST", "nodes/manual", `{"uri":"trojan://fixture@127.0.0.1:9","name":"failed"}`, 400)
	api("DELETE", "nodes/manual?name=second", "", 400)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.loadManualNodesLocked()
	if rows := a.manualSnapshotLocked(); len(rows) != 2 || rows[1].Name != "second" {
		t.Fatal("provider failure changed stored nodes")
	}
	t.Setenv("SMARTVPN_MANAGED_HOST", "off")
	// Start the actual kernel in the temporary home without changing host proxy settings.
	a.mu.Lock()
	err = a.startMihomoLocked()
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.kernelRunningLocked() {
			a.stopMihomoLocked()
		}
	}()
	api("POST", "connect", "", 200)
	api("PUT", "nodes/select", `{"name":"first"}`, 200)
	if a.activeNodeLocked() != "first" {
		t.Fatal("controller did not select first node")
	}
	invalidHome(func() { api("PUT", "nodes/select", `{"name":"second"}`, 500) })
	if a.activeNodeLocked() != "first" || a.settingsLocked().SelectedNode != "first" {
		t.Fatal("failed save changed live selection")
	}
	api("PUT", "nodes/select", `{"name":"second"}`, 200)
	if a.activeNodeLocked() != "second" {
		t.Fatal("controller did not select second node")
	}
	api("POST", "disconnect", "", 200)
	if a.kernelRunningLocked() {
		t.Fatal("disconnect left kernel running")
	}
	api("DELETE", "subscriptions?id="+id, "", 200)
}

// Force a post-start failure before any OS proxy configuration is attempted.
type connectCacheFailure struct{ *appConnectionEffects }

type resumeWithoutSystemProxy struct{ *appConnectionEffects }

func (e resumeWithoutSystemProxy) Call(q connectionEffect) connectionReply {
	switch q.Kind {
	case "system-proxy", "proxy-conflict", "async-patrol", "start-path":
		return connectionReply{}
	}
	return e.appConnectionEffects.Call(q)
}

func TestLiveResumeLastNode(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if bin == "" {
		t.Skip("requires local Mihomo")
	}
	a := storeApp(t)
	testEditSettings(a, func(s *settings) {
		s.MihomoPath, s.ConnectionMode, s.SelectionMode = bin, modeSystemProxy, "auto"
	})
	for _, name := range []string{"first", "previous"} {
		if _, err := a.addManualNodeLocked("trojan://test@127.0.0.1:9#"+name, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.setValue("lastConnectedNode", "previous"); err != nil {
		t.Fatal(err)
	}
	e := resumeWithoutSystemProxy{&appConnectionEffects{a: a, connecting: true, resumeLast: true}}
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.proxyOn = false
		a.dropConnectionLocked()
	}()
	r := runSelectedConnectionService(connectionServiceRequest{Op: "connect"}, e)
	if r.Status != 200 || r.Data["selectedNode"] != "previous" {
		t.Fatalf("resume returned %v", r)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeNodeLocked() != "previous" || a.controlMetaLocked().SelectionMode != "auto" {
		t.Fatal("resume did not restore the real node or changed the saved selection mode")
	}
}

func (e connectCacheFailure) Call(q connectionEffect) connectionReply {
	if q.Kind == "proxy-conflict" {
		return connectionReply{State: connectionStateFromApp(e.a)}
	}
	if q.Kind == "system-proxy" {
		panic("cache failure must precede OS proxy changes")
	}
	return e.appConnectionEffects.Call(q)
}
func TestLiveConnectCacheRollback(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if bin == "" {
		t.Skip("requires installed Mihomo")
	}
	t.Setenv("SMARTVPN_MANAGED_HOST", "off")
	a := storeApp(t)
	testEditSettings(a, func(s *settings) {
		s.MihomoPath = bin
		s.ConnectionMode = modeSystemProxy
		s.SelectionMode = "manual"
		s.SelectedNode = "old-pending"
	})
	a.replaceManualLocked([]manualNode{{Name: "fixture", Proxy: map[string]any{"name": "fixture", "type": "http", "server": "127.0.0.1", "port": 9}}})
	if err := a.saveSettings(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.home, "settings.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.cachedNodesPath()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	e := connectCacheFailure{&appConnectionEffects{a: a, connecting: true}}
	defer e.Release()
	r := runSelectedConnectionService(connectionServiceRequest{Op: "connect"}, e)
	if r.Status != 500 || !strings.Contains(r.Message, "node list") {
		t.Fatal(r)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) || a.kernelRunningLocked() || a.settingsLocked().SelectedNode != "old-pending" || len(a.cachedNodeSnapshotLocked()) != 0 {
		t.Fatal("failed connect did not restore kernel, selection, cache and disk")
	}
}
