package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func healthCodecState() serviceHealthState {
	return serviceHealthState{Params: healthParams{ConnectTimeoutMs: 5000, PatrolIntervalSec: 60, FailureThreshold: 3, CooldownSec: 60, MinDwellSec: 30, RecoverySuccesses: 2}, Health: map[string]serviceHealthRecord{"different-key": {Record: nodeHealth{Name: "\xff\x00name", State: "healthy", ConsecutiveFailures: -1, ConsecutiveSuccesses: 7, LatencyMs: 99}, LastSuccessToken: 1, CooldownToken: 2, UpdatedToken: 3}}, Regions: map[string]serviceRegionRecord{"region-key": {Record: regionRecord{Name: "not-key", ExitIP: "203.0.113.1", Country: "ZZ", Status: "verified", Sources: []string{}, Detail: "detail"}, VerifiedToken: 4}}, Job: &regionJob{Running: true, Total: 7, Done: 3, Verified: 1, Skipped: 1, Failed: 1, StartedAt: "opaque-start", FinishedAt: "opaque-end", LastError: "\xff"}, Blocked: true, BlockReason: "reason", SweepNote: "note", LastSwitchToken: 5}
}
func TestServiceStateHealthCodecShapes(t *testing.T) {
	for _, v := range []serviceHealthState{{}, {Health: map[string]serviceHealthRecord{}, Regions: map[string]serviceRegionRecord{}}, healthCodecState()} {
		b, e := encodeServiceHealthState(v)
		got, de := decodeServiceHealthState(b)
		if e != nil || de != nil || !reflect.DeepEqual(v, got) {
			t.Fatal("shape or field lost", e, de, v, got)
		}
		if _, e = decodeServiceHealthState(b + "x"); e == nil {
			t.Fatal("trailing accepted")
		}
		for i := 1; i < len(b); i++ {
			if _, e = decodeServiceHealthState(b[:i]); e == nil {
				t.Fatal("truncation accepted", i)
			}
		}
	}
}
func TestServiceStateHealthCodecCommandsQueries(t *testing.T) {
	s := healthCodecState()
	for action := uint8(1); action <= 15; action++ {
		v := serviceHealthCommand{Action: action, Params: s.Params, Health: s.Health, Regions: s.Regions, Key: "key", HealthRecord: s.Health["different-key"], RegionRecord: s.Regions["region-key"], Job: s.Job, Flag: true, Text: "text", Detail: "detail", Count: -3, TimeToken: 99}
		b, e := encodeServiceHealthCommand(v)
		got, de := decodeServiceHealthCommand(b)
		if e != nil || de != nil || !reflect.DeepEqual(v, got) {
			t.Fatal(action, e, de)
		}
	}
	for k := uint8(0); k < 4; k++ {
		v := serviceHealthQuery{Kind: k, Key: "\xff\x00"}
		b, e := encodeServiceHealthQuery(v)
		got, de := decodeServiceHealthQuery(b)
		if e != nil || de != nil || got != v {
			t.Fatal(e, de)
		}
	}
	v := serviceHealthReply{Found: true, State: s, HealthRecord: s.Health["different-key"], RegionRecord: s.Regions["region-key"]}
	b, e := encodeServiceHealthReply(v)
	got, de := decodeServiceHealthReply(b)
	if e != nil || de != nil || !reflect.DeepEqual(v, got) {
		t.Fatal(e, de)
	}
}
func TestServiceStateHealthCodecRejectsClockAndBadKeys(t *testing.T) {
	s := healthCodecState()
	h := s.Health["different-key"]
	h.Record.UpdatedAt = time.Now()
	s.Health["different-key"] = h
	if _, e := encodeServiceHealthState(s); e == nil {
		t.Fatal("raw time accepted")
	}
	w := serviceHealthWriter{}
	w.params(healthParams{})
	w.count(true, 2)
	for i := 0; i < 2; i++ {
		w.str("duplicate")
		w.health(serviceHealthRecord{})
	}
	w.regionMap(nil)
	w.job(nil)
	w.flag(false)
	w.str("")
	w.str("")
	w.n(0)
	b, e := w.finish()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = decodeServiceHealthState(b); e == nil {
		t.Fatal("duplicate map key accepted")
	}
	if _, e = encodeServiceHealthCommand(serviceHealthCommand{Text: strings.Repeat("x", serviceHealthMaxBytes)}); e == nil {
		t.Fatal("oversize accepted")
	}
	if _, e = decodeServiceHealthState(string([]byte{2})); e == nil {
		t.Fatal("malformed accepted")
	}
}
