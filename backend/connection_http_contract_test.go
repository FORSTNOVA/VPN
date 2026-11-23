package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Decoder-only rejection deliberately never reaches kernel, proxy, file or timer ports.
func TestMigrationConnectionHTTPRejectsBeforeEffects(t *testing.T) {
	for _, body := range []string{`{`, `null`, `{}`, `{"name":null}`, `{"name":2}`, `{"name":" \n "}`, `{"name":"x","unknown":true}`} {
		a := &app{}
		w := httptest.NewRecorder()
		a.selectNode(w, httptest.NewRequest("POST", "/api/select", strings.NewReader(body)))
		if w.Code != 400 || w.Body.String() != "invalid node selection\n" {
			t.Fatalf("body=%q status=%d response=%q", body, w.Code, w.Body.String())
		}
		if !a.mu.TryLock() {
			t.Fatal("decoder rejection leaked app lock")
		}
		a.mu.Unlock()
	}
}

// Only snapshot/lock/release are invoked on the real adapter. All platform ports
// are intentionally excluded, including stop-kernel on an apparently empty app.
func TestMigrationConnectionHTTPLockOwnership(t *testing.T) {
	a := &app{}
	a.mu.Lock()
	borrowed := &appConnectionEffects{a: a, locked: true}
	if borrowed.Call(connectionEffect{Kind: "snapshot"}).State == nil {
		t.Fatal("missing borrowed snapshot")
	}
	borrowed.Release()
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("Release unlocked caller-owned mutex")
	}
	a.mu.Unlock()
	owned := &appConnectionEffects{a: a}
	owned.Call(connectionEffect{Kind: "lock"})
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("lock effect did not acquire mutex")
	}
	owned.Release()
	owned.Release()
	if !a.mu.TryLock() {
		t.Fatal("owned mutex leaked")
	}
	a.mu.Unlock()
}
