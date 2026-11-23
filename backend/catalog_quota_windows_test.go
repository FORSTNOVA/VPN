package main

import (
	"path/filepath"
	"testing"
)

func TestCatalogConfigurationQuotaPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	open := func() *app {
		b, err := newSelectedServiceStateBackend()
		if err != nil {
			t.Fatal(err)
		}
		st, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		a := &app{service: newServiceAuthority(b), store: st}
		t.Cleanup(func() { a.stopAutoMergeLoopLocked(); a.service.Close(); st.Close() })
		return a
	}
	a := open()
	data := subscriptionUserInfo{Upload: 11, Total: 42}
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "id", URL: "https://example.test/sub", UserInfo: &data, QuotaUpdatedAt: 123}})
	update := subscriptionServiceRequest{Op: "update", ID: "id", Label: "saved", HasPrefix: true, Prefix: "p", HasEnabled: true}
	if r := a.runSubscriptionService(update); r.Status != 200 {
		t.Fatalf("update: %+v", r)
	}
	a.store.Close()
	other := open()
	if r := other.runSubscriptionService(subscriptionServiceRequest{Op: "load"}); r.Status != 200 {
		t.Fatalf("load: %+v", r)
	}
	v, ok := other.findSubscriptionLocked("id")
	if !ok || v.Label != "saved" || v.Prefix != "p" || v.Enabled || v.UserInfo == nil || *v.UserInfo != data || v.QuotaUpdatedAt != 123 {
		t.Fatalf("reloaded: %+v", v)
	}
	update.Label = "write failed"
	if r := a.runSubscriptionService(update); r.Status != 500 {
		t.Fatalf("closed-store update: %+v", r)
	}
	if v, _ := a.findSubscriptionLocked("id"); v.Label != "saved" {
		t.Fatal("failed save changed the in-memory label")
	}
}
