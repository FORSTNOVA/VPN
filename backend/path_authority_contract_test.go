package main

import (
	"reflect"
	"testing"
	"time"
)

func pathAuthorityState(t *testing.T, s *serviceAuthority) servicePathState {
	t.Helper()
	p, e := s.pathState()
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func pathAuthorityApply(t *testing.T, s *serviceAuthority, c servicePathCommand) {
	t.Helper()
	if e := s.pathCommand(c); e != nil {
		t.Fatal(e)
	}
}
func pathAuthorityGolden(t *testing.T, factory authorityFactory) {
	s := authorityNew(t, factory)
	a := &app{service: s}
	other := authorityNew(t, factory)
	start := time.Now()
	if e := s.baseline(trafficSample{at: start, up: 100, down: 200}); e != nil {
		t.Fatal(e)
	}
	if e := s.samplingError("history remains"); e != nil {
		t.Fatal(e)
	}
	initial := authoritySnapshot(t, s)
	cfg := pathCheckSettings{Enabled: true, IntervalSec: 30, Targets: []pathTarget{{Label: "extra", URL: "https://synthetic.invalid", Enabled: true}}}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateConfig, Config: cfg})
	cfg.Targets[0].Label = "external mutation"
	if pathAuthorityState(t, s).Config.Targets[0].Label != "extra" {
		t.Fatal("config borrowed caller memory")
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateOpenMonitor})
	p := pathAuthorityState(t, s)
	generation := p.Generation
	if !p.MonitorActive || generation != 1 {
		t.Fatal(p)
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateSamples, Generation: generation, Samples: []servicePathSample{{ID: "sample", Upload: 9}}})
	a.pathClockCommandLocked(pathStateThrottle, start, "extra", generation)
	a.pathClockCommandLocked(pathStateAt, start, "", 0)
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateBeginRound})
	lease := pathAuthorityState(t, s).LeaseID
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateRunning, Flag: true})
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateRevokeMonitor})
	p = pathAuthorityState(t, s)
	if !p.Busy || p.LeaseID != lease || len(p.Previous) != 0 || len(p.Throttle) != 0 {
		t.Fatal("revoke cancelled admitted round or retained loop data")
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateNote, Text: "admitted completes"})
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateFinishRound, LeaseID: lease})
	p = pathAuthorityState(t, s)
	if p.Busy || !p.Status.Running || p.Status.Note != "admitted completes" {
		t.Fatal("baseline cleanup semantics")
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateBeginRound})
	lease = pathAuthorityState(t, s).LeaseID
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateFinishRound, LeaseID: lease, Flag: true})
	if pathAuthorityState(t, s).Status.Running {
		t.Fatal("recovery cleanup retained running")
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateOpenMonitor})
	if p = pathAuthorityState(t, s); p.Generation != generation+1 {
		t.Fatal("generation reused")
	}
	v := authoritySnapshot(t, s)
	if v.Up != initial.Up || v.Down != initial.Down || v.TimeToken != initial.TimeToken || v.SamplingError != initial.SamplingError || v.Revision <= initial.Revision {
		t.Fatal("path clobbered traffic/shared revision")
	}
	op := pathAuthorityState(t, other)
	if op.MonitorActive || op.Busy || op.Generation != 0 {
		t.Fatal("instance leak")
	}
	if e := s.baseline(trafficSample{at: start.Add(time.Second), up: 110, down: 220}); e != nil {
		t.Fatal(e)
	}
	if a.pathStatusLocked().At != start {
		t.Fatal("traffic commit pruned path clock")
	}
	before := pathAuthorityState(t, s)
	if e := s.transferToGo(); e != nil {
		t.Fatal(e)
	}
	if after := pathAuthorityState(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("path checkpoint incomplete")
	}
	if a.pathStatusLocked().At != start {
		t.Fatal("checkpoint clock lost")
	}
}
func pathAuthorityJSONGolden(t *testing.T, factory authorityFactory) {
	s := authorityNew(t, factory)
	for _, targets := range [][]pathTarget{nil, {}, {{Label: "x", URL: "https://synthetic.invalid"}}} {
		cfg := pathCheckSettings{Targets: targets}
		pathAuthorityApply(t, s, servicePathCommand{Action: pathStateConfig, Config: cfg})
		got := pathAuthorityState(t, s).Config
		if !reflect.DeepEqual(got, cfg) {
			t.Fatalf("config nil/empty shape changed %+v/%+v", got, cfg)
		}
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateFailures, Strings: []string{}})
	if pathAuthorityState(t, s).Status.Failures == nil {
		t.Fatal("failures must nonnil empty")
	}
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateResults})
	if pathAuthorityState(t, s).Status.Results != nil {
		t.Fatal("results nil changed")
	}
}
func pathAuthorityInvalidGolden(t *testing.T, factory authorityFactory) {
	s := authorityNew(t, factory)
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateOpenMonitor})
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateBeginRound})
	for _, c := range []servicePathCommand{{Action: pathStateOpenMonitor}, {Action: pathStateBeginRound}, {Action: pathStateFinishRound, LeaseID: 999}, {Action: pathStateSamples, Generation: 999}, {Action: pathStateThrottle, Generation: 1, TimeToken: 0}, {Action: 255}} {
		before := authoritySnapshot(t, s)
		payload, e := encodeServicePathCommand(c)
		if e != nil {
			continue
		}
		cmd := serviceStateCommand{Epoch: before.Epoch, ID: s.nextCommand + 1, ExpectedRevision: before.Revision, Kind: 3, PathPayload: payload}
		if _, e = s.backend.Apply(cmd); e == nil {
			t.Fatal("invalid generation/lease accepted", c)
		}
		after := authoritySnapshot(t, s)
		if after != before {
			t.Fatal("invalid command altered authority")
		}
	}
}
func TestServiceStateSafePathContracts(t *testing.T) {
	pathAuthorityGolden(t, newSelectedServiceStateBackend)
	pathAuthorityJSONGolden(t, newSelectedServiceStateBackend)
	pathAuthorityInvalidGolden(t, newSelectedServiceStateBackend)
}
func pathAuthorityFaultGolden(t *testing.T, factory authorityFactory) {
	s := authorityNew(t, factory)
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateConfig, Config: pathCheckSettings{Enabled: true, IntervalSec: 77}})
	before := authoritySnapshot(t, s)
	f := &authorityFaultBackend{serviceStateBackend: s.backend, applyMode: "after"}
	s.backend = f
	pathAuthorityApply(t, s, servicePathCommand{Action: pathStateNote, Text: "committed once"})
	after := authoritySnapshot(t, s)
	if after.Revision != before.Revision+1 || f.applies != 1 || f.statuses != 1 || pathAuthorityState(t, s).Status.Note != "committed once" {
		t.Fatal("path apply lost-reply replay")
	}
	f.applyMode = ""
	if e := s.transferToGo(); e != nil {
		t.Fatal(e)
	}
	p := pathAuthorityState(t, s)
	if p.Config.IntervalSec != 77 || p.Status.Note != "committed once" {
		t.Fatal("path missing from checkpoint")
	}
	s2 := authorityNew(t, factory)
	v := authoritySnapshot(t, s2)
	payload, e := encodeServicePathCommand(servicePathCommand{Action: pathStateNote, Text: "first"})
	if e != nil {
		t.Fatal(e)
	}
	c := serviceStateCommand{Epoch: v.Epoch, ID: 1, Kind: 3, PathPayload: payload}
	ack, e := s2.backend.Apply(c)
	if e != nil {
		t.Fatal(e)
	}
	ack2, e := s2.backend.Apply(c)
	if e != nil || ack != ack2 {
		t.Fatal("path duplicate not idempotent")
	}
	payload, e = encodeServicePathCommand(servicePathCommand{Action: pathStateNote, Text: "different"})
	if e != nil {
		t.Fatal(e)
	}
	c.PathPayload = payload
	if _, e = s2.backend.Apply(c); e == nil {
		t.Fatal("path same-ID payload changed")
	}
	cp, e := s2.backend.Export()
	if e != nil || !cp.Pending || cp.LastCommand.PathPayload == payload {
		t.Fatal("pending path checkpoint incomplete")
	}
	if e = s2.backend.Acknowledge(ack); e != nil {
		t.Fatal(e)
	}
}
func TestServiceStateSafePathFaults(t *testing.T) {
	pathAuthorityFaultGolden(t, newSelectedServiceStateBackend)
}
