//go:build windows && amd64 && rustcore

package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestServiceStateControlEveryFieldDeliveryFault(t *testing.T) {
	for bit := 0; bit < 18; bit++ {
		for _, op := range []byte{3, 6} {
			a, tr, b := newServiceFault(t, op, false, true)
			v := controlCodecFixture()
			c := serviceControlCommand{Action: 2, Mask: 1 << bit, Value: v}
			p, e := encodeServiceControlCommand(c)
			if e != nil {
				t.Fatal(e)
			}
			if e = a.mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
				t.Fatal(bit, op, e)
			}
			out, e := b.ControlQuery()
			if e != nil {
				t.Fatal(e)
			}
			got, e := decodeServiceControlState(out)
			var want serviceControlState
			_ = applyServiceControl(&want, c)
			if e != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("mismatch", bit, e)
			}
			if tr.counts[3] != 1 || tr.counts[6] != 1 || tr.counts[4] != 1 {
				t.Fatal("replay", tr.counts)
			}
			_ = a.Close()
		}
	}
}

type controlBadExport struct {
	serviceStateBackend
	field int
}

func (b *controlBadExport) Export() (serviceStateCheckpoint, error) {
	cp, e := b.serviceStateBackend.Export()
	if e != nil {
		return cp, e
	}
	v, e := decodeServiceControlState(cp.Snapshot.ControlPayload)
	if e != nil {
		return cp, e
	}
	f := reflect.ValueOf(&v).Elem().Field(b.field)
	switch f.Kind() {
	case reflect.Bool:
		f.SetBool(!f.Bool())
	case reflect.String:
		f.SetString(f.String() + "changed")
	case reflect.Int:
		f.SetInt(f.Int() + 1)
	case reflect.Slice:
		f.Set(reflect.ValueOf([]string{"changed"}))
	}
	cp.Snapshot.ControlPayload, e = encodeServiceControlState(v)
	return cp, e
}
func TestServiceStateControlCheckpointEveryField(t *testing.T) {
	for i := 0; i < 18; i++ {
		a, _, b := newServiceFault(t, 0, false, false)
		p, _ := encodeServiceControlCommand(serviceControlCommand{Action: 1, Mask: controlAll, Value: controlCodecFixture()})
		if e := a.mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
			t.Fatal(e)
		}
		bad := &controlBadExport{b, i}
		a.backend = bad
		if e := a.transferToGo(); e == nil {
			t.Fatal("corrupt checkpoint accepted", i)
		}
		if a.backend != bad {
			t.Fatal("owner changed")
		}
		_ = a.Close()
	}
}
func TestServiceStateControlLargeQueryAndPayloadConflict(t *testing.T) {
	a, tr, b := newServiceFault(t, 0, false, false)
	v := controlCodecFixture()
	v.GameBypass = []string{strings.Repeat("x", 250000)}
	p, _ := encodeServiceControlCommand(serviceControlCommand{Action: 1, Mask: controlAll, Value: v})
	if e := a.mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
		t.Fatal(e)
	}
	out, e := b.ControlQuery()
	got, de := decodeServiceControlState(out)
	if e != nil || de != nil || !reflect.DeepEqual(got, v) {
		t.Fatal("large query", e, de)
	}
	cp, e := b.Export()
	if e != nil {
		t.Fatal(e)
	}
	bad := cp.LastCommand
	bad.ControlPayload += "x"
	if _, e = b.Apply(bad); e == nil {
		t.Fatal("same ID different payload")
	}
	after, e := b.Export()
	if e != nil || after != cp {
		t.Fatal("changed on reject", e)
	}
	if tr.counts[9] != 1 {
		t.Fatal("query replay")
	}
	m, e := b.Meta()
	if e != nil || m.ControlPayload != "" || m.HealthPayload != "" {
		t.Fatal("wide meta", e)
	}
}

func TestServiceStateControlScalarUpdatesKeepLargeList(t *testing.T) {
	a, tr, b := newServiceFault(t, 0, false, false)
	initial := serviceControlState{GameBypass: make([]string, 10000)}
	for i := range initial.GameBypass {
		initial.GameBypass[i] = "opaque-rule"
	}
	p, _ := encodeServiceControlCommand(serviceControlCommand{Action: 1, Mask: controlAll, Value: initial})
	if e := a.mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5000; i++ {
		m, e := b.ControlMeta()
		if e != nil || len(m) > 1024 {
			t.Fatal("wide control meta", len(m), e)
		}
		p, _ = encodeServiceControlCommand(serviceControlCommand{Action: 2, Mask: controlAutoMergeHours, Value: serviceControlState{AutoMergeHours: i}})
		if e := a.mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
			t.Fatal(e)
		}
	}
	if tr.counts[1] != 0 || tr.counts[5] != 0 || tr.counts[9] != 0 || len(a.clocks) != 0 {
		t.Fatal("wide read or clock growth", tr.counts, len(a.clocks))
	}
	raw, e := b.ControlQuery()
	v, de := decodeServiceControlState(raw)
	if e != nil || de != nil || v.AutoMergeHours != 4999 || !reflect.DeepEqual(v.GameBypass, initial.GameBypass) {
		t.Fatal("narrow field lost list", e, de)
	}
}
func TestServiceStateControlCorruptReadFailClosed(t *testing.T) {
	a, tr, _ := newServiceFault(t, 9, false, true)
	app := &app{service: a}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("bad control read accepted")
			}
		}()
		app.controlLocked()
	}()
	if tr.counts[3] != 0 || a.backend == nil {
		t.Fatal("read caused mutation or changed authority")
	}
}

// These selector regressions exercise native planners; their literal app fixtures
// intentionally retain the default Go authority. The separate authority suite
// covers native owner plus native planner combinations.
func TestServiceStateControlSafeNativeSelectors(t *testing.T) {
	if !rustChainFlowAvailable() || !rustProfileFlowAvailable() {
		t.Fatal("required native planners unavailable")
	}
	t.Setenv("SMARTVPN_RUST_CHAIN_FLOW", "1")
	t.Setenv("SMARTVPN_RUST_PROFILE_FLOW", "1")
	beforeChain, beforeProfile := chainFlowRecoveries.Load(), profileFlowRecoveries.Load()
	t.Run("chain-save-panic-rollback", TestServiceStateSafeControlChainSaveRollback)
	t.Run("profile-same-value-intent", TestServiceStateSafeControlProfileSameValueIntent)
	if chainFlowRecoveries.Load() != beforeChain || profileFlowRecoveries.Load() != beforeProfile {
		t.Fatal("normal native planner continuation fallback")
	}
}
