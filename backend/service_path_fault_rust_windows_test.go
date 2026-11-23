//go:build windows && amd64 && rustcore

package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func servicePathFaultCommands() []servicePathCommand {
	return []servicePathCommand{
		{Action: pathStateConfig, Config: pathCheckSettings{Enabled: true, IntervalSec: 120, Targets: []pathTarget{{Label: "a", URL: "https://example.invalid/?secret=value", Enabled: true}}}},
		{Action: pathStateOpenMonitor}, {Action: pathStateBeginRound}, {Action: pathStateRunning, Flag: true}, {Action: pathStatePhase, Text: "recovery"}, {Action: pathStateNote, Text: "note"}, {Action: pathStateAlert, Text: "alert"}, {Action: pathStateDemanded, Flag: true}, {Action: pathStateRetryMode, Text: "mode"}, {Action: pathStateRetryAt, Text: "opaque time"}, {Action: pathStateIncrementAlert}, {Action: pathStateReplaceSwitched, Strings: []string{}}, {Action: pathStateAppendSwitch, Text: "node"}, {Action: pathStateAt, TimeToken: 1}, {Action: pathStateResults, Results: []siteCheckResult{{Name: "a", HTTPCode: 403, State: "ok", LatencyMs: 99}}}, {Action: pathStateFailures, Strings: []string{}}, {Action: pathStateActiveNode, Text: "active"}, {Action: pathStateSamples, Generation: 1, Samples: []servicePathSample{{ID: "id", Upload: 1 << 54}}}, {Action: pathStateThrottle, Generation: 1, Text: "label", TimeToken: 1}, {Action: pathStateFinishRound, LeaseID: 1, Flag: true}, {Action: pathStateRevokeMonitor}}
}
func TestServiceStatePathEveryActionDeliveryFault(t *testing.T) {
	commands := servicePathFaultCommands()
	for target := range commands {
		for _, op := range []byte{3, 6} {
			a, tr, b := newServiceFault(t, 0, false, false)
			goBackend := newGoServiceStateBackend()
			oracle := newServiceAuthority(goBackend)
			for i, c := range commands {
				a.clocks[1] = time.Now()
				oracle.clocks[1] = a.clocks[1]
				if i == target {
					tr.op = op
					tr.fired = false
					tr.corrupt = true
				}
				if e := a.pathCommand(c); e != nil {
					t.Fatalf("target%d/op%d/action%d: %v", target, op, c.Action, e)
				}
				if e := oracle.pathCommand(c); e != nil {
					t.Fatal(e)
				}
				g, e := a.pathState()
				want, we := oracle.pathState()
				if e != nil || we != nil || !reflect.DeepEqual(g, want) {
					t.Fatalf("action%d state differs %v %v", c.Action, e, we)
				}
			}
			s, e := b.Read()
			if e != nil || s.Revision != uint64(len(commands)) || tr.counts[3] != len(commands) || tr.counts[6] != len(commands) || tr.counts[4] != 1 {
				t.Fatal("replay or lost acknowledgement", tr.counts, s, e)
			}
			_ = a.Close()
			_ = oracle.Close()
		}
	}
}

type servicePathBadExport struct {
	serviceStateBackend
	mutate func(*servicePathState)
}

func (b *servicePathBadExport) Export() (serviceStateCheckpoint, error) {
	cp, e := b.serviceStateBackend.Export()
	if e != nil {
		return cp, e
	}
	p, e := decodeServicePathState(cp.Snapshot.PathPayload)
	if e != nil {
		return cp, e
	}
	b.mutate(&p)
	cp.Snapshot.PathPayload, e = encodeServicePathState(p)
	return cp, e
}
func TestServiceStatePathCheckpointEveryField(t *testing.T) {
	mutations := []func(*servicePathState){
		func(p *servicePathState) { p.Config.Enabled = !p.Config.Enabled }, func(p *servicePathState) { p.Config.IntervalSec++ }, func(p *servicePathState) {
			p.Config.Targets = []pathTarget{{Label: "x", URL: "x", Enabled: true, Builtin: true}}
		}, func(p *servicePathState) { p.Busy = true }, func(p *servicePathState) { p.MonitorActive = true }, func(p *servicePathState) { p.Status.Running = true }, func(p *servicePathState) { p.Status.ActiveNode = "x" }, func(p *servicePathState) {
			p.Status.Results = []siteCheckResult{{Name: "x", State: "x", HTTPCode: 429, LatencyMs: 3, Detail: "x"}}
		}, func(p *servicePathState) { p.Status.Failures = []string{"x"} }, func(p *servicePathState) { p.Status.Switched = []string{"x"} }, func(p *servicePathState) { p.Status.Note = "x" }, func(p *servicePathState) { p.Status.AlertID++ }, func(p *servicePathState) { p.Status.Alert = "x" }, func(p *servicePathState) { p.Status.RetryAt = "x" }, func(p *servicePathState) { p.Status.RetryMode = "x" }, func(p *servicePathState) { p.Status.Demanded = true }, func(p *servicePathState) { p.Status.Phase = "x" }, func(p *servicePathState) { p.AtToken = 99 }, func(p *servicePathState) { p.Generation++ }, func(p *servicePathState) { p.NextLease++ }, func(p *servicePathState) { p.LeaseID++ }, func(p *servicePathState) { p.Previous = []servicePathSample{{ID: "x", Upload: 3}} }, func(p *servicePathState) { p.Throttle = []servicePathThrottle{{Label: "x", TimeToken: 9}} }}
	for i, m := range mutations {
		a, _, b := newServiceFault(t, 0, false, false)
		bad := &servicePathBadExport{serviceStateBackend: b, mutate: m}
		a.backend = bad
		if e := a.transferToGo(); e == nil {
			t.Fatal("corrupt checkpoint transferred", i)
		}
		if a.backend != bad {
			t.Fatal("authority changed", i)
		}
		_ = a.Close()
	}
}
func TestServiceStatePathActiveLeaseNoHandoff(t *testing.T) {
	a, _, _ := newServiceFault(t, 0, false, false)
	if e := a.pathCommand(servicePathCommand{Action: pathStateBeginRound}); e != nil {
		t.Fatal(e)
	}
	if e := a.transferToGo(); e == nil {
		t.Fatal("active lease transferred")
	}
	if e := a.pathCommand(servicePathCommand{Action: pathStateFinishRound, LeaseID: 1}); e != nil {
		t.Fatal(e)
	}
	if e := a.transferToGo(); e != nil {
		t.Fatal(e)
	}
}

func TestServiceStatePathLargeReadSizing(t *testing.T) {
	base, e := newRustServiceStateBackend()
	if e != nil {
		t.Fatal(e)
	}
	b := base.(*rustServiceStateBackend)
	tr := b.transport.(*nativeServiceStateTransport)
	a := newServiceAuthority(b)
	defer a.Close()
	note := strings.Repeat("long-state-", 20000)
	if e = a.pathCommand(servicePathCommand{Action: pathStateNote, Text: note}); e != nil {
		t.Fatal(e)
	}
	p, e := a.pathState()
	if e != nil || p.Status.Note != note {
		t.Fatal("resized read lost payload", e)
	}
	cp, e := b.Export()
	if e != nil || cp.Snapshot.Revision != 1 || len(tr.buffer) <= 4096 || len(tr.buffer) > 32<<20 {
		t.Fatal("read size/revision", e, len(tr.buffer))
	}
	st, e := b.CommandStatus(cp.LastCommand)
	if e != nil || !st.Found || st.Pending || st.Command != cp.LastCommand {
		t.Fatal("resized status", e)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	if tr.id != 0 || tr.buffer != nil {
		t.Fatal("closed buffer retained")
	}
}

type pathStateTraceFX struct {
	*monitorDiffFX
	lastKind string
	lastBool bool
}

func (f *pathStateTraceFX) Call(e pathMonitorServiceEffect) pathMonitorServiceReply {
	r := f.monitorDiffFX.Call(e)
	f.lastKind = e.Kind
	f.lastBool = r.Bool
	return r
}
func TestServiceStatePathMonitorFalseFlagsParity(t *testing.T) {
	s := monitorDiffBase()
	s.Running = false
	s.Enabled = false
	s.Busy = false
	s.Cancel = true
	f := &pathStateTraceFX{monitorDiffFX: monitorDiffNew(monitorDiffRequest("loop"), s)}
	n := pathMonitorFlowRecoveries.Load()
	_, used := runRustPathMonitorService(monitorDiffRequest("loop"), f)
	if !used || pathMonitorFlowRecoveries.Load() != n {
		t.Fatalf("recovery %v last=%s bool=%v trace=%#v", pathMonitorFlowRecoveryReason.Load(), f.lastKind, f.lastBool, f.trace)
	}
}

func TestServiceStatePathLongClockLedger(t *testing.T) {
	authority, _, _ := newServiceFault(t, 0, false, false)
	application := &app{service: authority}
	application.mu.Lock()
	defer application.mu.Unlock()
	if e := authority.pathCommand(servicePathCommand{Action: pathStateOpenMonitor}); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	for i := 0; i < 15000; i++ {
		at := now.Add(time.Duration(i) * time.Millisecond)
		application.pathClockCommandLocked(pathStateAt, at, "", 0)
		application.pathClockCommandLocked(pathStateThrottle, at, "same-label", 1)
		if len(authority.clocks) > 2 {
			t.Fatal("path time ledger grew", i, len(authority.clocks))
		}
	}
	p, e := authority.pathState()
	if e != nil || len(p.Throttle) != 1 || len(authority.clocks) != 2 {
		t.Fatal("path references lost", e, len(authority.clocks))
	}
	if e = authority.pathCommand(servicePathCommand{Action: pathStateRevokeMonitor}); e != nil {
		t.Fatal(e)
	}
	if len(authority.clocks) != 1 {
		t.Fatal("revoked throttle clock retained", len(authority.clocks))
	}
}
