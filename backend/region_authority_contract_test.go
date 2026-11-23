package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func regionAuthorityNew(t *testing.T, factory authorityFactory) *app {
	a := &app{service: authorityNew(t, factory)}
	a.replaceHealthParamsLocked(defaultHealthParams())
	a.restoreHealthRecordsLocked(map[string]nodeHealth{})
	a.restoreRegionRecordsLocked(map[string]regionRecord{})
	return a
}
func regionAuthorityGolden(t *testing.T, factory authorityFactory) {
	a, b := regionAuthorityNew(t, factory), regionAuthorityNew(t, factory)
	s := a.service
	now := time.Now()
	if e := s.baseline(trafficSample{at: now, up: 71, down: 81}); e != nil {
		t.Fatal(e)
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateConfig, Config: pathCheckSettings{Enabled: true, IntervalSec: 45}})
	a.pathClockCommandLocked(pathStateAt, now, "", 0)
	before := authoritySnapshot(t, s)
	h := nodeHealth{Name: "record name differs", State: healthHealthy, LastSuccessAt: now, UpdatedAt: now, CooldownUntil: now.Add(time.Second)}
	a.writeHealthRecordLocked("key-h", h)
	rr := regionRecord{Name: "not-key", Country: "JP", Status: regionVerified, VerifiedAt: now, Sources: []string{"one", "two"}}
	a.writeRegionRecordLocked("key-r", rr)
	rr.Sources[0] = "caller edit"
	got, found := a.regionRecordLocked("key-r")
	if !found || got.Name != "not-key" || got.Sources[0] != "one" {
		t.Fatal("record keys or sources alias")
	}
	got.Sources[0] = "read edit"
	got, _ = a.regionRecordLocked("key-r")
	if got.Sources[0] != "one" {
		t.Fatal("read aliases authority")
	}
	if _, found = a.regionRecordLocked("not-key"); found {
		t.Fatal("record name used as map key")
	}
	a.setHealthBlockedLocked(true)
	a.setHealthReasonLocked("reason")
	a.setHealthNoteLocked("measurement anomaly")
	a.setHealthLastSwitchLocked(now)
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateCreateJob, Job: &regionJob{Running: true, LastError: "old", Total: 8}}, nil)
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobAdvance, Text: "verified"}, nil)
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateCreateJob, Job: &regionJob{Running: true, LastError: "replacement", Total: 9}}, nil)
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobFinish, Text: "finish", Flag: false}, nil)
	m := a.healthMetaLocked()
	if m.Job.Running || m.Job.LastError != "replacement" || m.Job.FinishedAt != "finish" {
		t.Fatal("legacy no-generation finish or nil error semantics")
	}
	if len(b.healthDomainLocked().Regions) != 0 || b.healthMetaLocked().Blocked {
		t.Fatal("instance isolation")
	}
	after := authoritySnapshot(t, s)
	if after.Up != before.Up || after.TimeToken != before.TimeToken || a.pathConfigLocked().IntervalSec != 45 || a.pathStatusLocked().At != now {
		t.Fatal("health mutation clobbered traffic/path clocks")
	}
	original := a.healthDomainLocked()
	if e := s.transferToGo(); e != nil {
		cp, _ := s.backend.Export()
		live, _ := s.backend.Read()
		t.Fatalf("transfer=%v validate=%v equal=%v lastkind=%d", e, validateServiceCheckpoint(cp), cp.Snapshot == live, cp.LastCommand.Kind)
	}
	if !reflect.DeepEqual(original, a.healthDomainLocked()) {
		t.Fatal("three-domain checkpoint incomplete")
	}
	if a.pathStatusLocked().At != now {
		t.Fatal("checkpoint path clock lost")
	}
}
func regionAuthorityShapeGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	a.restoreRegionRecordsLocked(nil)
	a.restoreHealthRecordsLocked(nil)
	p := a.healthDomainLocked()
	if p.Regions != nil || p.Health != nil {
		t.Fatal("nil maps changed")
	}
	a.restoreHealthRecordsLocked(map[string]nodeHealth{})
	a.restoreRegionRecordsLocked(map[string]regionRecord{})
	p = a.healthDomainLocked()
	if p.Regions == nil || p.Health == nil {
		t.Fatal("empty maps became nil")
	}
	for _, sources := range [][]string{nil, {}} {
		a.writeRegionRecordLocked("k", regionRecord{Sources: sources})
		r, ok := a.regionRecordLocked("k")
		if !ok || !reflect.DeepEqual(r.Sources, sources) {
			t.Fatal("sources nil shape")
		}
	}
}

type regionAuthoritySpy struct {
	serviceStateBackend
	full, narrow, meta int
	fail               bool
}

func (s *regionAuthoritySpy) HealthQuery(p string) (string, error) {
	if s.fail {
		return "", errors.New("authority read failed")
	}
	q, e := decodeServiceHealthQuery(p)
	if e != nil {
		return "", e
	}
	if q.Kind == 3 {
		s.full++
	} else {
		s.narrow++
	}
	return s.serviceStateBackend.HealthQuery(p)
}
func (s *regionAuthoritySpy) Meta() (serviceStateSnapshot, error) {
	s.meta++
	return s.serviceStateBackend.Meta()
}
func regionAuthorityNarrowGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	spy := &regionAuthoritySpy{serviceStateBackend: a.service.backend}
	a.service.backend = spy
	now := time.Now()
	for i := 0; i < 1000; i++ {
		a.writeRegionRecordLocked("same", regionRecord{Name: "record", Status: regionVerified, VerifiedAt: now.Add(time.Duration(i)), Sources: []string{"source"}})
		a.writeHealthRecordLocked("same", nodeHealth{Name: "health", LastSuccessAt: now, UpdatedAt: now.Add(time.Duration(i)), CooldownUntil: now})
		_, _ = a.regionRecordLocked("same")
		_ = a.healthMetaLocked()
		if len(a.service.clocks) > 4 {
			t.Fatalf("record clocks grew %d", len(a.service.clocks))
		}
	}
	if spy.full != 0 || spy.narrow < 3000 || spy.meta < 2000 {
		t.Fatalf("single record path performed full-table reads full=%d narrow=%d meta=%d", spy.full, spy.narrow, spy.meta)
	}
}
func regionAuthorityFaultGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	spy := &regionAuthoritySpy{serviceStateBackend: a.service.backend, fail: true}
	a.service.backend = spy
	func() {
		defer func() {
			if recover() == nil {
				t.Error("authority error substituted empty metadata")
			}
		}()
		_ = a.healthMetaLocked()
	}()
	if a.service.backend != spy {
		t.Fatal("authority error switched owner")
	}
	spy.fail = false
	f := &authorityFaultBackend{serviceStateBackend: spy, applyMode: "after"}
	a.service.backend = f
	a.setHealthBlockedLocked(true)
	if !a.healthMetaLocked().Blocked || f.applies != 1 || f.statuses != 1 {
		t.Fatal("health mutation repeated after lost reply")
	}
}
func TestServiceStateSafeRegionContracts(t *testing.T) {
	regionAuthorityGolden(t, newSelectedServiceStateBackend)
	regionAuthorityShapeGolden(t, newSelectedServiceStateBackend)
	regionAuthorityNarrowGolden(t, newSelectedServiceStateBackend)
	regionAuthorityFaultGolden(t, newSelectedServiceStateBackend)
}
