package main

import (
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestMigrationPathSafePassedAndBaseline(t *testing.T) {
	for _, tc := range []struct {
		state string
		code  int
		want  bool
	}{{"restricted", 403, true}, {"restricted", 429, true}, {"redirect", 302, true}, {"ok", 204, true}, {"custom", 0, true}, {"error", 200, false}, {"skipped", 204, false}, {"", 200, false}} {
		if pathPassed(siteCheckResult{State: tc.state, HTTPCode: tc.code}) != tc.want {
			t.Fatalf("pass contract %#v", tc)
		}
	}
	b := baselinePathTargets()
	if len(b) != 2 || b[0].Label != "Google" || b[1].Label != "YouTube" {
		t.Fatal("mandatory baseline changed")
	}
	b[0].Label = "mutated"
	if baselinePathTargets()[0].Label != "Google" {
		t.Fatal("baseline slice aliases presets")
	}
	extras := otherPathTargets([]pathTarget{{Label: "renamed", URL: pathCheckPresets[0].URL}, {Label: "extra", URL: "https://synthetic.invalid"}})
	if len(extras) != 1 || extras[0].Label != "extra" {
		t.Fatal("extras use URL not label")
	}
}
func TestMigrationPathSafeRetryBoundary(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 5, 6, 123, time.FixedZone("X", 3600))
	for _, delta := range []time.Duration{-1, 0, 1} {
		a := &app{}
		testEditPath(a, func(p *servicePathState) { p.Config.Enabled = true })
		testEditPath(a, func(p *servicePathState) {
			p.Status = pathStatus{RetryAt: at.UTC().Format(time.RFC3339Nano), RetryMode: "targets"}
		})
		got := a.takeDuePathRetry(at.Add(delta))
		if delta < 0 {
			if got != "" || a.pathStatusLocked().RetryAt == "" {
				t.Fatal("early retry consumed")
			}
		} else if got != "targets" || a.pathStatusLocked().RetryAt != "" || a.pathStatusLocked().RetryMode != "" || a.pathStatusLocked().Phase != "scanning" {
			t.Fatal("due equality contract")
		}
	}
}
func TestMigrationPathSafeHTTP(t *testing.T) {
	a := &app{}
	testEditPath(a, func(p *servicePathState) { p.Config = defaultPathChecks() })
	testEditPath(a, func(p *servicePathState) {
		p.Status = pathStatus{Demanded: true, Alert: "old", RetryAt: "future", RetryMode: "scan"}
	})
	w := httptest.NewRecorder()
	a.cancelPathRecovery(w, httptest.NewRequest("POST", "/path/cancel", nil))
	if w.Code != 200 || a.pathStatusLocked().Demanded || a.pathStatusLocked().Alert != "" || a.pathStatusLocked().RetryAt != "" || a.pathStatusLocked().Phase != "baseline" {
		t.Fatal("cancel contract")
	}
	w = httptest.NewRecorder()
	a.runPathChecks(w, httptest.NewRequest("POST", "/path/run", nil))
	if w.Code != 409 {
		t.Fatal("stopped should reject before spawn")
	}
	before := a.pathStatusLocked()
	testEditPath(a, func(p *servicePathState) { p.Busy = true })
	w = httptest.NewRecorder()
	a.pathChecks(w, httptest.NewRequest("GET", "/path", nil))
	if w.Code != 200 || !reflect.DeepEqual(before, a.pathStatusLocked()) {
		t.Fatal("status must not mutate stored Running")
	}
	if !a.mu.TryLock() {
		t.Fatal("HTTP lock leaked")
	}
	a.mu.Unlock()
}
