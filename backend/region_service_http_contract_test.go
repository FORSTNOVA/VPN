package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMigrationRegionSafeHTTPValidation(t *testing.T) {
	for _, tc := range []struct {
		body   string
		chain  bool
		status int
	}{
		{`{`, false, 400}, {`{"country":3}`, false, 400}, {`{"country":"US","other":1}`, false, 400}, {`{"country":"USA"}`, false, 400}, {`{"country":"U1"}`, false, 400}, {`{"country":" us "}`, true, 409}, {`{"country":"US"}`, false, 400},
	} {
		a := &app{}
		a.settings.Chain.Enabled = tc.chain
		w := httptest.NewRecorder()
		a.lockRegion(w, httptest.NewRequest("POST", "/api/regions/lock", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("body=%s status=%d", tc.body, w.Code)
		}
		if !a.mu.TryLock() {
			t.Fatal("handler leaked mutex")
		}
		a.mu.Unlock()
	}
}
func TestMigrationRegionSafeHTTPReportEmptyShape(t *testing.T) {
	a := &app{}
	w := httptest.NewRecorder()
	a.regionReport(w, httptest.NewRequest("GET", "/api/regions", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nodes", "job", "lockedRegion", "pool", "health", "ttlHours"} {
		if _, ok := got[key]; !ok {
			t.Fatal("missing field", key)
		}
	}
	if string(got["nodes"]) != "[]" || string(got["pool"]) != "[]" || string(got["health"]) != "{}" {
		t.Fatalf("nil/empty DTO drift %s", w.Body.String())
	}
	if !a.mu.TryLock() {
		t.Fatal("report leaked mutex")
	}
	a.mu.Unlock()
}
