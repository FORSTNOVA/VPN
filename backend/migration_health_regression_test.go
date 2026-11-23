package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// No socket exists: every HTTP attempt is intercepted here, and unknown
// requests fail the test rather than falling through to the real transport.
type migrationHealthTransport func(*http.Request) (*http.Response, error)

func (f migrationHealthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func migrationHealthApp(t *testing.T) (*app, *[]string, *int) {
	t.Helper()
	calls := []string{}
	confirms := 0
	previous := http.DefaultTransport
	http.DefaultTransport = migrationHealthTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "127.0.0.1:0" {
			t.Fatalf("unexpected destination %s", r.URL.Host)
		}
		body := ""
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			body = string(b)
		}
		calls = append(calls, r.Method+" "+r.URL.Path+" "+body)
		allowed := (r.Method == "GET" && r.URL.Path == "/group/SmartVPN/delay") || (r.Method == "PUT" && r.URL.Path == "/proxies/SmartVPN" && body == `{"name":"REJECT"}`)
		if !allowed {
			t.Fatalf("unexpected mocked request: %s", calls[len(calls)-1])
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), ContentLength: 2, Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	a := testHealthApp(testControlApp(&app{home: t.TempDir()}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
		a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "active"})
	}), func(p *healthDomainProjection) {
		p.Params = defaultHealthParams()
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
	})
	testEditSettings(a, func(s *settings) { s.LockedRegion = "JP" })
	testEditSettings(a, func(s *settings) { s.LockedNode = "active" })
	a.confirmProbe = func() bool { confirms++; return true }
	return a, &calls, &confirms
}

func TestMigrationHealthBatchFailureKeepsPassingTunnel(t *testing.T) {
	a, calls, confirms := migrationHealthApp(t)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}
	// A faster other-country node must never escape the verified region filter.
	a.writeRegionRecordLocked("foreign", regionRecord{Name: "foreign", Country: "US", Status: regionVerified, VerifiedAt: now})
	a.writeHealthRecordLocked("foreign", nodeHealth{Name: "foreign", State: healthHealthy, LatencyMs: 1})
	params := a.healthDomainLocked().Params
	rounds := params.FailureThreshold + 2
	for i := 0; i < rounds; i++ {
		a.applyPatrolLocked("active", map[string]int{}, now.Add(time.Duration(i*params.PatrolIntervalSec)*time.Second))
		if a.healthDomainLocked().Blocked || a.controlLocked().RuntimeLockedRegion != "JP" || a.controlLocked().RuntimeLockedNode != "active" || a.settingsLocked().LockedNode != "active" {
			t.Fatal("passing tunnel/region pin changed")
		}
	}
	if a.healthDomainLocked().Health["active"].State != healthUnhealthy || a.healthDomainLocked().Health["standby"].State != healthUnhealthy {
		t.Fatal("bulk verdict not recorded")
	}
	if a.healthDomainLocked().SweepNote == "" || *confirms != rounds-params.FailureThreshold+1 {
		t.Fatalf("independent confirmation missing: %d", *confirms)
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "PUT ") {
			t.Fatal("passing tunnel must not be switched or blocked")
		}
	}
	if a.healthDomainLocked().Params != params || !a.healthDomainLocked().LastSwitch.IsZero() {
		t.Fatal("anomaly must not alter cadence or dwell baseline")
	}
	// A successful sweep clears only the anomaly; it does not cross the region.
	a.applyPatrolLocked("active", map[string]int{"active": 100}, now.Add(10*time.Minute))
	if a.healthDomainLocked().SweepNote != "" || a.controlLocked().RuntimeLockedRegion != "JP" {
		t.Fatal("recovery verdict incorrect")
	}
}

func TestMigrationHealthExhaustedRegionFailsClosed(t *testing.T) {
	a, calls, _ := migrationHealthApp(t)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	a.writeRegionRecordLocked("active", regionRecord{Name: "active", Country: "JP", Status: regionVerified, VerifiedAt: now})
	a.writeRegionRecordLocked("foreign", regionRecord{Name: "foreign", Country: "US", Status: regionVerified, VerifiedAt: now})
	a.writeHealthRecordLocked("foreign", nodeHealth{Name: "foreign", State: healthHealthy, LatencyMs: 1})
	a.confirmProbe = func() bool { return false }
	for i := 0; i < a.healthDomainLocked().Params.FailureThreshold; i++ {
		a.applyPatrolLocked("active", map[string]int{}, now.Add(time.Duration(i)*time.Minute))
	}
	if !a.healthDomainLocked().Blocked || a.healthDomainLocked().BlockReason == "" || a.controlLocked().RuntimeLockedRegion != "JP" || a.controlLocked().RuntimeLockedNode != "active" {
		t.Fatal("region exhaustion must fail closed, not cross regions")
	}
	puts := 0
	for _, call := range *calls {
		if strings.HasPrefix(call, "PUT ") {
			puts++
			if call != `PUT /proxies/SmartVPN {"name":"REJECT"}` {
				t.Fatal(call)
			}
		}
	}
	if puts != 1 {
		t.Fatalf("expected single mocked REJECT, got %d", puts)
	}
}

func TestMigrationHealthAndPathRetryCadence(t *testing.T) {
	params := defaultHealthParams()
	if params.PatrolIntervalSec != 60 {
		t.Fatal("bulk probe baseline no longer 60 seconds")
	}
	if (healthParams{PatrolIntervalSec: 1}).normalized().PatrolIntervalSec != 10 || (healthParams{PatrolIntervalSec: 1000}).normalized().PatrolIntervalSec != 600 {
		t.Fatal("patrol interval limits changed")
	}
	a := testHealthApp(&app{home: t.TempDir()}, func(p *healthDomainProjection) { p.Params = params; p.LastSwitch = time.Unix(100, 0) })
	if a.dwellElapsedLocked(a.healthDomainLocked().LastSwitch.Add(time.Duration(params.MinDwellSec)*time.Second - time.Nanosecond)) {
		t.Fatal("dwell released early")
	}
	if !a.dwellElapsedLocked(a.healthDomainLocked().LastSwitch.Add(time.Duration(params.MinDwellSec) * time.Second)) {
		t.Fatal("dwell boundary changed")
	}
	testEditPath(a, func(p *servicePathState) { p.Config.Enabled = true })
	before := time.Now()
	a.schedulePathRetryLocked("same-region")
	after := time.Now()
	due, err := time.Parse(time.RFC3339Nano, a.pathStatusLocked().RetryAt)
	if err != nil {
		t.Fatal(err)
	}
	if due.Before(before.Add(pathRetryDelay)) || due.After(after.Add(pathRetryDelay)) {
		t.Fatal("retry schedule changed")
	}
	if a.takeDuePathRetry(due.Add(-time.Nanosecond)) != "" {
		t.Fatal("retry released early")
	}
	testEditPath(a, func(p *servicePathState) { p.Busy = true })
	if a.takeDuePathRetry(due) != "" || a.pathStatusLocked().RetryAt == "" {
		t.Fatal("busy round lost retry")
	}
	testEditPath(a, func(p *servicePathState) { p.Busy = false })
	if a.takeDuePathRetry(due) != "same-region" || a.takeDuePathRetry(due) != "" || a.pathStatusLocked().Phase != "scanning" {
		t.Fatal("due retry must be consumed once")
	}
}
