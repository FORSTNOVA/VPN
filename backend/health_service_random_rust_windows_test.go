//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// In-memory effects only. Never creates a listener, controller, app or timer.
type differentialFlowEffect struct {
	Kind  string
	Value any
}
type differentialFlowFake struct {
	Trace                                                      []differentialFlowEffect
	Measured                                                   map[string]int
	MeasureFails, Confirm, SwitchFails, RejectFails, SaveFails bool
}

func (f *differentialFlowFake) record(k string, v any) {
	f.Trace = append(f.Trace, differentialFlowEffect{k, v})
}
func (f *differentialFlowFake) MeasureGroup(d time.Duration) (map[string]int, error) {
	f.record("measure", d)
	if f.MeasureFails {
		return nil, errors.New("synthetic measure failure")
	}
	return f.Measured, nil
}
func (f *differentialFlowFake) ConfirmTraffic() bool { f.record("confirm", nil); return f.Confirm }
func (f *differentialFlowFake) SwitchGroup(s string) error {
	f.record("switch", s)
	if f.SwitchFails && s != blockedChoice || f.RejectFails && s == blockedChoice {
		return errors.New("synthetic switch failure")
	}
	return nil
}
func (f *differentialFlowFake) SaveHealth(h nodeHealth) error {
	f.record("health", h)
	if f.SaveFails {
		return errors.New("synthetic save failure")
	}
	return nil
}
func (f *differentialFlowFake) SaveSettings(s healthServiceState) error {
	f.record("settings", differentialFlowClone(s))
	if f.SaveFails {
		return errors.New("synthetic save failure")
	}
	return nil
}
func (f *differentialFlowFake) AppendSwitchEvent(e switchEvent) error {
	f.record("event", e)
	if f.SaveFails {
		return errors.New("synthetic save failure")
	}
	return nil
}
func (f *differentialFlowFake) Logf(s string, a ...any) { f.record("log", fmt.Sprintf(s, a...)) }
func differentialFlowClone(s healthServiceState) healthServiceState {
	h := make(map[string]nodeHealth, len(s.Health))
	for k, v := range s.Health {
		h[k] = v
	}
	s.Health = h
	r := make(map[string]regionRecord, len(s.Regions))
	for k, v := range s.Regions {
		v.Sources = append([]string(nil), v.Sources...)
		r[k] = v
	}
	s.Regions = r
	return s
}
func differentialFlowCompare(t *testing.T, label string, s healthServiceState, q healthServiceRequest, script differentialFlowFake) (healthServiceState, []differentialFlowEffect) {
	t.Helper()
	if !rustHealthFlowAvailable() {
		t.Fatal("real Rust flow DLL required")
	}
	want, got := differentialFlowClone(s), differentialFlowClone(s)
	we, ge := script, script
	runGoHealthService(&want, q, &we)
	if !runRustHealthService(&got, q, &ge) {
		t.Fatalf("%s: Rust runner rejected before execution", label)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s state mismatch\nGO:%#v\nRS:%#v", label, want, got)
	}
	if !reflect.DeepEqual(we.Trace, ge.Trace) {
		t.Fatalf("%s effects mismatch\nGO:%#v\nRS:%#v", label, we.Trace, ge.Trace)
	}
	return got, ge.Trace
}
func TestMigrationHealthFlowRandomParity(t *testing.T) {
	rng := rand.New(rand.NewSource(291123))
	now := time.Date(2026, 11, 16, 12, 34, 56, 123456789, time.UTC)
	states := []string{healthHealthy, healthSuspect, healthUnhealthy, healthCooldown, healthRecovery}
	for iteration := 0; iteration < 2000; iteration++ {
		s := healthServiceState{Health: map[string]nodeHealth{}, Regions: map[string]regionRecord{}, Params: defaultHealthParams(), LockedRegion: "JP", LockedNode: "n0", SettingsLockedNode: "persisted", StoreAvailable: rng.Intn(2) == 0, ChainEnabled: rng.Intn(9) == 0, Blocked: rng.Intn(5) == 0}
		if rng.Intn(7) == 0 {
			s.LockedRegion = ""
		}
		if rng.Intn(3) == 0 {
			s.SweepNote = "previous anomaly"
		}
		if s.Blocked {
			s.BlockReason = "previous reason"
		}
		switch rng.Intn(4) {
		case 0:
			s.LastSwitch = now.Add(-time.Duration(s.Params.MinDwellSec) * time.Second)
		case 1:
			s.LastSwitch = now.Add(-time.Duration(s.Params.MinDwellSec)*time.Second + time.Nanosecond)
		case 2:
			s.LastSwitch = now.Add(-time.Hour)
		}
		q := healthServiceRequest{Kind: []string{"patrol", "failed", "unblock"}[rng.Intn(3)], Active: "n0", Delays: map[string]int{}, Now: now}
		script := differentialFlowFake{Measured: map[string]int{}, MeasureFails: rng.Intn(4) == 0, Confirm: rng.Intn(2) == 0, SwitchFails: rng.Intn(4) == 0, RejectFails: rng.Intn(4) == 0, SaveFails: rng.Intn(3) == 0}
		for i, n := 0, rng.Intn(8); i < n; i++ {
			name := fmt.Sprintf("n%d", i)
			h := nodeHealth{Name: name, State: states[rng.Intn(len(states))], ConsecutiveFailures: rng.Intn(4), ConsecutiveSuccesses: rng.Intn(4), LatencyMs: rng.Intn(4) * 20}
			switch rng.Intn(4) {
			case 0:
				h.CooldownUntil = now.Add(time.Nanosecond)
			case 1:
				h.CooldownUntil = now
			case 2:
				h.CooldownUntil = now.Add(-time.Nanosecond)
			}
			s.Health[name] = h
			r := regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now}
			if rng.Intn(3) == 0 {
				r.Country = "US"
			}
			if rng.Intn(5) == 0 {
				r.Status = regionUnverified
			}
			switch rng.Intn(4) {
			case 0:
				r.VerifiedAt = now.Add(-regionTTL)
			case 1:
				r.VerifiedAt = now.Add(-regionTTL - time.Nanosecond)
			case 2:
				r.VerifiedAt = now.Add(time.Hour)
			}
			s.Regions[name] = r
			q.Delays[name] = rng.Intn(4)*30 - 30
			script.Measured[name] = rng.Intn(4)*30 - 30
		}
		differentialFlowCompare(t, fmt.Sprintf("seed291123 case%d %s", iteration, q.Kind), s, q, script)
	}
}
func TestMigrationHealthFlowProtectedRegion(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	base := healthServiceState{Health: map[string]nodeHealth{}, Regions: map[string]regionRecord{}, Params: defaultHealthParams(), LockedRegion: "JP", LockedNode: "active", SettingsLockedNode: "active", StoreAvailable: true}
	for _, name := range []string{"active", "standby", "foreign"} {
		country := "JP"
		if name == "foreign" {
			country = "US"
		}
		base.Regions[name] = regionRecord{Name: name, Country: country, Status: regionVerified, VerifiedAt: now}
		base.Health[name] = nodeHealth{Name: name, State: healthHealthy, ConsecutiveFailures: base.Params.FailureThreshold - 1, LatencyMs: 10}
	}
	q := healthServiceRequest{Kind: "patrol", Active: "active", Delays: map[string]int{}, Now: now}
	got, trace := differentialFlowCompare(t, "allzero but passing", base, q, differentialFlowFake{Measured: map[string]int{"foreign": 1}, Confirm: true})
	if got.Blocked || got.LockedNode != "active" || got.LockedRegion != "JP" || got.SweepNote == "" {
		t.Fatal("passing tunnel lost protection")
	}
	measures, confirms := 0, 0
	for _, e := range trace {
		switch e.Kind {
		case "measure":
			measures++
		case "confirm":
			confirms++
		case "switch":
			t.Fatalf("passing tunnel switched %#v", e)
		}
	}
	if measures != 1 || confirms != 1 {
		t.Fatalf("probe cadence changed %d %d", measures, confirms)
	}
	got, trace = differentialFlowCompare(t, "exhausted region", base, q, differentialFlowFake{Measured: map[string]int{"foreign": 1}})
	if !got.Blocked || got.LockedRegion != "JP" || got.LockedNode != "active" {
		t.Fatal("exhaustion crossed region")
	}
	for _, e := range trace {
		if e.Kind == "switch" && e.Value != blockedChoice {
			t.Fatal("cross-region switch")
		}
	}
	q.Kind = "failed"
	got, trace = differentialFlowCompare(t, "API error preserves standby", base, q, differentialFlowFake{})
	if got.LockedNode != "standby" || got.Health["standby"] != base.Health["standby"] {
		t.Fatal("API error polluted standby")
	}
	for _, e := range trace {
		if e.Kind == "measure" {
			t.Fatal("cached standby caused needless probe")
		}
	}
}

func TestMigrationHealthFlowSequenceAndNames(t *testing.T) {
	// Name bytes never go through JSON; opaque Unicode/control/invalid-UTF8 names
	// and Go time locations must survive the index-based bridge unchanged.
	for caseNo, name := range []string{"node | with spaces", "节点🧪", "node\xff\x00"} {
		now := time.Date(2026, 11, 16, 12, 0, 0, 123, time.FixedZone("test+8", 8*3600))
		s := healthServiceState{Health: map[string]nodeHealth{}, Regions: map[string]regionRecord{}, Params: defaultHealthParams(), LockedRegion: "JP", LockedNode: name, SettingsLockedNode: name, StoreAvailable: true}
		s.Regions[name] = regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now}
		for tick, delay := range []int{0, 0, 0, 40, 40, 40, 0} {
			q := healthServiceRequest{Kind: "patrol", Active: name, Delays: map[string]int{name: delay}, Now: now.Add(time.Duration(tick) * time.Minute)}
			got, trace := differentialFlowCompare(t, fmt.Sprintf("name%d tick%d", caseNo, tick), s, q, differentialFlowFake{Confirm: true, Measured: map[string]int{}})
			if got.Blocked || got.LockedRegion != "JP" || got.LockedNode != name {
				t.Fatal("passing sequence lost region pin")
			}
			for _, effect := range trace {
				if effect.Kind == "switch" {
					t.Fatal("passing sequence switched away")
				}
			}
			s = got
		}
	}
}
