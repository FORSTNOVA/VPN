package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if len(a.subscriptions) != 2 {
		t.Fatalf("unexpected list: %+v", a.subscriptions)
	}
	if a.subscriptions[0].Label != "airport-a.com" {
		t.Fatalf("label = %q, want the host without www", a.subscriptions[0].Label)
	}
	if a.subscriptions[0].ID != subscriptionHash("https://www.airport-a.com/sub?token=1") {
		t.Fatalf("id = %q", a.subscriptions[0].ID)
	}

	// A restarted service keeps the list, and an active URL that predates the
	// list is added to it.
	a.settings.SubscriptionURL = "https://airport-c.org/sub"
	reloaded := &app{home: a.home, store: a.store, settings: a.settings}
	reloaded.loadSubscriptionsLocked()
	labels := []string{}
	for _, entry := range reloaded.subscriptions {
		labels = append(labels, entry.Label)
	}
	// hmm: keep the assertion readable
	if len(reloaded.subscriptions) != 3 {
		t.Fatalf("unexpected list: %v", labels)
	}
	if _, ok := reloaded.findSubscriptionLocked(subscriptionHash("https://airport-c.org/sub")); !ok {
		t.Fatalf("the active URL must be in the list: %v", labels)
	}
}

func TestClearNodeBoundState(t *testing.T) {
	a := storeApp(t)
	a.settings.SelectedNode = "香港 01"
	a.settings.SelectionMode = "manual"
	a.lockedRegion = "JP"
	a.lockedNode = "jp-a"
	a.settings.LockedRegion = "JP"
	a.settings.LockedNode = "jp-a"
	a.health["香港 01"] = nodeHealth{Name: "香港 01", State: healthHealthy}
	a.regions["香港 01"] = regionRecord{Name: "香港 01", Country: "HK", Status: regionVerified}
	a.blocked = true
	a.cachedNodes = []proxyNode{{Name: "香港 01"}}
	if err := a.store.saveHealth(a.health["香港 01"]); err != nil {
		t.Fatal(err)
	}
	if err := a.store.saveRegion(a.regions["香港 01"]); err != nil {
		t.Fatal(err)
	}

	a.clearNodeBoundStateLocked()

	if a.settings.SelectedNode != "" || a.settings.SelectionMode != "auto" {
		t.Fatalf("selection must be reset: %+v", a.settings)
	}
	if a.lockedRegion != "" || a.lockedNode != "" || a.settings.LockedRegion != "" || a.settings.LockedNode != "" {
		t.Fatalf("the lock described the old nodes: %+v", a.settings)
	}
	if a.blocked || len(a.health) != 0 || len(a.regions) != 0 || a.cachedNodes != nil {
		t.Fatalf("node state must be dropped: blocked=%v health=%v regions=%v cached=%v",
			a.blocked, a.health, a.regions, a.cachedNodes)
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
	a.settings.SubscriptionURL = "https://airport-b.net/sub?token=2"
	a.ensureSubscriptionLocked(a.settings.SubscriptionURL)
	id := subscriptionHash(a.settings.SubscriptionURL)
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
	a.settings.SubscriptionURL = "https://airport-a.com/sub?token=1"
	a.ensureSubscriptionLocked(a.settings.SubscriptionURL)
	a.ensureSubscriptionLocked("http://127.0.0.1:9/sub")
	writeProfileFor(t, a, a.settings.SubscriptionURL, sampleProfile)

	a.settings.SubscriptionURL = "http://127.0.0.1:9/sub"
	a.cacheProfileLocked([]byte(sampleProfile), "original")
	idB := subscriptionHash(a.settings.SubscriptionURL)
	a.settings.SubscriptionURL = "https://airport-a.com/sub?token=1"
	writeProfileFor(t, a, a.settings.SubscriptionURL, sampleProfile)

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
	if a.settings.SubscriptionURL != "http://127.0.0.1:9/sub" {
		t.Fatalf("the active URL must have switched: %q", a.settings.SubscriptionURL)
	}
	if !a.profileMatchesSubscription() {
		t.Fatal("the profile must belong to the subscription that is now active")
	}
	if !a.profileFromCache {
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
	a.settings.SubscriptionURL = url
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
	if a.settings.SubscriptionURL != "" || len(a.subscriptions) != 0 {
		t.Fatalf("unexpected state: url=%q list=%v", a.settings.SubscriptionURL, a.subscriptions)
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
	a.settings.SubscriptionURL = "https://old.example/sub"
	a.settings.SelectedNode = "旧节点"
	a.settings.SelectionMode = "manual"
	a.lockedRegion, a.lockedNode = "JP", "jp-a"
	a.health["旧节点"] = nodeHealth{Name: "旧节点", State: healthHealthy}
	a.cachedNodes = []proxyNode{{Name: "旧节点"}}

	body := `{"subscriptionUrl":"https://new.example/sub","mihomoPath":"C:\\tools\\mihomo.exe"}`
	recorder := httptest.NewRecorder()
	a.updateSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	// A selection, a lock and health data all name nodes the new subscription
	// may not even have.
	if a.lockedRegion != "" || a.lockedNode != "" || a.settings.SelectedNode != "" ||
		a.settings.SelectionMode != "auto" || len(a.health) != 0 || a.cachedNodes != nil {
		t.Fatalf("the old node set must be dropped: %+v", a.settings)
	}
	// The typed URL joins the saved list, so it can be switched back to later.
	if _, ok := a.findSubscriptionLocked(subscriptionHash("https://new.example/sub")); !ok {
		t.Fatalf("the new URL must be saved: %+v", a.subscriptions)
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

	a.subscriptions = []subscriptionEntry{
		{ID: "sub1", URL: srv1.URL, Label: "主力机场", Prefix: "[主力]", Enabled: true},
		{ID: "sub2", URL: srv2.URL, Label: "备用机场", Prefix: "[备用]", Enabled: true},
	}

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
	if a.settings.AutoMergeHours != 12 {
		t.Fatalf("expected AutoMergeHours 12, got %d", a.settings.AutoMergeHours)
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
	if a.settings.AutoMergeHours != 0 {
		t.Fatalf("expected AutoMergeHours 0, got %d", a.settings.AutoMergeHours)
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

	a.subscriptions = []subscriptionEntry{
		{ID: "sub-a", URL: srvA.URL, Label: "机场A", Prefix: "[A]", Enabled: true},
		{ID: "sub-b", URL: srvB.URL, Label: "机场B", Prefix: "[B]", Enabled: true},
	}
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
	if a.settings.SubscriptionURL != mergedSubscriptionURL {
		t.Fatalf("expected settings.SubscriptionURL to be %q, got %q", mergedSubscriptionURL, a.settings.SubscriptionURL)
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
	a.settings.SubscriptionURL = unreachableURL
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
	if !a.profileFromCache {
		t.Fatal("expected profileFromCache to be true")
	}
	if !a.profileMatchesSubscription() {
		t.Fatal("profile should now match subscription after cache restoration")
	}
}


