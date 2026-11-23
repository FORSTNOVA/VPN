package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleProfile = "proxies:\n  - name: \"香港 01\"\n    type: vmess\n    server: hk.example.com\n    port: 443\n"

// writeProfileFor puts a profile in place that claims to belong to the given
// subscription, which is what profileMatchesSubscription checks.
func writeProfileFor(t *testing.T, a *app, subscriptionURL string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(a.profilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.profilePath(), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.profilePath()+".source", []byte(subscriptionHash(subscriptionURL)), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionListRoundTrip(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()
	a.ensureSubscriptionLocked("https://www.airport-a.com/sub?token=1")
	a.ensureSubscriptionLocked("https://airport-b.net/sub?token=2")
	// The same URL twice stays one entry.
	a.ensureSubscriptionLocked("https://www.airport-a.com/sub?token=1")
	if len(testCatalogRead(a)) != 2 {
		t.Fatalf("unexpected list: %+v", testCatalogRead(a))
	}
	if testCatalogRead(a)[0].Label != "airport-a.com" {
		t.Fatalf("label = %q, want the host without www", testCatalogRead(a)[0].Label)
	}
	if testCatalogRead(a)[0].ID != subscriptionHash("https://www.airport-a.com/sub?token=1") {
		t.Fatalf("id = %q", testCatalogRead(a)[0].ID)
	}

	// A restarted service keeps the list, and an active URL that predates the
	// list is added to it.
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://airport-c.org/sub" })
	reloaded := testControlApp(&app{home: a.home, store: a.store}, func(fresh *app) { testSetSettings(fresh, a.settingsLocked()) })
	reloaded.loadSubscriptionsLocked()
	labels := []string{}
	for _, entry := range testCatalogRead(reloaded) {
		labels = append(labels, entry.Label)
	}
	// hmm: keep the assertion readable
	if len(testCatalogRead(reloaded)) != 3 {
		t.Fatalf("unexpected list: %v", labels)
	}
	if _, ok := reloaded.findSubscriptionLocked(subscriptionHash("https://airport-c.org/sub")); !ok {
		t.Fatalf("the active URL must be in the list: %v", labels)
	}
}

func TestClearNodeBoundState(t *testing.T) {
	a := storeApp(t)
	testEditSettings(a, func(s *settings) { s.SelectedNode = "香港 01" })
	testEditSettings(a, func(s *settings) { s.SelectionMode = "manual" })
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "jp-a"})
	testEditSettings(a, func(s *settings) { s.LockedRegion = "JP" })
	testEditSettings(a, func(s *settings) { s.LockedNode = "jp-a" })
	testEditHealth(a, func(p *healthDomainProjection) {
		p.Health["香港 01"] = nodeHealth{Name: "香港 01", State: healthHealthy}
	})
	testEditHealth(a, func(p *healthDomainProjection) {
		p.Regions["香港 01"] = regionRecord{Name: "香港 01", Country: "HK", Status: regionVerified}
	})
	testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = true })
	a.cachedNodes = []proxyNode{{Name: "香港 01"}}
	if err := a.store.saveHealth(a.healthDomainLocked().Health["香港 01"]); err != nil {
		t.Fatal(err)
	}
	if err := a.store.saveRegion(a.healthDomainLocked().Regions["香港 01"]); err != nil {
		t.Fatal(err)
	}

	a.clearNodeBoundStateLocked()

	if a.settingsLocked().SelectedNode != "" || a.settingsLocked().SelectionMode != "auto" {
		t.Fatalf("selection must be reset: %+v", a.settingsLocked())
	}
	if a.controlLocked().RuntimeLockedRegion != "" || a.controlLocked().RuntimeLockedNode != "" || a.settingsLocked().LockedRegion != "" || a.settingsLocked().LockedNode != "" {
		t.Fatalf("the lock described the old nodes: %+v", a.settingsLocked())
	}
	if a.healthDomainLocked().Blocked || len(a.healthDomainLocked().Health) != 0 || len(a.healthDomainLocked().Regions) != 0 || a.cachedNodes != nil {
		t.Fatalf("node state must be dropped: blocked=%v health=%v regions=%v cached=%v",
			a.healthDomainLocked().Blocked, a.healthDomainLocked().Health, a.healthDomainLocked().Regions, a.cachedNodes)
	}
	// A stored region for a name that no longer exists would make an old node
	// look verified if a subscription happens to reuse the name.
	stored, err := a.store.loadRegions()
	if err != nil || len(stored) != 0 {
		t.Fatalf("stored regions must be cleared: %v %v", stored, err)
	}
	storedHealth, err := a.store.loadHealth()
	if err != nil || len(storedHealth) != 0 {
		t.Fatalf("stored health must be cleared: %v %v", storedHealth, err)
	}
}

func TestUseCachedProfileAdoptsTheSubscription(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://airport-b.net/sub?token=2" })
	a.ensureSubscriptionLocked(a.settingsLocked().SubscriptionURL)
	id := subscriptionHash(a.settingsLocked().SubscriptionURL)
	a.cacheProfileLocked([]byte(sampleProfile), "original")
	if _, err := os.Stat(a.profileCachePath(id)); err != nil {
		t.Fatalf("the cached copy must be written: %v", err)
	}

	// Pretend the profile in place belongs to another subscription.
	writeProfileFor(t, a, "https://airport-a.com/sub?token=1", sampleProfile)
	if a.profileMatchesSubscription() {
		t.Fatal("the test needs a mismatched profile to start from")
	}

	if err := a.useCachedProfileLocked(id); err != nil {
		t.Fatal(err)
	}
	// This is what makes an offline switch work: everything downstream only asks
	// whether the profile matches the active subscription.
	if !a.profileMatchesSubscription() {
		t.Fatal("the cached copy must be adopted as this subscription's profile")
	}
	if len(a.cachedNodes) != 1 || a.cachedNodes[0].Name != "香港 01" {
		t.Fatalf("the node list must come from the copy: %+v", a.cachedNodes)
	}
}

func TestActivateSwitchesAndFallsBackToTheCache(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()
	// Subscription A is in use, B has been fetched before and is now unreachable.
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://airport-a.com/sub?token=1" })
	a.ensureSubscriptionLocked(a.settingsLocked().SubscriptionURL)
	a.ensureSubscriptionLocked("http://127.0.0.1:9/sub")
	writeProfileFor(t, a, a.settingsLocked().SubscriptionURL, sampleProfile)

	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "http://127.0.0.1:9/sub" })
	a.cacheProfileLocked([]byte(sampleProfile), "original")
	idB := subscriptionHash(a.settingsLocked().SubscriptionURL)
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://airport-a.com/sub?token=1" })
	writeProfileFor(t, a, a.settingsLocked().SubscriptionURL, sampleProfile)

	body, _ := json.Marshal(map[string]string{"id": idB})
	recorder := httptest.NewRecorder()
	a.activateSubscription(recorder, httptest.NewRequest(http.MethodPost, "/api/subscriptions/activate", strings.NewReader(string(body))))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	var answer map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer["fromCache"] != true {
		t.Fatalf("the cached copy is what makes this work: %v", answer)
	}
	if a.settingsLocked().SubscriptionURL != "http://127.0.0.1:9/sub" {
		t.Fatalf("the active URL must have switched: %q", a.settingsLocked().SubscriptionURL)
	}
	if !a.profileMatchesSubscription() {
		t.Fatal("the profile must belong to the subscription that is now active")
	}
	if !testCatalogCacheRead(a) {
		t.Fatal("the answer must be honest about where the profile came from")
	}
	if len(a.cachedNodes) == 0 {
		t.Fatal("the nodes from the copy must be usable")
	}
}

func TestDeleteActiveSubscriptionLeavesNothingBehind(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()
	url := "https://airport-a.com/sub?token=1"
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = url })
	a.ensureSubscriptionLocked(url)
	id := subscriptionHash(url)
	writeProfileFor(t, a, url, sampleProfile)
	a.cacheProfileLocked([]byte(sampleProfile), "original")
	a.cachedNodes = []proxyNode{{Name: "香港 01"}}

	recorder := httptest.NewRecorder()
	a.deleteSubscription(recorder, httptest.NewRequest(http.MethodDelete, "/api/subscriptions?id="+id, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	if a.settingsLocked().SubscriptionURL != "" || len(testCatalogRead(a)) != 0 {
		t.Fatalf("unexpected state: url=%q list=%v", a.settingsLocked().SubscriptionURL, testCatalogRead(a))
	}
	if _, err := os.Stat(a.profilePath()); !os.IsNotExist(err) {
		t.Fatal("the profile of the removed subscription must go with it")
	}
	if _, err := os.Stat(a.profileCachePath(id)); !os.IsNotExist(err) {
		t.Fatal("its cached copy must go too")
	}
}

func TestChangingTheSubscriptionDropsTheOldNodes(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://old.example/sub" })
	testEditSettings(a, func(s *settings) { s.SelectedNode = "旧节点" })
	testEditSettings(a, func(s *settings) { s.SelectionMode = "manual" })
	a.patchControlLocked(controlRuntimeLockedRegion|controlRuntimeLockedNode, serviceControlState{RuntimeLockedRegion: "JP", RuntimeLockedNode: "jp-a"})
	testEditHealth(a, func(p *healthDomainProjection) {
		p.Health["旧节点"] = nodeHealth{Name: "旧节点", State: healthHealthy}
	})
	a.cachedNodes = []proxyNode{{Name: "旧节点"}}

	body := `{"subscriptionUrl":"https://new.example/sub","mihomoPath":"C:\\tools\\mihomo.exe"}`
	recorder := httptest.NewRecorder()
	a.updateSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	// A selection, a lock and health data all name nodes the new subscription
	// may not even have.
	if a.controlLocked().RuntimeLockedRegion != "" || a.controlLocked().RuntimeLockedNode != "" || a.settingsLocked().SelectedNode != "" ||
		a.settingsLocked().SelectionMode != "auto" || len(a.healthDomainLocked().Health) != 0 || a.cachedNodes != nil {
		t.Fatalf("the old node set must be dropped: %+v", a.settingsLocked())
	}
	// The typed URL joins the saved list, so it can be switched back to later.
	if _, ok := a.findSubscriptionLocked(subscriptionHash("https://new.example/sub")); !ok {
		t.Fatalf("the new URL must be saved: %+v", testCatalogRead(a))
	}
}

func TestRenameSubscription(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()
	url := "https://airport-a.com/sub?token=1"
	a.ensureSubscriptionLocked(url)
	id := subscriptionHash(url)

	body, _ := json.Marshal(map[string]string{"id": id, "label": "备用机场"})
	recorder := httptest.NewRecorder()
	a.renameSubscription(recorder, httptest.NewRequest(http.MethodPut, "/api/subscriptions", strings.NewReader(string(body))))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	entry, _ := a.findSubscriptionLocked(id)
	if entry.Label != "备用机场" {
		t.Fatalf("label = %q", entry.Label)
	}

	for _, bad := range []string{"", strings.Repeat("字", 41)} {
		payload, _ := json.Marshal(map[string]string{"id": id, "label": bad})
		recorder = httptest.NewRecorder()
		a.renameSubscription(recorder, httptest.NewRequest(http.MethodPut, "/api/subscriptions", strings.NewReader(string(payload))))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("label %q must be refused, code = %d", bad, recorder.Code)
		}
	}
}

func TestParseSubscriptionUserInfo(t *testing.T) {
	header := "upload=1073741824; download=21474836480; total=107374182400; expire=1775000000"
	info := parseSubscriptionUserInfo(header)
	if info == nil {
		t.Fatal("expected non-nil userInfo")
	}
	if info.Upload != 1073741824 || info.Download != 21474836480 || info.Total != 107374182400 || info.Expire != 1775000000 {
		t.Fatalf("unexpected parsed values: %+v", info)
	}

	// Empty header returns nil
	if parseSubscriptionUserInfo("") != nil {
		t.Fatal("expected nil for empty header")
	}
}

func TestMultiSubscriptionMergeAndPrefix(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()

	// Prepare two mocked subscription servers
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=100; download=200; total=1000; expire=1800000000")
		w.Write([]byte("proxies:\n  - name: \"香港 01\"\n    type: vmess\n    server: hk1.example.com\n    port: 443\n"))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("proxies:\n  - name: \"香港 01\"\n    type: vmess\n    server: hk2.example.com\n    port: 443\n  - name: \"日本 01\"\n    type: trojan\n    server: jp.example.com\n    port: 443\n"))
	}))
	defer srv2.Close()

	testCatalogEdit(a, func(entries *[]subscriptionEntry) {
		(*entries) = []subscriptionEntry{
			{ID: "sub1", URL: srv1.URL, Label: "主力机场", Prefix: "[主力]", Enabled: true},
			{ID: "sub2", URL: srv2.URL, Label: "备用机场", Prefix: "[备用]", Enabled: true},
		}
	})

	nodes, err := a.mergeAllEnabledSubscriptionsLocked()
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes after merge, got %d: %+v", len(nodes), nodes)
	}

	// Verify prefixes and deduplication
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	if names[0] != "[主力] 香港 01" {
		t.Errorf("expected '[主力] 香港 01', got %q", names[0])
	}
	if names[1] != "[备用] 香港 01" {
		t.Errorf("expected '[备用] 香港 01', got %q", names[1])
	}
	if names[2] != "[备用] 日本 01" {
		t.Errorf("expected '[备用] 日本 01', got %q", names[2])
	}

	// Verify UserInfo was saved on sub1
	sub1, _ := a.findSubscriptionLocked("sub1")
	if sub1.UserInfo == nil || sub1.UserInfo.Total != 1000 {
		t.Fatalf("expected userinfo on sub1: %+v", sub1.UserInfo)
	}
}

func TestAddAndUpdateSubscriptionAPI(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()

	// Test Add
	addReq := map[string]any{
		"url":     "https://example.com/sub",
		"label":   "测试机场",
		"prefix":  "[测试]",
		"enabled": true,
	}
	body, _ := json.Marshal(addReq)
	rec := httptest.NewRecorder()
	a.addSubscription(rec, httptest.NewRequest(http.MethodPost, "/api/subscriptions/add", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("add failed with %d: %s", rec.Code, rec.Body.String())
	}
	id := subscriptionHash("https://example.com/sub")
	entry, ok := a.findSubscriptionLocked(id)
	if !ok || entry.Prefix != "[测试]" || !entry.Enabled {
		t.Fatalf("subscription not saved properly: %+v", entry)
	}

	// Test Update
	f := false
	upReq := map[string]any{
		"id":      id,
		"label":   "更新后的机场",
		"prefix":  "[新前缀]",
		"enabled": &f,
	}
	upBody, _ := json.Marshal(upReq)
	rec = httptest.NewRecorder()
	a.updateSubscription(rec, httptest.NewRequest(http.MethodPost, "/api/subscriptions/update", strings.NewReader(string(upBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("update failed with %d: %s", rec.Code, rec.Body.String())
	}
	entry, _ = a.findSubscriptionLocked(id)
	if entry.Label != "更新后的机场" || entry.Prefix != "[新前缀]" || entry.Enabled != false {
		t.Fatalf("update did not apply properly: %+v", entry)
	}
}

func TestAutoMergeHoursAPI(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()

	// Set to 12 hours
	reqBody := `{"hours": 12}`
	rec := httptest.NewRecorder()
	a.setAutoMergeHours(rec, httptest.NewRequest(http.MethodPost, "/api/subscriptions/auto-merge", strings.NewReader(reqBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("setAutoMergeHours failed: %d: %s", rec.Code, rec.Body.String())
	}
	if a.settingsLocked().AutoMergeHours != 12 {
		t.Fatalf("expected AutoMergeHours 12, got %d", a.settingsLocked().AutoMergeHours)
	}

	// Verify listSubscriptions returns autoMergeHours
	rec = httptest.NewRecorder()
	a.listSubscriptions(rec, httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil))
	var resp struct {
		AutoMergeHours int `json:"autoMergeHours"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if resp.AutoMergeHours != 12 {
		t.Fatalf("expected list autoMergeHours 12, got %d", resp.AutoMergeHours)
	}

	// Set back to 0
	reqBody = `{"hours": 0}`
	rec = httptest.NewRecorder()
	a.setAutoMergeHours(rec, httptest.NewRequest(http.MethodPost, "/api/subscriptions/auto-merge", strings.NewReader(reqBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset to 0 failed: %d: %s", rec.Code, rec.Body.String())
	}
	if a.settingsLocked().AutoMergeHours != 0 {
		t.Fatalf("expected AutoMergeHours 0, got %d", a.settingsLocked().AutoMergeHours)
	}
}

func TestMergedSubscriptionActivationAndKernelStart(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()

	yamlA := `proxies:
  - {name: "A节点01", type: vmess, server: 1.1.1.1, port: 443, uuid: "uuid-1"}
`
	yamlB := `proxies:
  - {name: "B节点01", type: vmess, server: 2.2.2.2, port: 443, uuid: "uuid-2"}
`
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(yamlA))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(yamlB))
	}))
	defer srvB.Close()

	testCatalogEdit(a, func(entries *[]subscriptionEntry) {
		(*entries) = []subscriptionEntry{
			{ID: "sub-a", URL: srvA.URL, Label: "机场A", Prefix: "[A]", Enabled: true},
			{ID: "sub-b", URL: srvB.URL, Label: "机场B", Prefix: "[B]", Enabled: true},
		}
	})
	_ = a.saveSubscriptionsLocked()

	// Perform merge
	nodes, err := a.mergeAllEnabledSubscriptionsLocked()
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 merged nodes, got %d", len(nodes))
	}

	// Verify active URL is __merged__
	if a.settingsLocked().SubscriptionURL != mergedSubscriptionURL {
		t.Fatalf("expected settings.SubscriptionURL to be %q, got %q", mergedSubscriptionURL, a.settingsLocked().SubscriptionURL)
	}

	// Verify profileMatchesSubscription() is true
	if !a.profileMatchesSubscription() {
		t.Fatalf("profileMatchesSubscription should be true for merged subscription")
	}

	// Verify activateSubscription with __merged__
	rec := httptest.NewRecorder()
	reqBody := fmt.Sprintf(`{"id": %q}`, mergedSubscriptionURL)
	a.activateSubscription(rec, httptest.NewRequest(http.MethodPost, "/api/subscriptions/activate", strings.NewReader(reqBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("activate __merged__ failed: %d: %s", rec.Code, rec.Body.String())
	}

	// Verify listSubscriptions returns isMerged: true
	rec = httptest.NewRecorder()
	a.listSubscriptions(rec, httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil))
	var listResp struct {
		IsMerged  bool   `json:"isMerged"`
		ActiveUrl string `json:"activeUrl"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if !listResp.IsMerged || listResp.ActiveUrl != mergedSubscriptionURL {
		t.Fatalf("listSubscriptions should reflect merged state: %+v", listResp)
	}
}

func TestFetchProfileFallsBackToCacheWhenNetworkFails(t *testing.T) {
	a := storeApp(t)
	a.loadSubscriptionsLocked()

	// Given an unreachable remote subscription
	unreachableURL := "http://127.0.0.1:9/unreachable-airport"
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = unreachableURL })
	a.ensureSubscriptionLocked(unreachableURL)

	// Pre-populate the cache for this subscription
	id := subscriptionHash(unreachableURL)
	a.cacheProfileForID(id, []byte(sampleProfile), "yaml")

	// Ensure profilePath does NOT match (e.g. no subscription.yaml yet)
	_ = os.Remove(a.profilePath())
	_ = os.Remove(a.profilePath() + ".source")

	if a.profileMatchesSubscription() {
		t.Fatal("profile should not match before fetch")
	}

	// Now call fetchProfileLocked: network will fail, but it should transparently restore from cache
	nodes, err := a.fetchProfileLocked()
	if err != nil {
		t.Fatalf("fetchProfileLocked should have fallen back to cache, but got error: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected cached nodes to be returned")
	}
	if !testCatalogCacheRead(a) {
		t.Fatal("expected profileFromCache to be true")
	}
	if !a.profileMatchesSubscription() {
		t.Fatal("profile should now match subscription after cache restoration")
	}
}

func TestLiveSubscriptionMergeThroughMihomo(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if bin == "" {
		t.Skip("set SMARTVPN_TEST_MIHOMO to exercise the installed kernel")
	}
	a := storeApp(t)
	// The process watcher takes the app lock when Mihomo exits. Exercise the
	// same lock boundary as the production handlers, including explicit stop.
	a.newToken()
	locked := func(f func()) {
		a.mu.Lock()
		defer a.mu.Unlock()
		f()
	}
	var started time.Time
	locked(func() {
		testEditSettings(a, func(s *settings) { s.ConnectionMode = modeSystemProxy; s.ImportPolicy = true })
		testEditSettings(a, func(s *settings) { s.MihomoPath = bin })
		a.manualNodes = []manualNode{{Name: "original", Proxy: map[string]any{"name": "original", "type": "http", "server": "127.0.0.1", "port": 9}}}
		if err := a.startMihomoLocked(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			locked(func() {
				if err := a.stopMihomoLocked(); err != nil {
					t.Errorf("stop Mihomo: %v", err)
				}
			})
		})
		if err := a.switchGroupLocked("original"); err != nil {
			t.Fatal(err)
		}
		testEditSettings(a, func(s *settings) { s.SelectedNode = "original" })
		testEditSettings(a, func(s *settings) { s.SelectionMode = "manual" })
		started = a.started
	})
	post := func(handler http.HandlerFunc, body string) {
		t.Helper()
		r := httptest.NewRecorder()
		handler(r, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if r.Code != 200 {
			t.Fatalf("status %d: %s", r.Code, r.Body.String())
		}
	}
	post(a.addSubscription, `{"url":"trojan://example@127.0.0.1:8443#incoming","prefix":"demo","label":"local-one"}`)
	var id string
	locked(func() {
		if len(testCatalogRead(a)) != 1 || testCatalogRead(a)[0].Enabled || a.settingsLocked().SubscriptionURL != "" {
			t.Fatal("adding changed active subscription or merge selection")
		}
		id = testCatalogRead(a)[0].ID
	})
	post(a.updateSubscription, fmt.Sprintf(`{"id":%q,"enabled":true}`, id))
	locked(func() {
		if testCatalogRead(a)[0].Prefix != "demo" {
			t.Fatal("selection erased source prefix")
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `defaults: &local {type: http, server: node.localhost, port: 020003}
proxies: [{<<: *local, name: demo incoming}]
proxy-groups: [{name: local-policy, type: select, proxies: [demo incoming]}]
rules: ["DOMAIN,fixture.invalid,local-policy"]
dns: {nameserver-policy: {node.localhost: https://127.0.0.1:9/dns-query}}
`)
	}))
	defer server.Close()
	post(a.addSubscription, fmt.Sprintf(`{"url":%q,"enabled":true,"label":"local-two"}`, server.URL))
	post(a.mergeSubscriptions, "")
	checkSubscriptionQueryLabels(t, a, "local-two")
	post(a.updateSubscription, fmt.Sprintf(`{"id":%q,"label":"local-two-refreshed"}`, subscriptionHash(server.URL)))
	post(a.mergeSubscriptions, "")
	checkSubscriptionQueryLabels(t, a, "local-two-refreshed")
	// A fresh process cannot inherit Go maps, Rust state, or the live controller.
	cmd := exec.Command(os.Args[0], "-test.run=^TestSubscriptionNodeQueryAfterRestart$")
	cmd.Env = append(os.Environ(), "SMARTVPN_TEST_REOPEN_NODE_HOME="+a.home)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restarted backend query: %v %s", err, log)
	}
	preview := httptest.NewRecorder()
	a.subscriptionPolicy(preview, httptest.NewRequest("GET", "/", nil))
	var policy struct {
		Groups   []sourceGroup
		Rules    []previewRule
		Sections []policySection
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &policy); err != nil || preview.Code != 200 || len(policy.Groups) != 1 || len(policy.Rules) != 1 || len(policy.Sections) == 0 || policy.Rules[0].Applied != "DOMAIN,fixture.invalid,SmartVPN" {
		t.Fatalf("unexpected policy preview: %d %#v (%v)", preview.Code, policy, err)
	}
	body, err := os.ReadFile(a.profilePath())
	if err != nil {
		t.Fatal(err)
	}
	dns := entryDNS(body)["node.localhost"]
	if len(dns) != 1 || dns[0] != "https://127.0.0.1:9/dns-query" {
		t.Fatal("merged DNS policy missing")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeNodeLocked() != "original" || a.started != started {
		t.Fatal("merge changed the current route")
	}
	ruleSet, err := a.mihomoRequest(http.MethodGet, "/rules", nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := ruleSet["rules"].([]any)
	found := false
	for _, v := range rules {
		row, _ := v.(map[string]any)
		if row["payload"] == "fixture.invalid" && row["proxy"] == "SmartVPN" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("imported rule missing from running Mihomo controller")
	}
	if len(a.cachedNodes) != 2 {
		t.Fatal("missing merged node")
	}
	for _, node := range a.cachedNodes {
		if err := a.switchGroupLocked(node.Name); err != nil {
			t.Fatal("merged node unavailable in live kernel:", err)
		}
	}
}

func checkSubscriptionQueryLabels(t *testing.T, a *app, second string) {
	t.Helper()
	r := httptest.NewRecorder()
	a.nodes(r, httptest.NewRequest("GET", "/nodes", nil))
	var listed struct{ Nodes []proxyNode }
	if err := json.Unmarshal(r.Body.Bytes(), &listed); err != nil || r.Code != 200 {
		t.Fatal("node query", err, r.Code)
	}
	labels := map[string]string{}
	for _, n := range listed.Nodes {
		labels[n.Name] = n.SourceLabel
	}
	if labels["demo incoming"] != "local-one" || labels["demo incoming (2)"] != second {
		t.Fatal("duplicate source labels", labels)
	}
}

func TestSubscriptionNodeQueryAfterRestart(t *testing.T) {
	home := os.Getenv("SMARTVPN_TEST_REOPEN_NODE_HOME")
	if home == "" {
		t.Skip("called by live merge test with its temporary store")
	}
	db, err := openStore(filepath.Join(home, "smartvpn.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &app{home: home, store: db}
	a.loadSettings()
	a.loadCachedNodes()
	a.loadManualNodesLocked()
	a.loadSubscriptionsLocked()
	a.restoreSourceLabels()
	checkSubscriptionQueryLabels(t, a, "local-two-refreshed")
}
