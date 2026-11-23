//go:build windows && amd64 && rustcore

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

type regionDiffSpec struct {
	State                               regionServiceState
	Times                               []time.Time
	Running, SaveFail, PutFail, GetFail bool
	Active, Nested                      any
	BlockProtected                      bool
}
type regionDiffEvent struct {
	Effect regionEffect
	State  *regionServiceState
	Owned  bool
}
type regionDiffFX struct {
	Base                     *appRegionEffects
	Trace                    []regionDiffEvent
	Calls                    map[string]int
	NowCalls, Blocks, Events int
	Spec                     regionDiffSpec
}

func regionDiffBase() regionDiffSpec {
	now := time.Date(2041, 2, 3, 4, 5, 6, 999999999, time.FixedZone("UTC+08", 8*3600))
	return regionDiffSpec{State: regionServiceState{LockedRegion: "US", LockedNode: "old", Blocked: true, BlockReason: "old-block", Regions: map[string]regionRecord{"a": {Name: "different-record-name", Country: "US", Status: regionVerified, VerifiedAt: now.Add(-time.Hour), ExitIP: "192.0.2.1", Sources: []string{"source-a", "source-b"}, Detail: "detail"}, "b": {Name: "b", Country: "US", Status: regionVerified, VerifiedAt: now.Add(-time.Hour), Sources: []string{}}}, Health: map[string]nodeHealth{"a": {Name: "a", State: healthHealthy, LatencyMs: 20, ConsecutiveFailures: 3, UpdatedAt: now.Add(-time.Minute)}, "b": {Name: "b", State: healthUnhealthy, LatencyMs: 10}}, Job: regionJob{Running: true, Total: 10, Done: 4, Verified: 2, Skipped: 1, Failed: 1, StartedAt: "verbatim-start", FinishedAt: "verbatim-end", LastError: "job-error"}}, Times: []time.Time{now}, Running: true, Active: autoGroup, Nested: "off-region"}
}
func regionDiffNew(s regionDiffSpec) *regionDiffFX {
	state := cloneRegionServiceState(&s.State)
	a := testHealthApp(testControlApp(&app{}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: state.LockedRegion})
		a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: state.LockedNode})
	}), func(p *healthDomainProjection) {
		p.Blocked = state.Blocked
		p.BlockReason = state.BlockReason
		p.SweepNote = state.SweepNote
		p.LastSwitch = state.LastSwitch
		p.Regions = state.Regions
		p.Health = state.Health
		p.Job = &state.Job
	})
	a.settings.Chain.Enabled = state.Chain
	testEditSettings(a, func(s *settings) { s.LockedRegion = state.LockedRegion })
	testEditSettings(a, func(s *settings) { s.LockedNode = state.LockedNode })
	f := &regionDiffFX{Base: newAppRegionEffects(a), Calls: map[string]int{}, Spec: s}
	f.Base.now = func() time.Time {
		i := f.NowCalls
		f.NowCalls++
		if i >= len(s.Times) {
			i = len(s.Times) - 1
		}
		return s.Times[i]
	}
	f.Base.save = func() error {
		if s.SaveFail {
			return errors.New("synthetic save")
		}
		return nil
	}
	f.Base.running = func() bool { return s.Running }
	f.Base.mihomo = func(method, path string, _ any) (map[string]any, error) {
		if method == "PUT" {
			if s.PutFail {
				return nil, errors.New("synthetic put")
			}
			return nil, nil
		}
		if s.GetFail {
			return nil, errors.New("synthetic get")
		}
		v := s.Nested
		if path == "/proxies/SmartVPN" {
			v = s.Active
		}
		return map[string]any{"now": v}, nil
	}
	// Entire mutating health sub-usecase is replaced. Never invoke real blockLocked,
	// traffic confirmation, controller REJECT or store. Return its mutable-state outcomes.
	f.Base.block = func(_ string, at time.Time, reason string) {
		f.Blocks++
		testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = at })
		testEditHealth(a, func(p *healthDomainProjection) { p.SweepNote = "synthetic block result" })
		if s.BlockProtected {
			testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = false })
			testEditHealth(a, func(p *healthDomainProjection) { p.BlockReason = "" })
			if a.healthDomainLocked().Health == nil {
				testEditHealth(a, func(p *healthDomainProjection) { p.Health = map[string]nodeHealth{} })
			}
			h := a.healthDomainLocked().Health["a"]
			h.State = healthHealthy
			h.CooldownUntil = time.Time{}
			a.writeHealthRecordLocked("a", h)
		} else {
			testEditHealth(a, func(p *healthDomainProjection) { p.Blocked = true })
			testEditHealth(a, func(p *healthDomainProjection) { p.BlockReason = reason })
		}
	}
	f.Base.event = func(switchEvent) { f.Events++ }
	f.Base.logger = func(string) {}
	return f
}
func (f *regionDiffFX) Call(e regionEffect) regionReply {
	f.Calls[e.Kind]++
	r := f.Base.Call(e)
	f.Trace = append(f.Trace, regionDiffEvent{e, regionStateFromApp(f.Base.a), f.Base.owned})
	return r
}
func (f *regionDiffFX) Release() {
	owned := f.Base.owned
	f.Base.Release()
	if owned {
		f.Trace = append(f.Trace, regionDiffEvent{regionEffect{Kind: "release"}, regionStateFromApp(f.Base.a), false})
	}
}
func regionDiffCompare(t *testing.T, q regionServiceRequest, s regionDiffSpec) {
	t.Helper()
	if !rustRegionFlowAvailable() {
		t.Fatal("actual region DLL required")
	}
	g, r := regionDiffNew(s), regionDiffNew(s)
	gr := runGoRegionService(q, g)
	before := regionFlowRecoveries.Load()
	rr, handled := runRustRegionService(q, r)
	if !handled {
		t.Fatal("unexpected Go fallback")
	}
	if regionFlowRecoveries.Load() != before {
		t.Fatalf("recovery=%v req=%+v state=%+v", regionFlowRecoveryReason.Load(), q, s.State)
	}
	if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || !reflect.DeepEqual(regionStateFromApp(g.Base.a), regionStateFromApp(r.Base.a)) || g.NowCalls != r.NowCalls || g.Blocks != r.Blocks || g.Events != r.Events {
		t.Fatalf("request=%+v\nGo=%+v\nRust=%+v\nGoTrace=%+v\nRustTrace=%+v", q, gr, rr, g.Trace, r.Trace)
	}
	if !r.Base.a.mu.TryLock() {
		t.Fatal("region workflow leaked lock")
	}
	r.Base.a.mu.Unlock()
	if q.Op == "report" {
		if r.NowCalls != 1 || len(r.Trace) != 3 {
			t.Fatal("report must snapshot using one clock and no sideeffect")
		}
		gb, _ := json.Marshal(gr.Data)
		rb, _ := json.Marshal(rr.Data)
		if string(gb) != string(rb) {
			t.Fatal("report JSON mismatch")
		}
		saved := regionStateFromApp(r.Base.a)
		nodes := rr.Data["nodes"].([]regionReportNode)
		for i := range nodes {
			if len(nodes[i].Sources) > 0 {
				nodes[i].Sources[0] = "changed-output"
			}
		}
		rr.Data["job"].(*regionJob).Total = -123
		if p := rr.Data["pool"].([]string); len(p) > 0 {
			p[0] = "changed-pool"
		}
		if !reflect.DeepEqual(saved, regionStateFromApp(r.Base.a)) {
			t.Fatal("DTO aliases live application state")
		}
	}
}
func TestMigrationRegionActualMatrix(t *testing.T) {
	for _, op := range []string{"lock", "report"} {
		for _, country := range []string{"", "US", "JP", "invalid"} {
			for mask := 0; mask < 32; mask++ {
				t.Run(fmt.Sprintf("%s/%s/%d", op, country, mask), func(t *testing.T) {
					s := regionDiffBase()
					s.State.Chain = mask&1 != 0
					s.Running = mask&2 != 0
					s.SaveFail = mask&4 != 0
					s.PutFail = mask&8 != 0
					s.GetFail = mask&16 != 0
					regionDiffCompare(t, regionServiceRequest{Op: op, Country: country}, s)
				})
			}
		}
	}
}
func TestMigrationRegionActualRandom(t *testing.T) {
	for i := 0; i < 1600; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(170000 + i)))
			s := regionDiffBase()
			op := "lock"
			if rng.Intn(2) == 0 {
				op = "report"
			}
			countries := []string{"", "US", "JP", "ZZ"}
			s.State.Chain = rng.Intn(5) == 0
			s.State.LockedRegion = countries[rng.Intn(4)]
			s.Running = rng.Intn(2) == 0
			s.SaveFail = rng.Intn(6) == 0
			s.PutFail = rng.Intn(4) == 0
			s.GetFail = rng.Intn(4) == 0
			s.Active = []any{autoGroup, fallbackGroup, "a", "b", nil, 42}[rng.Intn(6)]
			s.Nested = []any{"a", "off-region", nil, 42}[rng.Intn(4)]
			s.BlockProtected = rng.Intn(2) == 0
			s.State.Regions = map[string]regionRecord{}
			s.State.Health = map[string]nodeHealth{}
			now := s.Times[0]
			for j := 0; j < rng.Intn(12); j++ {
				name := fmt.Sprintf("node-%02d", j)
				age := []time.Duration{regionTTL - time.Nanosecond, regionTTL, regionTTL + time.Nanosecond, time.Hour, -time.Hour}[rng.Intn(5)]
				status := []string{regionVerified, "failed", "conflict", "unknown"}[rng.Intn(4)]
				s.State.Regions[name] = regionRecord{Name: "not-map-key", Country: countries[rng.Intn(4)], Status: status, VerifiedAt: now.Add(-age), Sources: []string{"source"}}
				s.State.Health[name] = nodeHealth{Name: "not-key", State: []string{healthHealthy, healthUnhealthy, "UNKNOWN"}[rng.Intn(3)], LatencyMs: rng.Intn(51) - 1, ConsecutiveFailures: rng.Intn(5), CooldownUntil: now.Add(time.Duration(rng.Intn(3)-1) * time.Nanosecond)}
			}
			if rng.Intn(8) == 0 {
				s.State.Regions = nil
			}
			if rng.Intn(8) == 0 {
				s.State.Health = nil
			}
			regionDiffCompare(t, regionServiceRequest{Op: op, Country: countries[rng.Intn(4)]}, s)
		})
	}
}

func TestMigrationRegionActualTimeBoundaries(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		for _, op := range []string{"lock", "report"} {
			t.Run(fmt.Sprintf("ttl/%s/%d", op, delta), func(t *testing.T) {
				s := regionDiffBase()
				now := s.Times[0]
				s.State.Regions = map[string]regionRecord{"a": {Country: "US", Status: regionVerified, VerifiedAt: now.Add(-regionTTL - delta)}}
				s.State.Health = nil
				regionDiffCompare(t, regionServiceRequest{Op: op, Country: "US"}, s)
			})
		}
	}
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		for _, protected := range []bool{false, true} {
			t.Run(fmt.Sprintf("cooldown/%d/%t", delta, protected), func(t *testing.T) {
				s := regionDiffBase()
				s.BlockProtected = protected
				now := s.Times[0]
				s.State.Regions = map[string]regionRecord{"a": s.State.Regions["a"]}
				s.State.Health = map[string]nodeHealth{"a": {State: healthUnhealthy, LatencyMs: 1, CooldownUntil: now.Add(delta)}}
				regionDiffCompare(t, regionServiceRequest{Op: "lock", Country: "US"}, s)
			})
		}
	}
	t.Run("clock-crosses-ttl-after-validation", func(t *testing.T) {
		s := regionDiffBase()
		now := s.Times[0]
		s.State.Regions = map[string]regionRecord{"a": {Country: "US", Status: regionVerified, VerifiedAt: now.Add(-regionTTL + time.Nanosecond)}}
		s.Times = []time.Time{now, now.Add(2 * time.Nanosecond), now.Add(3 * time.Nanosecond), now.Add(4 * time.Nanosecond), now.Add(5 * time.Nanosecond)}
		regionDiffCompare(t, regionServiceRequest{Op: "lock", Country: "US"}, s)
	})
	for _, at := range []time.Time{time.Time{}, time.Date(1800, 1, 1, 0, 0, 0, 0, time.FixedZone("west", -7*3600)), time.Date(2400, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 8*3600))} {
		t.Run("report-format/"+at.String(), func(t *testing.T) {
			s := regionDiffBase()
			r := s.State.Regions["a"]
			r.VerifiedAt = at
			s.State.Regions["a"] = r
			h := s.State.Health["a"]
			h.CooldownUntil = at
			h.UpdatedAt = at
			s.State.Health["a"] = h
			regionDiffCompare(t, regionServiceRequest{Op: "report"}, s)
		})
	}
}

func TestMigrationRegionActualNamesAndReportGolden(t *testing.T) {
	for _, op := range []string{"lock", "report"} {
		for _, names := range [][]string{{""}, {"", "a"}, {"中", "é", "Z", "a"}} {
			t.Run(fmt.Sprintf("%s/%v", op, names), func(t *testing.T) {
				s := regionDiffBase()
				s.State.Regions = map[string]regionRecord{}
				s.State.Health = map[string]nodeHealth{}
				for i, name := range names {
					s.State.Regions[name] = regionRecord{Name: "not-key", Country: "US", Status: regionVerified, VerifiedAt: s.Times[0]}
					s.State.Health[name] = nodeHealth{State: healthUnhealthy, LatencyMs: 10 + i}
				}
				regionDiffCompare(t, regionServiceRequest{Op: op, Country: "US"}, s)
			})
		}
	}
	t.Run("complete-golden", func(t *testing.T) {
		if !rustRegionFlowAvailable() {
			t.Fatal("actual DLL required")
		}
		s := regionDiffBase()
		f := regionDiffNew(s)
		before := regionFlowRecoveries.Load()
		r, used := runRustRegionService(regionServiceRequest{Op: "report"}, f)
		if !used || regionFlowRecoveries.Load() != before {
			t.Fatal("fallback/recovery")
		}
		at := "2041-02-03T03:05:06+08:00"
		wantNodes := []regionReportNode{{Name: "a", Country: "US", ExitIP: "192.0.2.1", Status: regionVerified, Sources: []string{"source-a", "source-b"}, Detail: "detail", VerifiedAt: at}, {Name: "b", Country: "US", Status: regionVerified, Sources: []string{}, VerifiedAt: at}}
		wantHealth := map[string]regionReportHealth{"a": {State: healthHealthy, LatencyMs: 20, ConsecutiveFailures: 3, UpdatedAt: "2041-02-03T04:04:06+08:00"}, "b": {State: healthUnhealthy, LatencyMs: 10}}
		want := map[string]any{"nodes": wantNodes, "job": &s.State.Job, "lockedRegion": "US", "pool": []string{"a", "b"}, "health": wantHealth, "ttlHours": 24}
		if r.Status != 200 || !reflect.DeepEqual(r.Data, want) {
			t.Fatalf("full DTO got=%+v want=%+v", r.Data, want)
		}
	})
}
