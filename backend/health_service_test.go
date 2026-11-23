package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type healthServiceMemoryEffects struct {
	Trace        []string
	Fresh        map[string]int
	MeasureError error
	Confirm      bool
	Fail         map[string]bool
	Saved        []healthServiceState
	Events       []switchEvent
}

func (f *healthServiceMemoryEffects) result(op string) error {
	f.Trace = append(f.Trace, op)
	if f.Fail[op] {
		return errors.New("synthetic " + op)
	}
	return nil
}
func (f *healthServiceMemoryEffects) MeasureGroup(d time.Duration) (map[string]int, error) {
	f.Trace = append(f.Trace, fmt.Sprintf("measure:%d", d.Milliseconds()))
	return f.Fresh, f.MeasureError
}
func (f *healthServiceMemoryEffects) ConfirmTraffic() bool {
	f.Trace = append(f.Trace, "confirm")
	return f.Confirm
}
func (f *healthServiceMemoryEffects) SwitchGroup(target string) error {
	return f.result("switch:" + target)
}
func (f *healthServiceMemoryEffects) SaveHealth(h nodeHealth) error {
	return f.result("health:" + h.Name + ":" + h.State)
}
func (f *healthServiceMemoryEffects) SaveSettings(s healthServiceState) error {
	f.Saved = append(f.Saved, s)
	return f.result("settings:" + s.SettingsLockedNode)
}
func (f *healthServiceMemoryEffects) AppendSwitchEvent(e switchEvent) error {
	f.Events = append(f.Events, e)
	return f.result("event:" + e.Trigger)
}
func (f *healthServiceMemoryEffects) Logf(s string, v ...any) {
	f.Trace = append(f.Trace, "log:"+fmt.Sprintf(s, v...))
}
func healthServiceFixture() (healthServiceState, time.Time) {
	now := time.Unix(1700000000, 0)
	p := defaultHealthParams()
	p.FailureThreshold = 1
	p.MinDwellSec = 0
	return healthServiceState{Health: map[string]nodeHealth{"a": {Name: "a", State: healthHealthy}, "b": {Name: "b", State: healthHealthy, LatencyMs: 10}}, Regions: map[string]regionRecord{"a": {Name: "a", Country: "JP", Status: regionVerified, VerifiedAt: now}, "b": {Name: "b", Country: "JP", Status: regionVerified, VerifiedAt: now}}, Params: p, LockedRegion: "JP", LockedNode: "a", SettingsLockedNode: "a", StoreAvailable: true}, now
}
func healthTraceWithoutLogs(in []string) []string {
	var out []string
	for _, v := range in {
		if !strings.HasPrefix(v, "log:") {
			out = append(out, v)
		}
	}
	return out
}
func TestHealthServiceFailoverContract(t *testing.T) {
	s, now := healthServiceFixture()
	fx := &healthServiceMemoryEffects{Fail: map[string]bool{"health:a:UNHEALTHY": true, "health:a:COOLDOWN": true, "settings:a": true, "event:unhealthy": true}}
	runGoHealthService(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx)
	want := []string{"health:a:UNHEALTHY", "switch:b", "health:a:COOLDOWN", "settings:a", "event:unhealthy"}
	if got := healthTraceWithoutLogs(fx.Trace); !reflect.DeepEqual(got, want) {
		t.Fatalf("trace=%q want=%q", got, want)
	}
	if s.LockedNode != "b" || s.SettingsLockedNode != "b" || s.Blocked || s.LastSwitch != now || s.Health["a"].State != healthCooldown {
		t.Fatal("final state", s)
	}
	if len(fx.Saved) != 1 || fx.Saved[0].SettingsLockedNode != "a" {
		t.Fatal("failover must preserve pre-pin save ordering")
	}
}
func TestHealthServiceBlockContract(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		confirm, rejectFail, targetFail bool
	}{{"live", true, false, false}, {"block", false, false, false}, {"reject failed", false, true, false}, {"target failed live", true, false, true}, {"target failed blocked", false, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, now := healthServiceFixture()
			if !tc.targetFail {
				delete(s.Regions, "b")
			}
			fx := &healthServiceMemoryEffects{Confirm: tc.confirm, Fail: map[string]bool{"switch:b": tc.targetFail, "switch:REJECT": tc.rejectFail}}
			runGoHealthService(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx)
			trace := strings.Join(healthTraceWithoutLogs(fx.Trace), "|")
			if strings.Count(trace, "confirm") != 1 {
				t.Fatal("confirmation count", trace)
			}
			if tc.confirm {
				if s.Blocked || s.LastSwitch != (time.Time{}) || s.SweepNote == "" || strings.Contains(trace, "switch:REJECT") {
					t.Fatal("false block", trace)
				}
			} else if tc.rejectFail {
				if s.Blocked || !s.LastSwitch.IsZero() || len(fx.Events) != 0 {
					t.Fatal("failed REJECT committed")
				}
			} else {
				if !s.Blocked || s.LastSwitch != now || len(fx.Events) != 1 || fx.Events[0].Trigger != "region_exhausted" {
					t.Fatal("block missing", trace)
				}
			}
		})
	}
}
func TestHealthServiceGatesAndFreshMeasurement(t *testing.T) {
	for _, gate := range []string{"chain", "dwell", "no-region"} {
		s, now := healthServiceFixture()
		switch gate {
		case "chain":
			s.ChainEnabled = true
		case "dwell":
			s.Params.MinDwellSec = 100
			s.LastSwitch = now
		case "no-region":
			s.LockedRegion = ""
		}
		fx := &healthServiceMemoryEffects{}
		runGoHealthService(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx)
		if got := healthTraceWithoutLogs(fx.Trace); len(got) != 1 || got[0] != "health:a:UNHEALTHY" {
			t.Fatal(gate, got)
		}
	}
	for _, failed := range []bool{false, true} {
		s, now := healthServiceFixture()
		h := s.Health["b"]
		h.LatencyMs = 0
		s.Health["b"] = h
		fx := &healthServiceMemoryEffects{Fresh: map[string]int{"b": 30}, Confirm: true}
		if failed {
			fx.MeasureError = errors.New("synthetic measurement")
		}
		runGoHealthService(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx)
		trace := strings.Join(fx.Trace, "|")
		if strings.Count(trace, "measure:") != 1 {
			t.Fatal("fresh measurement count", trace)
		}
		if !failed && s.LockedNode != "b" {
			t.Fatal("fresh target not chosen")
		}
		if failed && s.LockedNode != "a" {
			t.Fatal("measurement failure switched")
		}
	}
}
func TestHealthServiceUnblockAndPatrol(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, now := healthServiceFixture()
		s.Blocked = true
		s.BlockReason = "synthetic"
		s.SweepNote = "old"
		fx := &healthServiceMemoryEffects{Fail: map[string]bool{"switch:b": fail, "settings:b": true, "event:region_recovered": true}}
		runGoHealthService(&s, healthServiceRequest{Kind: "unblock", Now: now}, fx)
		if fail {
			if !s.Blocked || s.BlockReason != "synthetic" || len(fx.Saved) != 0 {
				t.Fatal("failed recovery changed state")
			}
		} else {
			if s.Blocked || s.SweepNote != "" || s.LockedNode != "b" || len(fx.Saved) != 1 || fx.Saved[0].SettingsLockedNode != "b" {
				t.Fatal("recover commit/save order")
			}
		}
	}
	s, now := healthServiceFixture()
	s.SweepNote = "old"
	fx := &healthServiceMemoryEffects{}
	runGoHealthService(&s, healthServiceRequest{Kind: "patrol", Active: "a", Delays: map[string]int{"a": 20, "b": 10}, Now: now}, fx)
	if s.SweepNote != "" || len(healthTraceWithoutLogs(fx.Trace)) != 2 {
		t.Fatal("patrol de-dup/clear", fx.Trace)
	}
	s, now = healthServiceFixture()
	s.StoreAvailable = false
	fx = &healthServiceMemoryEffects{}
	runGoHealthService(&s, healthServiceRequest{Kind: "patrol", Active: "a", Delays: map[string]int{"a": 20, "b": 10}, Now: now}, fx)
	if len(healthTraceWithoutLogs(fx.Trace)) != 0 {
		t.Fatal("nil store wrote")
	}
}

// These app adapters are exercised only with states that cannot request network
// effects: chain/no-region patrol and empty-candidate recovery.
func TestHealthServiceDefaultEntrypoints(t *testing.T) {
	now := time.Unix(1700000000, 0)
	a := testHealthApp(&app{}, func(p *healthDomainProjection) {
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
		p.Params = defaultHealthParams()
		p.SweepNote = "old"
	})
	a.settings.Chain.Enabled = true
	a.applyPatrolLocked("synthetic", map[string]int{"synthetic": 12}, now)
	if a.healthDomainLocked().Health["synthetic"].State != healthHealthy || a.healthDomainLocked().SweepNote != "" {
		t.Fatal("patrol adapter")
	}
	a.applyFailedSweepLocked("synthetic", now.Add(time.Second))
	if a.healthDomainLocked().Health["synthetic"].ConsecutiveFailures != 1 {
		t.Fatal("failed adapter")
	}
	testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = true })
	testEditHealth(a, func(p *healthDomainProjection) { p.BlockReason = "synthetic" })
	a.tryUnblockLocked(now)
	if !a.healthDomainLocked().Blocked || a.healthDomainLocked().BlockReason != "synthetic" {
		t.Fatal("empty-pool unblock adapter")
	}
}
