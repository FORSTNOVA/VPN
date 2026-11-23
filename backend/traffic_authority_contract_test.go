package main

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type authorityFactory func() (serviceStateBackend, error)

func authorityNew(t *testing.T, makeBackend authorityFactory) *serviceAuthority {
	t.Helper()
	b, e := makeBackend()
	if e != nil {
		t.Fatal(e)
	}
	s := newServiceAuthority(b)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func authoritySnapshot(t *testing.T, s *serviceAuthority) serviceStateSnapshot {
	t.Helper()
	v, e := s.read()
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func authorityLegacyRate(p, c trafficSample) (int64, int64) {
	if p.at.IsZero() {
		return 0, 0
	}
	elapsed := c.at.Sub(p.at).Seconds()
	if elapsed <= 0 || c.up < p.up || c.down < p.down {
		return 0, 0
	}
	return int64(float64(c.up-p.up) / elapsed), int64(float64(c.down-p.down) / elapsed)
}
func authorityRateGoldens(t *testing.T, factory authorityFactory) {
	t.Helper()
	base := time.Now()
	cases := [][2]trafficSample{
		{{}, {at: base, up: 5, down: 9}}, {{at: base, up: 10, down: 20}, {at: base.Add(time.Second), up: 30, down: 50}}, {{at: base, up: 10, down: 20}, {at: base.Add(time.Second), up: 9, down: 50}}, {{at: base, up: 10, down: 20}, {at: base, up: 30, down: 50}}, {{at: base, up: 10, down: 20}, {at: base.Add(-time.Second), up: 30, down: 50}}, {{at: base, up: 0, down: 0}, {at: base.Add(time.Nanosecond), up: math.MaxInt64, down: math.MaxInt64}}, {{at: base, up: math.MinInt64, down: math.MinInt64}, {at: base.Add(time.Second), up: math.MaxInt64, down: math.MaxInt64}}, {{at: base, up: 10, down: 20}, {at: base.Add(time.Second), up: 10, down: 20}},
	}
	for i, c := range cases {
		s := authorityNew(t, factory)
		if e := s.baseline(c[0]); e != nil {
			t.Fatal(e)
		}
		before := authoritySnapshot(t, s)
		u, d, e := s.rate(c[1])
		wu, wd := authorityLegacyRate(c[0], c[1])
		if e != nil || u != wu || d != wd {
			t.Fatalf("rate case %d got %d/%d err=%v want %d/%d", i, u, d, e, wu, wd)
		}
		if after := authoritySnapshot(t, s); after != before {
			t.Fatal("rate mutated baseline/revision")
		}
		if e = s.baseline(c[1]); e != nil {
			t.Fatal(e)
		}
		after := authoritySnapshot(t, s)
		if after.Revision != before.Revision+1 || after.Up != c[1].up || after.Down != c[1].down {
			t.Fatal("separate baseline commit")
		}
	}
}
func authoritySharedGolden(t *testing.T, factory authorityFactory) {
	a, b := authorityNew(t, factory), authorityNew(t, factory)
	start := time.Now()
	for i := 0; i < 100; i++ {
		if e := a.baseline(trafficSample{at: start.Add(time.Duration(i) * time.Second), up: int64(i * 10), down: int64(i * 20)}); e != nil {
			t.Fatal(e)
		}
		if e := a.samplingError("history-only"); e != nil {
			t.Fatal(e)
		}
		if len(a.clocks) > 1 {
			t.Fatal("clock ledger growing")
		}
		s := authoritySnapshot(t, b)
		if s.Revision != 0 || s.HasBaseline || s.SamplingError != "" {
			t.Fatal("cross-instance contamination")
		}
	}
	v := authoritySnapshot(t, a)
	if v.Revision != 200 || v.Up != 990 || v.SamplingError != "history-only" {
		t.Fatal(v)
	}
	if e := a.baseline(trafficSample{}); e != nil {
		t.Fatal(e)
	}
	v = authoritySnapshot(t, a)
	if v.HasBaseline || v.SamplingError != "history-only" || len(a.clocks) != 0 {
		t.Fatal("baseline clear changed history error")
	}
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := a.read(); e == nil {
		t.Fatal("closed authority readable")
	}
	if _, e := b.read(); e != nil {
		t.Fatal("close affected peer")
	}
}
func authorityCommandGolden(t *testing.T, factory authorityFactory) {
	s := authorityNew(t, factory)
	b := s.backend
	v := authoritySnapshot(t, s)
	c := serviceStateCommand{Epoch: v.Epoch, ID: 1, Kind: 1, HasBaseline: true, TimeToken: 7, Up: 99, Down: 100}
	a, e := b.Apply(c)
	if e != nil {
		t.Fatal(e)
	}
	a2, e := b.Apply(c)
	if e != nil || a2 != a {
		t.Fatal("same command not idempotent")
	}
	if st, e := b.CommandStatus(c); e != nil || !st.Found || st.Ack != a || !st.Pending {
		t.Fatal("status missing confirmed command")
	}
	bad := c
	bad.Up++
	if _, e = b.Apply(bad); e == nil {
		t.Fatal("different payload duplicate accepted")
	}
	if _, e = b.CommandStatus(bad); e == nil {
		t.Fatal("status accepted mismatched payload")
	}
	next := serviceStateCommand{Epoch: v.Epoch, ID: 2, ExpectedRevision: 1, Kind: 2}
	if _, e = b.Apply(next); e == nil {
		t.Fatal("write accepted before ack")
	}
	badAck := a
	badAck.ID++
	if e = b.Acknowledge(badAck); e == nil {
		t.Fatal("bad ack accepted")
	}
	if e = b.Acknowledge(a); e != nil {
		t.Fatal(e)
	}
	if e = b.Acknowledge(a); e != nil {
		t.Fatal("duplicate ack rejected")
	}
	for _, bad := range []serviceStateCommand{{Epoch: v.Epoch, ID: 0, Kind: 1}, {Epoch: v.Epoch, ID: 2, ExpectedRevision: 0, Kind: 2}, {Epoch: v.Epoch + 1, ID: 2, ExpectedRevision: 1, Kind: 2}, {Epoch: v.Epoch, ID: 2, ExpectedRevision: 1, Kind: 9}} {
		if _, e = b.Apply(bad); e == nil {
			t.Fatal("invalid command accepted", bad)
		}
	}
	after, e := b.Read()
	if e != nil || after.Revision != 1 || after.Up != 99 {
		t.Fatal("rejected command mutated")
	}
}

type authorityFaultBackend struct {
	serviceStateBackend
	applyMode, statusMode, exportMode string
	applies, statuses, closes         int
}

func (f *authorityFaultBackend) Apply(c serviceStateCommand) (serviceStateAck, error) {
	f.applies++
	if f.applyMode == "before" {
		return serviceStateAck{}, errors.New("transport before")
	}
	a, e := f.serviceStateBackend.Apply(c)
	if f.applyMode == "after" {
		return serviceStateAck{}, errors.New("transport after")
	}
	if f.applyMode == "bad-ack" {
		a.Revision++
	}
	return a, e
}
func (f *authorityFaultBackend) CommandStatus(c serviceStateCommand) (serviceStateCommandStatus, error) {
	f.statuses++
	if f.statusMode == "fail" {
		return serviceStateCommandStatus{}, errors.New("status unavailable")
	}
	return f.serviceStateBackend.CommandStatus(c)
}
func (f *authorityFaultBackend) Export() (serviceStateCheckpoint, error) {
	if f.exportMode == "fail" {
		return serviceStateCheckpoint{}, errors.New("cannot export")
	}
	cp, e := f.serviceStateBackend.Export()
	if f.exportMode == "corrupt" {
		cp.Snapshot.Revision++
	}
	return cp, e
}
func (f *authorityFaultBackend) Close() error { f.closes++; return f.serviceStateBackend.Close() }
func authorityFaultGoldens(t *testing.T, factory authorityFactory) {
	for _, mode := range []string{"after", "before", "bad-ack", "after-status-fail"} {
		b, e := factory()
		if e != nil {
			t.Fatal(e)
		}
		f := &authorityFaultBackend{serviceStateBackend: b, applyMode: mode}
		if mode == "after-status-fail" {
			f.applyMode = "after"
			f.statusMode = "fail"
		}
		s := newServiceAuthority(f)
		e = s.baseline(trafficSample{at: time.Now(), up: 100})
		if mode == "after" {
			if e != nil || s.pending != nil || f.applies != 1 || f.statuses != 1 {
				t.Fatal("lost reply not reconciled once", e)
			}
		} else {
			if e == nil || s.pending == nil {
				t.Fatal("unknown commit silently accepted")
			}
			if _, e = s.read(); e == nil {
				t.Fatal("unresolved authority read stale")
			}
			if e = s.transferToGo(); e == nil {
				t.Fatal("unresolved state silently fell back")
			}
			if f.applies != 1 {
				t.Fatal("mutation replayed")
			}
		}
		_ = s.Close()
	}
	for _, mode := range []string{"", "fail", "corrupt"} {
		b, e := factory()
		if e != nil {
			t.Fatal(e)
		}
		f := &authorityFaultBackend{serviceStateBackend: b, exportMode: mode}
		s := newServiceAuthority(f)
		_ = s.baseline(trafficSample{at: time.Now(), up: 4, down: 9})
		_ = s.samplingError("keep")
		before := authoritySnapshot(t, s)
		e = s.transferToGo()
		if mode == "" {
			if e != nil {
				t.Fatal(e)
			}
			if _, ok := s.backend.(*goServiceStateBackend); !ok {
				t.Fatal("not transferred")
			}
			if after := authoritySnapshot(t, s); !reflect.DeepEqual(before, after) || f.closes != 1 {
				t.Fatal("checkpoint incomplete")
			}
			if e = s.samplingError("next"); e != nil {
				t.Fatal(e)
			}
		} else {
			if e == nil || s.backend != f || f.closes != 0 {
				t.Fatal("failed export installed stale Go")
			}
		}
		_ = s.Close()
	}
}
func authorityGoFactory() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil }
func TestServiceStateSafeAuthorityContracts(t *testing.T) {
	authorityRateGoldens(t, authorityGoFactory)
	authoritySharedGolden(t, authorityGoFactory)
	authorityCommandGolden(t, authorityGoFactory)
	authorityFaultGoldens(t, authorityGoFactory)
}
func TestServiceStateSafeSelectedAuthority(t *testing.T) {
	authorityRateGoldens(t, newSelectedServiceStateBackend)
	authoritySharedGolden(t, newSelectedServiceStateBackend)
	authorityCommandGolden(t, newSelectedServiceStateBackend)
	authorityFaultGoldens(t, newSelectedServiceStateBackend)
}

func authorityAckGolden(t *testing.T, factory authorityFactory) {
	for _, mode := range []string{"before", "after"} {
		b, e := factory()
		if e != nil {
			t.Fatal(e)
		}
		f := &authorityAckFault{serviceStateBackend: b, mode: mode}
		s := newServiceAuthority(f)
		e = s.baseline(trafficSample{at: time.Now(), up: 13})
		if mode == "after" {
			if e != nil || s.pending != nil {
				t.Fatal("ack reply loss unreconciled", e)
			}
		} else {
			if e == nil || s.pending == nil {
				t.Fatal("unconfirmed ack accepted")
			}
			cp, er := b.Export()
			if er != nil || !cp.Pending || cp.Snapshot.Up != 13 {
				t.Fatal("pending checkpoint lost committed payload")
			}
			if e = s.samplingError("new"); e == nil {
				t.Fatal("new write during pending")
			}
			if e = s.transferToGo(); e == nil {
				t.Fatal("pending export installed")
			}
		}
		if f.acks != 1 {
			t.Fatal("ack repeated")
		}
		_ = s.Close()
	}
}

type authorityAckFault struct {
	serviceStateBackend
	mode string
	acks int
}

func (f *authorityAckFault) Acknowledge(a serviceStateAck) error {
	f.acks++
	if f.mode == "after" {
		if e := f.serviceStateBackend.Acknowledge(a); e != nil {
			return e
		}
	}
	return errors.New("ack transport unavailable")
}
func TestServiceStateSafeAck(t *testing.T) { authorityAckGolden(t, newSelectedServiceStateBackend) }
