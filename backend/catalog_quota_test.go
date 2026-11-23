package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exercise the HTTP boundary, then simulate a fetch that completes after an edit.
func TestCatalogConfigurationAndQuota(t *testing.T) {
	b, err := newSelectedServiceStateBackend()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{service: newServiceAuthority(b)}
	defer a.service.Close()
	initial := subscriptionUserInfo{Upload: 7}
	const oldURL = "https://old.example/profile"
	const newURL = "https://new.example/profile"
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "one", URL: oldURL, Label: "A", Enabled: true, Prefix: "old", UserInfo: &initial}, {ID: "two", URL: "https://second.example/profile", Label: "Z"}})
	native := a.service.quotaStore != nil
	if want := os.Getenv("SMARTVPN_TEST_QUOTA_STORE"); want != "" && native != (want == "rust") {
		t.Fatalf("native quota=%v, expected %s", native, want)
	}
	initial.Upload = 8
	wantUpload := int64(8)
	if native {
		wantUpload = 7
	}
	list := func() []subscriptionEntry {
		w := httptest.NewRecorder()
		a.listSubscriptions(w, nil)
		var resp struct{ Subscriptions []subscriptionEntry }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resp) != nil || len(resp.Subscriptions) != 2 {
			t.Fatalf("list %d: %s", w.Code, w.Body)
		}
		return resp.Subscriptions
	}
	entries := list()
	if entries[0].UserInfo.Upload != wantUpload || entries[1].UserInfo != nil {
		t.Fatal("alias or absent quota lost")
	}
	fx := newAppQuotaEffects(a)
	fx.proxies = func() []string { return []string{""} }
	fx.now = func() int64 { return 123 }
	refresh := func() quotaServiceResult {
		return runSelectedQuotaService(quotaServiceRequest{Op: "refresh", ID: "one"}, fx)
	}
	fx.fetch = func(url, proxy string) (*subscriptionUserInfo, error) {
		update := httptest.NewRecorder()
		a.updateSubscription(update, httptest.NewRequest("POST", "/", strings.NewReader(`{"id":" one ","url":"https://new.example/profile","prefix":"","enabled":false,"label":" B "}`)))
		if update.Code != 200 {
			t.Fatal(update.Body)
		}
		return &subscriptionUserInfo{}, nil
	}
	if got := refresh(); got.Status != 409 {
		t.Fatalf("stale fetch: %+v", got)
	}
	entry := list()[0]
	if entry.UserInfo.Upload != wantUpload || entry.QuotaUpdatedAt != 0 || entry.URL != newURL || entry.Label != "B" || entry.Prefix != "" || entry.Enabled {
		t.Fatalf("stale commit or wrong config: %+v", entry)
	}
	for _, value := range []subscriptionUserInfo{{}, {math.MinInt64, math.MaxInt64, -1, 42}} {
		fx.fetch = func(string, string) (*subscriptionUserInfo, error) { return &value, nil }
		if got := refresh(); got.Status != 200 || !got.HasEntry || *got.Entry.UserInfo != value {
			t.Fatalf("refresh: %+v", got)
		}
		entry = list()[0]
		if entry.UserInfo == nil || *entry.UserInfo != value || entry.QuotaUpdatedAt != 123 {
			t.Fatalf("quota snapshot %+v", entry)
		}
		h := a.catalogQueryLocked(serviceCatalogQuery{Kind: 3, ID: "one"}).Entry.InfoHandle
		if native && a.service.infos[h] != nil {
			t.Fatal("read-only list retained a Go quota copy")
		}
	}
	p, _ := a.findSubscriptionLocked("one")
	p.UserInfo.Download = 13
	wantDownload := int64(13)
	if native {
		wantDownload = math.MaxInt64
	}
	if list()[0].UserInfo.Download != wantDownload {
		t.Fatal("lookup returned incorrect quota ownership")
	}
}

func TestCatalogConcurrentRefreshAndEdit(t *testing.T) {
	for _, tc := range []struct {
		name            string
		full, changeURL bool
		status          int
	}{
		{"quota-url", false, true, 409}, {"quota-label", false, false, 200}, {"profile-url", true, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once, unblock sync.Once
			resume := func() { unblock.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(entered) })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Subscription-Userinfo", "upload=13; download=27; total=1000; expire=42")
				w.Write([]byte("proxies:\n  - {name: fixture, type: http, server: 127.0.0.1, port: 12345}\n"))
			}))
			defer server.Close()
			defer resume()
			b, err := newSelectedServiceStateBackend()
			if err != nil {
				t.Fatal(err)
			}
			a := &app{service: newServiceAuthority(b), home: t.TempDir()}
			defer a.service.Close()
			original := server.URL + "/sub"
			state := settingsStateFromApp(a)
			markCatalogAppend(&state, subscriptionEntry{ID: "one", URL: original, UserInfo: &subscriptionUserInfo{Upload: 1}, QuotaUpdatedAt: 7})
			applySettingsServiceState(a, state)
			refreshDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"one"}`))
				if tc.full {
					a.refreshSingleSubscription(w, req)
				} else {
					a.refreshSubscriptionQuota(w, req)
				}
				refreshDone <- w
			}()
			select {
			case <-entered:
			case w := <-refreshDone:
				t.Fatalf("refresh did not fetch: %d %s", w.Code, w.Body)
			case <-time.After(5 * time.Second):
				t.Fatal("fetch never reached fixture")
			}
			if a.mu.TryLock() {
				a.mu.Unlock()
				if tc.full {
					t.Error("profile refresh released its lock during download")
				}
			} else if !tc.full {
				t.Error("quota refresh held its lock during download")
			}
			target := original
			edit := map[string]string{"id": "one", "label": "edited"}
			if tc.changeURL {
				target = server.URL + "/new"
				edit["url"] = target
			}
			body, _ := json.Marshal(edit)
			updating := make(chan struct{})
			updated := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				close(updating)
				a.updateSubscription(w, httptest.NewRequest("POST", "/", strings.NewReader(string(body))))
				updated <- w
			}()
			<-updating
			if tc.full {
				resume()
			}
			update := <-updated
			resume()
			refresh := <-refreshDone
			if update.Code != 200 || refresh.Code != tc.status {
				t.Fatalf("edit %d %s refresh %d %s", update.Code, update.Body, refresh.Code, refresh.Body)
			}
			got, _ := a.findSubscriptionByURL(target)
			if got.ID != "one" || got.URL != target || got.Label != "edited" || got.UserInfo == nil {
				t.Fatalf("wrong final entry %+v", got)
			}
			if tc.status == 409 {
				if got.UserInfo.Upload != 1 || got.QuotaUpdatedAt != 7 {
					t.Fatal("stale result committed")
				}
			} else if *got.UserInfo != (subscriptionUserInfo{13, 27, 1000, 42}) || got.QuotaUpdatedAt <= 7 || (tc.full && got.NodeCount != 1) {
				t.Fatalf("lost successful refresh %+v", got)
			}
			if a.service.quotaStore != nil && len(a.service.infoReverse) != 0 {
				t.Fatal("catalog retained a Go quota pointer")
			}
		})
	}
}

func TestCatalogRestoreAndPatchQuotaOwnership(t *testing.T) {
	b, err := newSelectedServiceStateBackend()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{service: newServiceAuthority(b)}
	defer a.service.Close()
	first := subscriptionUserInfo{Total: 100}
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "a", UserInfo: &first}, {ID: "b", UserInfo: &first}})
	native := a.service.quotaStore != nil
	read := func(i int) *subscriptionUserInfo {
		row, ok := a.catalogEntryLocked(i)
		if !ok {
			t.Fatal("missing row", i)
		}
		return row.UserInfo
	}
	handle := func(i int) uint64 {
		return a.catalogQueryLocked(serviceCatalogQuery{Kind: 1, Index: i}).Entry.InfoHandle
	}
	if (handle(0) == handle(1)) == native {
		t.Fatal("unexpected ownership across rows")
	}
	next := subscriptionUserInfo{Total: 200}
	a.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: &next})
	h := handle(0)
	watermark := a.service.nextInfo
	a.patchCatalogEntryLocked(0, catalogLabel, subscriptionEntry{Label: "new", UserInfo: &subscriptionUserInfo{Total: 300}})
	if handle(0) != h || a.service.nextInfo != watermark {
		t.Fatal("metadata patch registered a quota")
	}
	a.catalogCommandLocked(serviceCatalogCommand{Action: catalogBeginDelete})
	a.catalogCommandLocked(serviceCatalogCommand{Action: catalogRemoveID, ID: "a"})
	next.Total = 201
	a.catalogCommandLocked(serviceCatalogCommand{Action: catalogRestoreDelete})
	a.catalogCommandLocked(serviceCatalogCommand{Action: catalogFinishDelete})
	got := read(0)
	if native {
		if got == &next || got.Total != 200 || a.service.infoReverse != nil {
			t.Fatal("Rust retained caller pointer")
		}
		got.Total = 202
		if read(0).Total != 200 {
			t.Fatal("snapshot changed the Rust quota")
		}
	} else if got != &next || got.Total != 201 {
		t.Fatal("legacy pointer stopped working")
	}
	if handle(0) != h || read(1).Total != 100 {
		t.Fatal("restore lost row or resource")
	}
}
