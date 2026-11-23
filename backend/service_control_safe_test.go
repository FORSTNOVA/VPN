package main

import (
	"errors"
	"testing"
)

func TestServiceStateSafeControlHealthIntent(t *testing.T) {
	a := &app{}
	a.patchControlLocked(controlRuntimeLockedNode|controlLockedNode, serviceControlState{RuntimeLockedNode: "old", LockedNode: "old"})
	s := healthServiceState{LockedNode: "old", SettingsLockedNode: "old"}
	fx := &ownedHealthControlEffects{appHealthEffects: appHealthEffects{a}, previous: s}
	a.patchControlLocked(controlRuntimeLockedNode|controlLockedNode, serviceControlState{RuntimeLockedNode: "new", LockedNode: "new"})
	fx.CommitControl(s)
	if a.controlLocked().RuntimeLockedNode != "new" {
		t.Fatal("unrelated callback restored stale pin")
	}
	fx.CommitPin(s)
	if a.controlLocked().RuntimeLockedNode != "old" || a.controlLocked().LockedNode != "old" {
		t.Fatal("explicit same-value pin intent lost")
	}
}
func TestServiceStateSafeControlDecodedRuntimePreserved(t *testing.T) {
	a := &app{}
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "runtime"})
	a.restoreDecodedControlLocked(settings{SelectedNode: "decoded", LockedNode: "stored"})
	v := a.controlLocked()
	if v.RuntimeLockedNode != "runtime" || v.LockedNode != "stored" || v.SelectedNode != "decoded" {
		t.Fatalf("wrong decoded control: %+v", v)
	}
}

type controlLostAck struct {
	serviceStateBackend
	calls int
}

func (b *controlLostAck) Apply(c serviceStateCommand) (serviceStateAck, error) {
	b.calls++
	a, e := b.serviceStateBackend.Apply(c)
	if e != nil {
		return a, e
	}
	return a, errors.New("lost")
}
func (b *controlLostAck) CommandStatus(c serviceStateCommand) (serviceStateCommandStatus, error) {
	return serviceStateCommandStatus{}, errors.New("unavailable")
}
func TestServiceStateSafeControlUnresolvedNoReplay(t *testing.T) {
	b := &controlLostAck{serviceStateBackend: newGoServiceStateBackend()}
	a := &app{service: newServiceAuthority(b)}
	for i := 0; i < 2; i++ {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("unconfirmed command accepted")
				}
			}()
			a.patchControlLocked(controlSelectedNode, serviceControlState{SelectedNode: "selected"})
		}()
	}
	if b.calls != 1 || a.service.pending == nil {
		t.Fatal("unresolved command replayed")
	}
	if e := a.service.transferToGo(); e == nil {
		t.Fatal("uncertain checkpoint transferred")
	}
}

func TestServiceStateSafeControlClosedWorkflowBoundaries(t *testing.T) {
	makeApp := func() *app { a := &app{}; _ = a.closeServiceStateLocked(); return a }
	t.Run("connection", func(t *testing.T) {
		a := makeApp()
		r := a.runConnectionService(connectionServiceRequest{Op: "connect"})
		if r.Err == nil || r.Status != 500 {
			t.Fatalf("%+v", r)
		}
		if !a.mu.TryLock() {
			t.Fatal("lock leaked")
		}
		a.mu.Unlock()
	})
	t.Run("chain", func(t *testing.T) {
		a := makeApp()
		r := a.runChainService(chainServiceRequest{Op: "update"})
		if r.Err == nil || r.Status != 500 {
			t.Fatalf("%+v", r)
		}
		if !a.mu.TryLock() {
			t.Fatal("lock leaked")
		}
		a.mu.Unlock()
	})
	t.Run("subscription", func(t *testing.T) {
		a := makeApp()
		a.mu.Lock()
		r := a.runSubscriptionService(subscriptionServiceRequest{Op: "list"})
		if r.Err == nil || r.Status != 500 {
			t.Fatalf("%+v", r)
		}
		if a.mu.TryLock() {
			t.Fatal("borrowed lock lost")
		}
		a.mu.Unlock()
	})
	t.Run("profile", func(t *testing.T) {
		a := makeApp()
		r := a.runProfileService(profileServiceRequest{Op: "auto-tick"})
		if r.Err == nil || r.Status != 500 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("auto-tick", func(t *testing.T) {
		a := makeApp()
		a.autoMergeTickSafe()
		if !a.mu.TryLock() {
			t.Fatal("auto tick lock leaked")
		}
		a.mu.Unlock()
	})
	t.Run("kernel", func(t *testing.T) {
		a := makeApp()
		a.mu.Lock()
		r := a.runKernelService(kernelServiceRequest{Op: "start"})
		if r.Err == nil {
			t.Fatal("missing failure")
		}
		if a.mu.TryLock() {
			t.Fatal("borrowed kernel lock lost")
		}
		a.mu.Unlock()
	})
}
func TestServiceStateSafeControlBatchPanicJoin(t *testing.T) {
	rs := profileAcquireBatch([]subscriptionEntry{{ID: "one"}, {ID: "two"}}, func(en subscriptionEntry) profileAcquisitionResult { panic("synthetic") })
	if len(rs) != 2 || rs[0].Err == nil || rs[1].Err == nil {
		t.Fatal("worker failures not joined")
	}
}

type controlQueryFailure struct{ serviceStateBackend }

func (b *controlQueryFailure) ControlQuery() (string, error) {
	return "", errors.New("injected full control read failure")
}
func TestServiceStateSafeControlChainSaveRollback(t *testing.T) {
	for _, op := range []string{"update", "save-profile"} {
		t.Run(op, func(t *testing.T) {
			q := chainServiceRequest{Op: op, Name: "new", Original: "old", Hops: []string{"a", "b"}, Next: proxyChain{Enabled: false}}
			f := chainDiffNew(q, chainDiffBase())
			a := f.Base.a
			hops := make([]string, 2, 5)
			copy(hops, []string{"a", "b"})
			old := map[string][]string{"old": hops}
			a.settings.Chain = proxyChain{Name: "old", Enabled: true, Hops: hops}
			a.settings.Chains = old
			saves := 0
			f.Base.save = func() error {
				saves++
				a.patchControlLocked(controlAutoMergeHours, serviceControlState{AutoMergeHours: 123})
				a.service.backend = &controlQueryFailure{serviceStateBackend: a.service.backend}
				_ = a.settingsDocumentLocked()
				return nil
			}
			r := runSelectedChainService(q, f.Base)
			if r.Status != 500 || r.Err == nil || saves != 1 {
				t.Fatalf("result %+v saves %d", r, saves)
			}
			old["visible"] = []string{"yes"}
			if a.settings.Chains["visible"] == nil || &a.settings.Chain.Hops[0] != &hops[0] || cap(a.settings.Chain.Hops) != 5 {
				t.Fatal("rollback lost graph identity")
			}
			if a.controlMetaLocked().AutoMergeHours != 123 {
				t.Fatal("chain rollback reverted control")
			}
			if !a.mu.TryLock() {
				t.Fatal("chain lock leaked")
			}
			a.mu.Unlock()
		})
	}
}

type profileControlIntentFX struct {
	a       *app
	memory  profileMemoryEffects
	changed bool
	saves   int
}

func (f *profileControlIntentFX) Call(q profileEffect, s *profileServiceState) profileReply {
	if q.Kind == "write" && !f.changed {
		f.changed = true
		f.a.patchControlLocked(controlSubscriptionURL, serviceControlState{SubscriptionURL: "interleaved"})
	}
	if q.Kind == "save-settings" {
		f.saves++
		applySettingsServiceState(f.a, s.settingsServiceState)
	}
	return f.memory.Call(q, s)
}
func TestServiceStateSafeControlProfileSameValueIntent(t *testing.T) {
	a := &app{}
	a.patchControlLocked(controlSubscriptionURL, serviceControlState{SubscriptionURL: mergedSubscriptionURL})
	s := profileTestState()
	s.settingsServiceState = settingsStateFromApp(a)
	f := &profileControlIntentFX{a: a, memory: profileMemoryEffects{Files: map[string][]byte{}, Nodes: []proxyNode{{Name: "one"}}}}
	r := runSelectedProfileService(&s, profileServiceRequest{Op: "write-merged"}, f)
	if r.Err != nil || f.saves != 1 || a.controlLocked().SubscriptionURL != mergedSubscriptionURL {
		t.Fatalf("same value intent lost: %+v saves %d", r, f.saves)
	}
}
