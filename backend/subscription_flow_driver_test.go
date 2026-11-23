package main

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type registryFakeSession struct {
	frames       [][]byte
	at, failStep int
	closed       bool
}

func (f *registryFakeSession) next() ([]byte, uint64, error) {
	if f.at >= len(f.frames) {
		return nil, 0, errors.New("synthetic EOF")
	}
	return f.frames[f.at], uint64(f.at + 1), nil
}
func (f *registryFakeSession) advance(_ uint64, _ []byte) error {
	if f.failStep == f.at+1 {
		return errors.New("synthetic advance failure")
	}
	f.at++
	return nil
}
func (f *registryFakeSession) close() { f.closed = true }
func registryFrame(kind byte, index, aux int64, data string) []byte {
	b := registryInt([]byte{kind}, index)
	b = registryInt(b, aux)
	b = registryBytes(b, data)
	return registryEntries(b, nil)
}

type registryPanicEffects struct {
	invariantOK bool
	base        subscriptionMemoryEffects
	kind        string
	live        *subscriptionServiceState
	seen        bool
}

func (f *registryPanicEffects) Call(kind, arg string, s *subscriptionServiceState) subscriptionEffectReply {
	if kind == f.kind {
		f.seen = true
		f.invariantOK = true
		if kind == "write-registry" {
			if f.live.Subscriptions[0].URL != "https://old.test/" || s.Subscriptions[0].URL != "sealed:https://old.test/" {
				f.invariantOK = false
				panic("sealed copy contaminated live graph")
			}
		}
		panic("synthetic effect panic")
	}
	return f.base.Call(kind, arg, s)
}
func TestRegistryFlowSavePanicsReturnErrorAndKeepLivePlain(t *testing.T) {
	for _, kind := range []string{"seal-secret", "write-registry"} {
		s := subscriptionTestState()
		fx := &registryPanicEffects{kind: kind, live: &s}
		f := &registryFakeSession{frames: [][]byte{registryFrame(4, 0, 0, s.Subscriptions[0].URL), registryFrame(5, -1, 0, "")}}
		result, used := runRustSubscriptionServiceWithStart(&s, subscriptionServiceRequest{Op: "save"}, fx, func([]byte) (subscriptionFlowSession, error) { return f, nil })
		if !used || result.Err == nil || !fx.seen || !fx.invariantOK || !f.closed || s.Subscriptions[0].URL != "https://old.test/" {
			t.Fatal(kind, result, s)
		}
	}
}
func TestRegistryFlowContinuationDoesNotResealOrRewrite(t *testing.T) {
	for _, failure := range []bool{false, true} {
		for _, failStep := range []int{1, 2} {
			s := subscriptionTestState()
			fx := &subscriptionMemoryEffects{Fail: map[string]int{}}
			if failure {
				fx.Fail["write-registry"] = 1
			}
			f := &registryFakeSession{frames: [][]byte{registryFrame(4, 0, 0, s.Subscriptions[0].URL), registryFrame(5, -1, 0, "")}, failStep: failStep}
			result, used := runRustSubscriptionServiceWithStart(&s, subscriptionServiceRequest{Op: "save"}, fx, func([]byte) (subscriptionFlowSession, error) { return f, nil })
			if !used || (result.Err != nil) != failure || !reflect.DeepEqual(fx.Trace, []string{"seal-secret:https://old.test/", "write-registry:"}) || s.Subscriptions[0].URL != "https://old.test/" {
				t.Fatal(result, fx.Trace, s)
			}
		}
	}
}
func TestRegistryFlowDeepClonePreservesTimeAndOwnership(t *testing.T) {
	s := subscriptionTestState()
	now := time.Now()
	s.Health = map[string]nodeHealth{"x": {Name: "x", UpdatedAt: now}}
	s.Settings.GameBypass = []string{"game"}
	s.Settings.Chains = map[string][]string{"chain": {"x"}}
	s.Subscriptions[0].UserInfo = &subscriptionUserInfo{Total: 99}
	v := subscriptionCloneState(s)
	v.Settings.GameBypass[0] = "other"
	v.Settings.Chains["chain"][0] = "other"
	v.Subscriptions[0].UserInfo.Total = 0
	h := v.Health["x"]
	if h.UpdatedAt != now {
		t.Fatal("monotonic timestamp lost")
	}
	if s.Settings.GameBypass[0] != "game" || s.Settings.Chains["chain"][0] != "x" || s.Subscriptions[0].UserInfo.Total != 99 {
		t.Fatal("aliased mutable state")
	}
}
func TestRegistryFlowCancellationReclaimsWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1200; i++ {
			s := subscriptionTestState()
			fx := &subscriptionMemoryEffects{}
			f := &registryFakeSession{frames: [][]byte{{255}}}
			_, used := runRustSubscriptionServiceWithStart(&s, subscriptionServiceRequest{Op: "save"}, fx, func([]byte) (subscriptionFlowSession, error) { return f, nil })
			if used || len(fx.Trace) != 0 || !f.closed {
				panic("cancel path executed effect")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("mirror cancellation leaked/deadlocked")
	}
	runtime.GC()
	if got := runtime.NumGoroutine(); got > baseline+4 {
		t.Fatalf("workers grew %d -> %d", baseline, got)
	}
}
func TestRegistryFlowLargeLoadTransfersContinuationOnce(t *testing.T) {
	s := subscriptionTestState()
	s.Settings.SubscriptionURL = ""
	fx := &subscriptionMemoryEffects{}
	for i := 0; i < 1200; i++ {
		fx.Read = append(fx.Read, subscriptionLoadedEntry{Entry: subscriptionEntry{ID: fmt.Sprint(i), URL: fmt.Sprintf("https://%d.test", i), Label: "label"}, HasEnabled: true})
	}
	f := &registryFakeSession{frames: [][]byte{registryFrame(1, -1, 0, "")}}
	result, used := runRustSubscriptionServiceWithStart(&s, subscriptionServiceRequest{Op: "load"}, fx, func([]byte) (subscriptionFlowSession, error) { return f, nil })
	if !used || result.Status != 200 || len(s.Subscriptions) != 1200 || len(fx.Trace) != 1202 || fx.Trace[0] != "read-registry:" || fx.Trace[len(fx.Trace)-1] != "start-loop:" {
		t.Fatal(result, len(s.Subscriptions), len(fx.Trace))
	}
}
func TestRegistryFlowLargeSnapshotFallsBackBeforeStart(t *testing.T) {
	s := subscriptionTestState()
	for len(s.Subscriptions) <= 256 {
		s.Subscriptions = append(s.Subscriptions, s.Subscriptions[0])
	}
	called := false
	_, used := runRustSubscriptionServiceWithStart(&s, subscriptionServiceRequest{Op: "save"}, &subscriptionMemoryEffects{}, func([]byte) (subscriptionFlowSession, error) { called = true; return nil, nil })
	if used || called {
		t.Fatal("large snapshot entered bridge")
	}
}

type registryPartialPanic struct{ trace []string }

func (f *registryPartialPanic) Call(kind, arg string, s *subscriptionServiceState) subscriptionEffectReply {
	f.trace = append(f.trace, kind)
	if kind == "fetch" {
		s.CachedNodes = []proxyNode{{Name: "confirmed-partial"}}
		s.Settings.SelectedNode = "partial-choice"
		panic("synthetic")
	}
	return subscriptionEffectReply{}
}
func TestRegistryFlowCompositePanicPreservesPartialWithoutContinuation(t *testing.T) {
	s := subscriptionTestState()
	fx := &registryPartialPanic{}
	f := &registryFakeSession{frames: [][]byte{registryFrame(6, -1, 0, "")}, failStep: 1}
	result, used := runRustSubscriptionServiceWithStart(&s, subscriptionServiceRequest{Op: "activate", ID: "old"}, fx, func([]byte) (subscriptionFlowSession, error) { return f, nil })
	if !used || result.Err == nil || s.CachedNodes[0].Name != "confirmed-partial" || s.Settings.SelectedNode != "partial-choice" || !f.closed {
		t.Fatal(result, s, fx.trace)
	}
	if fx.trace[len(fx.trace)-1] != "fetch" {
		t.Fatal("effects continued after uncertain panic", fx.trace)
	}
}
func TestRegistryFlowDeleteAndActivateFaultKeepRollbackParity(t *testing.T) {
	for _, op := range []string{"delete", "activate"} {
		for _, fail := range []string{"write-registry", "fetch", "use-cache", "merge-caches"} {
			a := subscriptionTestState()
			b := subscriptionCloneState(a)
			x := &subscriptionMemoryEffects{Fail: map[string]int{fail: 1}, CachePartial: true}
			y := &subscriptionMemoryEffects{Fail: map[string]int{fail: 1}, CachePartial: true}
			req := subscriptionServiceRequest{Op: op, ID: "old"}
			want := runGoSubscriptionService(&a, req, x)
			f := &registryFakeSession{frames: [][]byte{registryFrame(6, -1, 0, "")}, failStep: 1}
			got, used := runRustSubscriptionServiceWithStart(&b, req, y, func([]byte) (subscriptionFlowSession, error) { return f, nil })
			if !used || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.Trace, y.Trace) || got.Status != want.Status || got.Message != want.Message {
				t.Fatal(op, fail, got, want, x.Trace, y.Trace)
			}
		}
	}
}
