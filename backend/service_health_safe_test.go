package main

import (
	"errors"
	"testing"
	"time"
)

type healthSafeBackend struct {
	serviceStateBackend
	fail         bool
	full, narrow int
}

func (b *healthSafeBackend) HealthQuery(p string) (string, error) {
	q, _ := decodeServiceHealthQuery(p)
	if q.Kind == 3 {
		b.full++
	} else {
		b.narrow++
	}
	if b.fail {
		return "", errors.New("injected health unavailable")
	}
	return b.serviceStateBackend.HealthQuery(p)
}
func TestServiceStateHealthWorkerFault(t *testing.T) {
	b := &healthSafeBackend{serviceStateBackend: newGoServiceStateBackend(), fail: true}
	a := &app{service: newServiceAuthority(b)}
	for _, op := range []string{"start", "run"} {
		r := a.runRegionJobService(regionJobServiceRequest{Op: op})
		if r.Status != 500 || r.Err == nil {
			t.Fatalf("%s: %+v", op, r)
		}
		if !a.mu.TryLock() {
			t.Fatal("job lock leaked")
		}
		a.mu.Unlock()
	}
	r := a.runRegionService(regionServiceRequest{Op: "report"})
	if r.Status != 500 {
		t.Fatalf("region: %+v", r)
	}
	if !a.mu.TryLock() {
		t.Fatal("report lock leaked")
	}
	a.mu.Unlock()
}
func TestServiceStateHealthNarrowClockBound(t *testing.T) {
	b := &healthSafeBackend{serviceStateBackend: newGoServiceStateBackend()}
	a := &app{service: newServiceAuthority(b)}
	a.restoreHealthRecordsLocked(map[string]nodeHealth{})
	a.restoreRegionRecordsLocked(map[string]regionRecord{})
	b.full = 0
	for i := 0; i < 200; i++ {
		now := time.Unix(int64(i+1), 0)
		a.writeHealthRecordLocked("one", nodeHealth{Name: "alias", UpdatedAt: now, LastSuccessAt: now, CooldownUntil: now})
		a.writeRegionRecordLocked("one", regionRecord{Name: "different", VerifiedAt: now})
	}
	if b.full != 0 {
		t.Fatalf("record writes used %d full queries", b.full)
	}
	if len(a.service.healthClocks) != 4 || len(a.service.clocks) != 4 {
		t.Fatalf("clock leak %d/%d", len(a.service.healthClocks), len(a.service.clocks))
	}
	if _, ok := a.healthRecordLocked("alias"); ok {
		t.Fatal("map key conflated with record name")
	}
	if e := a.service.transferToGo(); e != nil {
		t.Fatal(e)
	}
	a.writeRegionRecordLocked("one", regionRecord{Name: "different", VerifiedAt: time.Unix(999, 0)})
	if len(a.service.clocks) != 4 {
		t.Fatal("handoff clock ledger leak")
	}
}
func TestServiceStateHealthRejectAliasedTakeoverClock(t *testing.T) {
	h := serviceHealthState{Health: map[string]serviceHealthRecord{"a": {UpdatedToken: 1}, "b": {UpdatedToken: 1}}}
	if _, e := uniqueServiceHealthClockRefs(h, serviceStateSnapshot{}, servicePathState{}); e == nil {
		t.Fatal("accepted shared record clock")
	}
	delete(h.Health, "b")
	if _, e := uniqueServiceHealthClockRefs(h, serviceStateSnapshot{HasBaseline: true, TimeToken: 1}, servicePathState{}); e == nil {
		t.Fatal("accepted traffic/health shared clock")
	}
}

type healthLostAck struct {
	serviceStateBackend
	applies int
}

func (b *healthLostAck) Apply(c serviceStateCommand) (serviceStateAck, error) {
	b.applies++
	a, e := b.serviceStateBackend.Apply(c)
	if e != nil {
		return a, e
	}
	return a, errors.New("lost reply")
}
func (b *healthLostAck) CommandStatus(c serviceStateCommand) (serviceStateCommandStatus, error) {
	return serviceStateCommandStatus{}, errors.New("status unavailable")
}
func TestServiceStateHealthPendingClocksPreserved(t *testing.T) {
	a := &app{}
	a.restoreHealthRecordsLocked(map[string]nodeHealth{})
	a.writeHealthRecordLocked("n", nodeHealth{Name: "n", UpdatedAt: time.Unix(1, 0)})
	b := &healthLostAck{serviceStateBackend: a.service.backend}
	a.service.backend = b
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing failclosed panic")
			}
		}()
		a.writeHealthRecordLocked("n", nodeHealth{Name: "n", UpdatedAt: time.Unix(2, 0)})
	}()
	if a.service.pending == nil || len(a.service.clocks) != 2 || b.applies != 1 {
		t.Fatalf("pending ownership lost: %v %d %d", a.service.pending, len(a.service.clocks), b.applies)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("unresolved write accepted")
			}
		}()
		a.setHealthBlockedLocked(true)
	}()
	if b.applies != 1 {
		t.Fatal("effect replay after unresolved command")
	}
	if e := a.service.transferToGo(); e == nil {
		t.Fatal("unresolved takeover accepted")
	}
}
func TestServiceStateHealthPatrolCreationFault(t *testing.T) {
	b := &healthSafeBackend{serviceStateBackend: newGoServiceStateBackend(), fail: true}
	a := &app{service: newServiceAuthority(b)}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected authority rejection")
			}
		}()
		a.startPatrolLocked()
	}()
	if a.patrolCancel != nil {
		t.Fatal("failed start published ghost patrol")
	}
}

func TestServiceStateHealthJobCommitFailure(t *testing.T) {
	a := &app{}
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateCreateJob, Job: &regionJob{Running: true, LastError: "old"}}, nil)
	b := &healthLostAck{serviceStateBackend: a.service.backend}
	a.service.backend = b
	a.mu.Lock()
	r := a.runRegionJobService(regionJobServiceRequest{Op: "finish"})
	if r.Status != 500 || r.Err == nil || b.applies != 1 {
		t.Fatalf("finish failure %+v applies=%d", r, b.applies)
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	a.mu.Unlock()
	cp, e := b.serviceStateBackend.Export()
	if e != nil {
		t.Fatal(e)
	}
	h, e := decodeServiceHealthState(cp.Snapshot.HealthPayload)
	if e != nil {
		t.Fatal(e)
	}
	if h.Job == nil || h.Job.Running || h.Job.LastError != "old" {
		t.Fatalf("committed latefinish lost: %+v", h.Job)
	}
	r = a.runRegionJobService(regionJobServiceRequest{Op: "run"})
	if r.Status != 500 || b.applies != 1 {
		t.Fatal("pending job replay")
	}
	if !a.mu.TryLock() {
		t.Fatal("owned lock leaked")
	}
	a.mu.Unlock()
}

func TestServiceStateHealthExistingPatrolPreserved(t *testing.T) {
	cancel := make(chan struct{})
	b := &healthSafeBackend{serviceStateBackend: newGoServiceStateBackend(), fail: true}
	a := &app{service: newServiceAuthority(b), patrolCancel: cancel}
	a.startPatrolLocked()
	if a.patrolCancel != cancel || b.narrow != 0 || b.full != 0 {
		t.Fatal("existing patrol consulted authority or replaced")
	}
	select {
	case <-cancel:
		t.Fatal("existing patrol cancelled")
	default:
	}
}
