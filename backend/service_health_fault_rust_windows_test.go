//go:build windows && amd64 && rustcore

package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func healthFaultCommands() []serviceHealthCommand {
	s := healthCodecState()
	return []serviceHealthCommand{{Action: healthStateParams, Params: s.Params}, {Action: healthStateRestoreHealth, Health: s.Health}, {Action: healthStateRestoreRegions, Regions: s.Regions}, {Action: healthStateRecordHealth, Key: "key-not-name", HealthRecord: s.Health["different-key"]}, {Action: healthStateRecordRegion, Key: "other-region-key", RegionRecord: s.Regions["region-key"]}, {Action: healthStateBlocked, Flag: true}, {Action: healthStateBlockReason, Text: "reason"}, {Action: healthStateSweepNote, Text: "independent HTTPS passed"}, {Action: healthStateLastSwitch, TimeToken: 5}, {Action: healthStateCreateJob, Job: s.Job}, {Action: healthStateJobTotal, Count: 9}, {Action: healthStateJobAdvance, Text: "verified"}, {Action: healthStateJobFinish, Text: "finish", Flag: true, Detail: "late old finish"}, {Action: healthStateClearHealth}, {Action: healthStateClearRegions}}
}
func TestServiceStateHealthEveryActionDeliveryFault(t *testing.T) {
	commands := healthFaultCommands()
	for target := range commands {
		for _, op := range []byte{3, 6} {
			a, tr, b := newServiceFault(t, 0, false, false)
			oracle := newServiceAuthority(newGoServiceStateBackend())
			for _, owner := range []*serviceAuthority{a, oracle} {
				owner.healthClocks = map[uint64]bool{}
				for i := uint64(1); i <= 5; i++ {
					owner.clocks[i] = time.Unix(int64(i), 0)
					owner.healthClocks[i] = true
				}
			}
			for i, c := range commands {
				if i == target {
					tr.op = op
					tr.fired = false
					tr.corrupt = true
				}
				if e := a.healthCommand(c, nil); e != nil {
					t.Fatalf("action%d target%d op%d %v", c.Action, target, op, e)
				}
				if e := oracle.healthCommand(c, nil); e != nil {
					t.Fatal(e)
				}
				g, e := a.healthQuery(serviceHealthQuery{Kind: 3})
				want, we := oracle.healthQuery(serviceHealthQuery{Kind: 3})
				if e != nil || we != nil || !reflect.DeepEqual(g, want) {
					t.Fatal("domain diverged", c.Action, e, we, g, want)
				}
			}
			s, e := b.Meta()
			if e != nil || s.Revision != uint64(len(commands)) || tr.counts[3] != len(commands) || tr.counts[6] != len(commands) || tr.counts[4] != 1 {
				t.Fatal("mutation replay", tr.counts, s, e)
			}
			_ = a.Close()
			_ = oracle.Close()
		}
	}
}

type healthQueryCostTransport struct {
	serviceStateTransport
	readCalls, exportCalls, queries, maxReply int
}

func (t *healthQueryCostTransport) call(op byte, b []byte) ([]byte, error) {
	if op == 1 {
		t.readCalls++
	}
	if op == 5 {
		t.exportCalls++
	}
	v, e := t.serviceStateTransport.call(op, b)
	if op == 8 {
		t.queries++
		if len(v) > t.maxReply {
			t.maxReply = len(v)
		}
	}
	return v, e
}
func TestServiceStateHealthNarrowLargeTable(t *testing.T) {
	base, e := newRustServiceStateBackend()
	if e != nil {
		t.Fatal(e)
	}
	b := base.(*rustServiceStateBackend)
	tr := &healthQueryCostTransport{serviceStateTransport: b.transport}
	b.transport = tr
	a := newServiceAuthority(b)
	defer a.Close()
	table := make(map[string]serviceHealthRecord, 10000)
	for i := 0; i < 10000; i++ {
		table[fmt.Sprintf("key-%05d", i)] = serviceHealthRecord{Record: nodeHealth{Name: "different name", State: "healthy", LatencyMs: i}}
	}
	if e = a.healthCommand(serviceHealthCommand{Action: healthStateRestoreHealth, Health: table}, nil); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("key-%05d", i)
		v := serviceHealthRecord{Record: nodeHealth{Name: key, State: "suspect", LatencyMs: i + 1}}
		if e = a.healthCommand(serviceHealthCommand{Action: healthStateRecordHealth, Key: key, HealthRecord: v}, nil); e != nil {
			t.Fatal(e)
		}
		q, e := a.healthQuery(serviceHealthQuery{Kind: 1, Key: key})
		if e != nil || !q.Found || q.HealthRecord != v {
			t.Fatal("narrow data", i, e)
		}
		if len(a.clocks) != 0 {
			t.Fatal("clock leak")
		}
	}
	if tr.readCalls != 0 || tr.exportCalls != 0 || tr.queries != 10000 || tr.maxReply > 4096 {
		t.Fatal("full table on narrow path", tr.readCalls, tr.exportCalls, tr.queries, tr.maxReply)
	}
	meta, e := b.Meta()
	if e != nil || meta.HealthPayload != "" || meta.Revision != 10001 {
		t.Fatal("meta not narrow", e, meta.Revision)
	}
}
func TestServiceStateHealthFailClosedBeforeLookup(t *testing.T) {
	a, tr, _ := newServiceFault(t, 3, true, false)
	if e := a.healthCommand(serviceHealthCommand{Action: healthStateBlocked, Flag: true}, nil); e == nil {
		t.Fatal("unknown accepted")
	}
	n := tr.counts[8]
	for i := 0; i < 1000; i++ {
		if _, e := a.healthQuery(serviceHealthQuery{Kind: 1, Key: "node"}); e == nil {
			t.Fatal("pending treated as missing")
		}
	}
	if tr.counts[8] != n {
		t.Fatal("pending reached query")
	}
	if e := a.transferToGo(); e == nil {
		t.Fatal("unknown transfer")
	}
}

type healthBadExport struct {
	serviceStateBackend
	mutate func(*serviceHealthState)
}

func (b *healthBadExport) Export() (serviceStateCheckpoint, error) {
	cp, e := b.serviceStateBackend.Export()
	if e != nil {
		return cp, e
	}
	s, e := decodeServiceHealthState(cp.Snapshot.HealthPayload)
	if e != nil {
		return cp, e
	}
	b.mutate(&s)
	cp.Snapshot.HealthPayload, e = encodeServiceHealthState(s)
	return cp, e
}
func TestServiceStateHealthCheckpointEveryField(t *testing.T) {
	h := func(f func(*serviceHealthRecord)) func(*serviceHealthState) {
		return func(s *serviceHealthState) { v := s.Health["different-key"]; f(&v); s.Health["different-key"] = v }
	}
	r := func(f func(*serviceRegionRecord)) func(*serviceHealthState) {
		return func(s *serviceHealthState) { v := s.Regions["region-key"]; f(&v); s.Regions["region-key"] = v }
	}
	mutations := []func(*serviceHealthState){
		func(s *serviceHealthState) { s.Params.ConnectTimeoutMs++ }, func(s *serviceHealthState) { s.Params.PatrolIntervalSec++ }, func(s *serviceHealthState) { s.Params.FailureThreshold++ }, func(s *serviceHealthState) { s.Params.CooldownSec++ }, func(s *serviceHealthState) { s.Params.MinDwellSec++ }, func(s *serviceHealthState) { s.Params.RecoverySuccesses++ }, func(s *serviceHealthState) { s.Health = nil }, func(s *serviceHealthState) {
			s.Health["otherkey"] = s.Health["different-key"]
			delete(s.Health, "different-key")
		},
		h(func(v *serviceHealthRecord) { v.Record.Name = "changed" }), h(func(v *serviceHealthRecord) { v.Record.State = "changed" }), h(func(v *serviceHealthRecord) { v.Record.ConsecutiveFailures++ }), h(func(v *serviceHealthRecord) { v.Record.ConsecutiveSuccesses++ }), h(func(v *serviceHealthRecord) { v.Record.LatencyMs++ }), h(func(v *serviceHealthRecord) { v.LastSuccessToken = 5 }), h(func(v *serviceHealthRecord) { v.CooldownToken = 5 }), h(func(v *serviceHealthRecord) { v.UpdatedToken = 5 }),
		func(s *serviceHealthState) { s.Regions = nil }, func(s *serviceHealthState) {
			s.Regions["otherkey"] = s.Regions["region-key"]
			delete(s.Regions, "region-key")
		}, r(func(v *serviceRegionRecord) { v.Record.Name = "changed" }), r(func(v *serviceRegionRecord) { v.Record.ExitIP = "changed" }), r(func(v *serviceRegionRecord) { v.Record.Country = "changed" }), r(func(v *serviceRegionRecord) { v.Record.Status = "changed" }), r(func(v *serviceRegionRecord) { v.Record.Sources = nil }), r(func(v *serviceRegionRecord) { v.Record.Detail = "changed" }), r(func(v *serviceRegionRecord) { v.VerifiedToken = 5 }),
		func(s *serviceHealthState) { s.Job = nil }, func(s *serviceHealthState) { s.Job.Running = true }, func(s *serviceHealthState) { s.Job.Total++ }, func(s *serviceHealthState) { s.Job.Done++ }, func(s *serviceHealthState) { s.Job.Verified++ }, func(s *serviceHealthState) { s.Job.Skipped++ }, func(s *serviceHealthState) { s.Job.Failed++ }, func(s *serviceHealthState) { s.Job.StartedAt = "changed" }, func(s *serviceHealthState) { s.Job.FinishedAt = "changed" }, func(s *serviceHealthState) { s.Job.LastError = "changed" }, func(s *serviceHealthState) { s.Blocked = true }, func(s *serviceHealthState) { s.BlockReason = "changed" }, func(s *serviceHealthState) { s.SweepNote = "changed" }, func(s *serviceHealthState) { s.LastSwitchToken = 5 }}
	for i, m := range mutations {
		a, _, base := newServiceFault(t, 0, false, false)
		s := healthCodecState()
		s.Job.Running = false
		a.healthClocks = map[uint64]bool{}
		for id := uint64(1); id <= 5; id++ {
			a.clocks[id] = time.Unix(int64(id), 0)
			a.healthClocks[id] = true
		}
		for _, c := range []serviceHealthCommand{{Action: healthStateParams, Params: s.Params}, {Action: healthStateRestoreHealth, Health: s.Health}, {Action: healthStateRestoreRegions, Regions: s.Regions}, {Action: healthStateCreateJob, Job: s.Job}} {
			if e := a.healthCommand(c, nil); e != nil {
				t.Fatal(e)
			}
		}
		bad := &healthBadExport{serviceStateBackend: base, mutate: m}
		a.backend = bad
		if e := a.transferToGo(); e == nil {
			t.Fatal("damaged health checkpoint accepted", i)
		}
		if a.backend != bad {
			t.Fatal("authority changed", i)
		}
		_ = a.Close()
	}
}

func TestServiceStateHealthCheckpointTokenAliases(t *testing.T) {
	for _, kind := range []string{"health", "traffic", "path"} {
		t.Run(kind, func(t *testing.T) {
			a, _, b := newServiceFault(t, 0, false, false)
			now := time.Now()
			a.clocks[1] = now
			a.healthClocks = map[uint64]bool{1: true}
			a.nextToken = 1
			table := map[string]serviceHealthRecord{"a": {Record: nodeHealth{Name: "a"}, UpdatedToken: 1}}
			switch kind {
			case "health":
				table["b"] = serviceHealthRecord{Record: nodeHealth{Name: "b"}, UpdatedToken: 1}
			case "traffic":
				a.nextToken = 0
				if e := a.baseline(trafficSample{at: now, up: 1}); e != nil {
					t.Fatal(e)
				}
			case "path":
				if e := a.pathCommand(servicePathCommand{Action: pathStateAt, TimeToken: 1}); e != nil {
					t.Fatal(e)
				}
			}
			if e := a.healthCommand(serviceHealthCommand{Action: healthStateRestoreHealth, Health: table}, nil); e != nil {
				t.Fatal(e)
			}
			before, e := b.Export()
			if e != nil {
				t.Fatal(e)
			}
			if e = a.transferToGo(); e == nil {
				t.Fatal("aliased clock transferred")
			}
			if a.backend != b {
				t.Fatal("owner changed")
			}
			after, e := b.Export()
			if e != nil || before != after {
				t.Fatal("failed takeover mutated state", e)
			}
		})
	}
}
func TestServiceStateHealthUniqueTokenHandoffAndRetire(t *testing.T) {
	a, _, _ := newServiceFault(t, 0, false, false)
	application := &app{service: a}
	application.mu.Lock()
	defer application.mu.Unlock()
	now := time.Now()
	application.restoreHealthRecordsLocked(map[string]nodeHealth{"a": {Name: "a", UpdatedAt: now}, "b": {Name: "b", UpdatedAt: now.Add(time.Second)}})
	old, e := a.healthQuery(serviceHealthQuery{Kind: 1, Key: "a"})
	if e != nil {
		t.Fatal(e)
	}
	other, e := a.healthQuery(serviceHealthQuery{Kind: 1, Key: "b"})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.transferToGo(); e != nil {
		t.Fatal(e)
	}
	application.writeHealthRecordLocked("a", nodeHealth{Name: "a", UpdatedAt: now.Add(2 * time.Second)})
	if _, ok := a.clocks[old.HealthRecord.UpdatedToken]; ok {
		t.Fatal("old token retained")
	}
	if _, ok := a.clocks[other.HealthRecord.UpdatedToken]; !ok {
		t.Fatal("other token retired")
	}
	v, ok := application.healthRecordLocked("b")
	if !ok || !v.UpdatedAt.Equal(now.Add(time.Second)) {
		t.Fatal("clock changed")
	}
}
