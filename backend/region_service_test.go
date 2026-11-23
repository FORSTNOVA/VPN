package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func regionFixture() (*app, *appRegionEffects, time.Time) {
	now := time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)
	a := testHealthApp(&app{}, func(p *healthDomainProjection) {
		p.Regions = map[string]regionRecord{"a": {Name: "record-name-not-key", Country: "US", Status: regionVerified, VerifiedAt: now.Add(-time.Hour), Sources: []string{"synthetic"}}}
		p.Health = map[string]nodeHealth{}
		p.Job = &regionJob{Done: 2}
		p.Blocked = true
		p.BlockReason = "old-block"
	})
	fx := newAppRegionEffects(a)
	fx.now = func() time.Time { return now }
	fx.save = func() error { return nil }
	fx.running = func() bool { return false }
	fx.mihomo = func(string, string, any) (map[string]any, error) { return map[string]any{"now": "other"}, nil }
	fx.block = func(string, time.Time, string) { panic("unexpected block") }
	fx.event = func(switchEvent) {}
	fx.logger = func(string) {}
	return a, fx, now
}
func TestRegionServiceClearInChainKeepsBlock(t *testing.T) {
	a, fx, _ := regionFixture()
	a.settings.Chain.Enabled = true
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "US"})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "a"})
	testEditSettings(a, func(s *settings) { s.LockedRegion = "US" })
	testEditSettings(a, func(s *settings) { s.LockedNode = "a" })
	times, events := 0, 0
	clock := fx.now
	fx.now = func() time.Time { times++; return clock() }
	fx.running = func() bool { t.Fatal("clear checked running"); return true }
	fx.event = func(switchEvent) { events++ }
	r := runGoRegionService(regionServiceRequest{Op: "lock"}, fx)
	if r.Status != 200 || a.controlLocked().RuntimeLockedRegion != "" || a.controlLocked().RuntimeLockedNode != "" || !a.healthDomainLocked().Blocked || a.healthDomainLocked().BlockReason != "old-block" || times != 2 || events != 1 {
		t.Fatal(r, a, times, events)
	}
	if !a.mu.TryLock() {
		t.Fatal("owned lock leak")
	}
	a.mu.Unlock()
}
func TestRegionServiceBoundaryAndCooldown(t *testing.T) {
	a, fx, now := regionFixture()
	record := a.healthDomainLocked().Regions["a"]
	record.VerifiedAt = now.Add(-regionTTL)
	a.writeRegionRecordLocked("a", record)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "US"})
	r := runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 400 {
		t.Fatal("lock TTL equality accepted", r)
	}
	r = runGoRegionService(regionServiceRequest{Op: "report"}, fx)
	nodes := r.Data["nodes"].([]regionReportNode)
	if nodes[0].Stale || !reflect.DeepEqual(r.Data["pool"], []string{"a"}) {
		t.Fatal("report TTL equality excluded", r)
	}
	record.VerifiedAt = now.Add(-time.Hour)
	a.writeRegionRecordLocked("a", record)
	a.writeHealthRecordLocked("a", nodeHealth{CooldownUntil: now.Add(time.Minute)})
	fx.running = func() bool { return true }
	blocks := 0
	fx.block = func(_ string, at time.Time, message string) {
		blocks++
		testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = true })
		testEditHealth(a, func(p *healthDomainProjection) { p.BlockReason = message })
		testEditHealth(a, func(p *healthDomainProjection) { p.SweepNote = "partial-health" })
		testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = at })
	}
	r = runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 200 || blocks != 1 || a.controlLocked().RuntimeLockedNode != "" || a.healthDomainLocked().SweepNote != "partial-health" || a.healthDomainLocked().LastSwitch != now || r.Data["pool"] != 0 {
		t.Fatal(r, a, blocks)
	}
}
func TestRegionServiceIndependentClocksAndPartialState(t *testing.T) {
	a, fx, now := regionFixture()
	clock := 0
	fx.now = func() time.Time {
		clock++
		if clock == 1 {
			return now
		}
		return now.Add(2 * regionTTL)
	}
	fx.running = func() bool { return true }
	blocks := 0
	fx.block = func(string, time.Time, string) {
		blocks++
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "CA"})
		testEditSettings(a, func(s *settings) { s.LockedRegion = "CA" })
		a.writeHealthRecordLocked("new", nodeHealth{Name: "new", LatencyMs: 7})
		testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = true })
	}
	r := runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 200 || blocks != 1 || clock != 5 || r.Data["lockedRegion"] != "CA" || a.healthDomainLocked().Health["new"].LatencyMs != 7 {
		t.Fatal(r, clock, blocks)
	}
}
func TestRegionServiceSaveFailureAndPinError(t *testing.T) {
	a, fx, _ := regionFixture()
	fx.save = func() error { return errors.New("save") }
	fx.running = func() bool { t.Fatal("running after save failure"); return false }
	r := runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 500 || a.controlLocked().RuntimeLockedRegion != "US" || a.controlLocked().RuntimeLockedNode != "a" {
		t.Fatal(r, a)
	}
	a, fx, _ = regionFixture()
	fx.running = func() bool { return true }
	pins, logs, events := 0, 0, 0
	fx.mihomo = func(method, path string, body any) (map[string]any, error) {
		if method == "PUT" {
			pins++
			return nil, errors.New("pin failed")
		}
		return map[string]any{"now": "other"}, nil
	}
	fx.logger = func(string) { logs++ }
	fx.event = func(switchEvent) { events++ }
	r = runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 200 || pins != 1 || logs != 1 || events != 1 || a.controlLocked().RuntimeLockedNode != "a" {
		t.Fatal(r, pins, logs, events)
	}
}
func TestRegionServiceReportSnapshotDoesNotAlias(t *testing.T) {
	a, fx, _ := regionFixture()
	a.writeRegionRecordLocked("z", regionRecord{Name: "different", Status: "unknown"})
	a.writeHealthRecordLocked("orphan", nodeHealth{State: healthUnhealthy})
	r := runGoRegionService(regionServiceRequest{Op: "report"}, fx)
	testEditHealth(a, func(p *healthDomainProjection) { p.Regions["a"].Sources[0] = "mutated" })
	testEditHealth(a, func(p *healthDomainProjection) { p.Job.Done = 999 })
	a.writeHealthRecordLocked("orphan", nodeHealth{State: "mutated"})
	nodes := r.Data["nodes"].([]regionReportNode)
	if len(nodes) != 2 || nodes[0].Name != "a" || nodes[1].Name != "z" || nodes[0].Sources[0] != "synthetic" || r.Data["job"].(*regionJob).Done != 2 || r.Data["health"].(map[string]regionReportHealth)["orphan"].State != healthUnhealthy {
		t.Fatal(r)
	}
	testEditHealth(a, func(p *healthDomainProjection) { p.Regions = nil })
	testEditHealth(a, func(p *healthDomainProjection) { p.Health = nil })
	testEditHealth(a, func(p *healthDomainProjection) { p.Job = nil })
	r = runGoRegionService(regionServiceRequest{Op: "report"}, fx)
	body, err := json.Marshal(r.Data)
	if err != nil || !strings.Contains(string(body), `"nodes":[]`) || !strings.Contains(string(body), `"pool":[]`) || !strings.Contains(string(body), `"health":{}`) {
		t.Fatal(string(body), err)
	}
}
func TestRegionServiceBestUnknownFallbackAndPanicRelease(t *testing.T) {
	a, fx, now := regionFixture()
	a.writeRegionRecordLocked("z", regionRecord{Country: "US", Status: regionVerified, VerifiedAt: now})
	a.writeHealthRecordLocked("a", nodeHealth{State: healthUnhealthy})
	a.writeHealthRecordLocked("z", nodeHealth{LatencyMs: 0})
	r := runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	if r.Status != 200 || a.controlLocked().RuntimeLockedNode != "a" {
		t.Fatal("fallback must include unhealthy unknown-latency", r, a.controlLocked().RuntimeLockedNode)
	}
	fx.save = func() error { panic("injected") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing panic")
			}
		}()
		runGoRegionService(regionServiceRequest{Op: "lock", Country: "US"}, fx)
	}()
	if !a.mu.TryLock() {
		t.Fatal("panic lock leaked")
	}
	a.mu.Unlock()
}
