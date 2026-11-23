package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Only early rejection paths are invoked. No valid request reaches persistence,
// cache, timers, crypto, network, or a running kernel in this test.
func TestMigrationSubscriptionHTTPDecodeContract(t *testing.T) {
	a := &app{}
	handlers := []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"add", a.addSubscription}, {"update", a.updateSubscription}, {"rename", a.renameSubscription},
		{"activate", a.activateSubscription}, {"hours", a.setAutoMergeHours},
	}
	for _, h := range handlers {
		for _, body := range []string{`{`, `{"unknown":true}`, `[]`} {
			t.Run(h.name+body, func(t *testing.T) {
				w := httptest.NewRecorder()
				h.fn(w, httptest.NewRequest("POST", "/api/subscriptions", strings.NewReader(body)))
				if w.Code != 400 || w.Body.String() != "invalid request body\n" {
					t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
				}
			})
		}
	}
	for _, tc := range []struct {
		name, body, path string
		fn               http.HandlerFunc
		message          string
	}{
		{"update-empty-id", `{"id":"  "}`, "/", a.updateSubscription, "missing subscription id"},
		{"delete-empty-id", "", "/?id=%20", a.deleteSubscription, "missing subscription id"},
		{"hours-negative", `{"hours":-1}`, "/", a.setAutoMergeHours, "hours must be between 0 and 720"},
		{"hours-overflow", `{"hours":721}`, "/", a.setAutoMergeHours, "hours must be between 0 and 720"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.fn(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			if w.Code != 400 || w.Body.String() != tc.message+"\n" {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
	// Decoder pointer presence differs from legacy saved-list enabled defaulting.
	for _, body := range []string{`{}`, `{"enabled":null,"prefix":null}`} {
		var p struct {
			Enabled *bool   `json:"enabled"`
			Prefix  *string `json:"prefix"`
		}
		if err := decodeJSON(strings.NewReader(body), &p); err != nil || p.Enabled != nil || p.Prefix != nil {
			t.Fatalf("absent/null presence %s: %+v %v", body, p, err)
		}
	}
	var p struct {
		Enabled *bool   `json:"enabled"`
		Prefix  *string `json:"prefix"`
	}
	if err := decodeJSON(strings.NewReader(`{"enabled":false,"prefix":""}`), &p); err != nil || p.Enabled == nil || *p.Enabled || p.Prefix == nil || *p.Prefix != "" {
		t.Fatalf("explicit false/empty lost: %+v %v", p, err)
	}
}

// Read-only real list handler: preserve its duplicate-active comparator quirk,
// JSON shape, and source slice rather than silently "fixing" sorting in Rust.
func TestMigrationSubscriptionHTTPListDuplicates(t *testing.T) {
	for _, count := range []int{0, 1, 2, 12, 21, 50, 101} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			a := testControlApp(testControlApp(&app{}, func(a *app) { testCatalogCacheWrite(a, true) }), func(a *app) {
				testSetSettings(a, settings{SubscriptionURL: "https://active.example/sub", AutoMergeHours: 13})
			})
			for i := 0; i < count; i++ {
				u := "https://other.example/sub"
				if i%3 != 0 {
					u = a.settingsLocked().SubscriptionURL
				}
				testCatalogEdit(a, func(entries *[]subscriptionEntry) {
					(*entries) = append((*entries), subscriptionEntry{ID: fmt.Sprint(i), URL: u, Label: []string{"same", "z", "a"}[i%3], Enabled: i%2 == 0})
				})
			}
			before := append([]subscriptionEntry(nil), testCatalogRead(a)...)
			want := append([]subscriptionEntry(nil), testCatalogRead(a)...)
			sort.SliceStable(want, func(i, j int) bool {
				if want[i].URL == a.settingsLocked().SubscriptionURL {
					return true
				}
				if want[j].URL == a.settingsLocked().SubscriptionURL {
					return false
				}
				return want[i].Label < want[j].Label
			})
			w := httptest.NewRecorder()
			a.listSubscriptions(w, httptest.NewRequest("GET", "/api/subscriptions", nil))
			var got struct {
				Subscriptions  []subscriptionEntry `json:"subscriptions"`
				ActiveURL      string              `json:"activeUrl"`
				IsMerged       bool                `json:"isMerged"`
				FromCache      bool                `json:"fromCache"`
				Running        bool                `json:"running"`
				AutoMergeHours int                 `json:"autoMergeHours"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || !reflect.DeepEqual(want, got.Subscriptions) || got.ActiveURL != a.settingsLocked().SubscriptionURL || !got.FromCache || got.Running || got.IsMerged || got.AutoMergeHours != 13 {
				t.Fatalf("list contract code=%d body=%s want=%+v", w.Code, w.Body.String(), want)
			}
			if !reflect.DeepEqual(before, testCatalogRead(a)) {
				t.Fatal("list sorted original registry in place")
			}
		})
	}
}
