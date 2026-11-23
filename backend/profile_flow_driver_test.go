package main

import (
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type profileFaultEffects struct {
	running   bool
	trace     []profileEffect
	fail      string
	panicKind string
	seen      int
}

func (f *profileFaultEffects) Call(q profileEffect, s *profileServiceState) profileReply {
	f.trace = append(f.trace, q)
	r := profileReply{}
	switch q.Kind {
	case "running":
		r.Bool = f.running
	case "read":
		r.Body = []byte("synthetic")
	case "parse":
		r.Nodes = []proxyNode{{Name: "node"}}
	case "merge-sources":
		r.Body = []byte("merged")
		r.Nodes = []proxyNode{{Name: "node"}}
	case "prepare-live-config":
		r.Text = "synthetic-config"
	}
	if q.Kind == f.panicKind {
		s.CachedNodes = []proxyNode{{Name: "partial"}}
		panic("synthetic")
	}
	if q.Kind == f.fail {
		r.Err = errors.New("synthetic failure")
	}
	return r
}
func profileFaultState() profileServiceState {
	return profileServiceState{settingsServiceState: subscriptionTestState(), Home: "synthetic-home", ProfilePath: "synthetic-profile"}
}
func profileFakeFrame(kind byte, a, b, c int64, data, text string, items []int64) []byte {
	v := registryInt([]byte{kind}, a)
	v = registryInt(v, b)
	v = registryInt(v, c)
	v = registryBytes(v, data)
	v = registryBytes(v, text)
	return profileIntegers(v, items)
}
func TestProfileFlowFaultContinuationExactlyOnce(t *testing.T) {
	for _, op := range []string{"use-cache", "replace", "cache-id"} {
		for _, failure := range []string{"", "write", "rename", "save-nodes"} {
			a := profileFaultState()
			b := profileCloneState(a)
			q := profileServiceRequest{Op: op, ID: "old", Path: a.ProfilePath, Body: []byte("synthetic"), Format: "yaml"}
			x := &profileFaultEffects{fail: failure}
			y := &profileFaultEffects{fail: failure}
			want := runGoProfileService(&a, q, x)
			first := profileFakeFrame(2, 1, 0, 0, "old", "", nil)
			if op == "replace" {
				first = profileFakeFrame(3, 4, 1, 0, "", "", nil)
			}
			if op == "cache-id" {
				first = profileFakeFrame(1, 1, 0, 0, "old", "", nil)
			}
			fs := &registryFakeSession{frames: [][]byte{first}, failStep: 1}
			got, used := runRustProfileServiceWithStart(&b, q, y, func([]byte) (profileFlowSession, error) { return fs, nil })
			if !used || !fs.closed || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || (got.Err != nil) != (want.Err != nil) {
				t.Fatal(op, failure, got, want, x.trace, y.trace)
			}
		}
	}
}
func TestProfileFlowPanicDoesNotContinueUnknownEffect(t *testing.T) {
	s := profileFaultState()
	q := profileServiceRequest{Op: "use-cache", ID: "old"}
	fx := &profileFaultEffects{panicKind: "save-nodes"}
	fs := &registryFakeSession{frames: [][]byte{profileFakeFrame(2, 1, 0, 0, "old", "", nil)}, failStep: 1}
	result, used := runRustProfileServiceWithStart(&s, q, fx, func([]byte) (profileFlowSession, error) { return fs, nil })
	if !used || result.Err == nil || result.Status != 500 || s.CachedNodes[0].Name != "partial" || !fs.closed || fx.trace[len(fx.trace)-1].Kind != "save-nodes" {
		t.Fatal(result, s, fx.trace)
	}
}
func TestProfileFlowCancellationReclaimsWorkers(t *testing.T) {
	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1200; i++ {
			s := profileFaultState()
			fx := &profileFaultEffects{}
			fs := &registryFakeSession{frames: [][]byte{{255}}}
			_, used := runRustProfileServiceWithStart(&s, profileServiceRequest{Op: "use-cache", ID: "old"}, fx, func([]byte) (profileFlowSession, error) { return fs, nil })
			if used || len(fx.trace) != 0 || !fs.closed {
				panic("unexpected effect")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel join timed out")
	}
	runtime.GC()
	if n := runtime.NumGoroutine(); n > before+4 {
		t.Fatal("worker leak", before, n)
	}
}
func TestProfileFlowCapacityFailureBeforeEffects(t *testing.T) {
	s := profileFaultState()
	fx := &profileFaultEffects{}
	result, used := runRustProfileServiceWithStart(&s, profileServiceRequest{Op: "use-cache", ID: "old"}, fx, func([]byte) (profileFlowSession, error) { return nil, errors.New("capacity") })
	if used || result.Err != nil || len(fx.trace) != 0 {
		t.Fatal("capacity should permit pre-effect Go fallback")
	}
}

func TestProfileFlowLargeSnapshotFallbackBeforeEffects(t *testing.T) {
	s := profileFaultState()
	for len(s.Subscriptions) <= 256 {
		s.Subscriptions = append(s.Subscriptions, s.Subscriptions[0])
	}
	fx := &profileFaultEffects{}
	called := false
	_, used := runRustProfileServiceWithStart(&s, profileServiceRequest{Op: "merge-all"}, fx, func([]byte) (profileFlowSession, error) { called = true; return nil, errors.New("must not start") })
	if used || called || len(fx.trace) != 0 {
		t.Fatal("large snapshot was not rejected before effects")
	}
}
