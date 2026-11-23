package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type chainDiffSpec struct {
	State        chainServiceState
	Manual       []manualNode
	Regions      map[string]regionRecord
	Proxies      []map[string]any
	Running, Job bool
	Fail         map[string]error
	DecodeFailAt int
	Now          time.Time
}
type chainDiffEvent struct {
	Effect          chainEffect
	State           *chainServiceState
	Owned, Borrowed bool
}
type chainDiffFX struct {
	Base       *appChainEffects
	Trace      []chainDiffEvent
	Calls      map[string]int
	ClockCalls int
	Spec       chainDiffSpec
}

func chainDiffBase() chainDiffSpec {
	now := time.Date(2043, 4, 5, 6, 7, 8, 999999999, time.FixedZone("east", 8*3600))
	return chainDiffSpec{State: chainServiceState{Subscription: true, Chain: proxyChain{Name: "old", Enabled: true, Hops: []string{"a", "b"}}, Chains: map[string][]string{"old": {"a", "b"}, "nil": nil, "empty": {}}}, Regions: map[string]regionRecord{"b": {Name: "b", Country: "US", Status: regionVerified, VerifiedAt: now.Add(-time.Hour)}}, Proxies: []map[string]any{{"name": "a", "type": "trojan", "server": "a.invalid", "nested": map[string]any{"path": "/"}}, {"name": "b", "type": "trojan", "server": "b.invalid"}}, Fail: map[string]error{}, Now: now}
}
func chainDiffCloneProxies(ps []map[string]any) []map[string]any {
	if ps == nil {
		return nil
	}
	out := make([]map[string]any, len(ps))
	for i, p := range ps {
		out[i] = cloneManualValue(p).(map[string]any)
	}
	return out
}
func chainCloneRequest(q chainServiceRequest) chainServiceRequest {
	q.Hops = appendPreserveChainSlice(q.Hops)
	q.Next.Hops = appendPreserveChainSlice(q.Next.Hops)
	q.Profile = append([]byte(nil), q.Profile...)
	q.Manual = cloneManualNodes(q.Manual)
	if q.Index != nil {
		idx := map[string][]map[string]any{}
		for k, v := range q.Index {
			idx[k] = chainDiffCloneProxies(v)
		}
		q.Index = idx
	}
	return q
}
func chainDiffNew(q chainServiceRequest, s chainDiffSpec) *chainDiffFX {
	q = chainCloneRequest(q)
	var a *app
	if q.Op == "update" || q.Op == "save-profile" || q.Op == "build" {
		state := cloneChainServiceState(&s.State)
		a = testHealthApp(testControlApp(&app{manualNodes: cloneManualNodes(s.Manual)}, func(a *app) {
			a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: state.LockedRegion})
		}), func(p *healthDomainProjection) {
			p.Regions = cloneRegionServiceState(&regionServiceState{Regions: s.Regions}).Regions
		})
		a.settings.Chain = state.Chain
		a.settings.Chains = state.Chains
		if state.Subscription {
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
		}
		if q.Op == "build" {
			a.mu.Lock()
		}
	}
	f := &chainDiffFX{Base: newAppChainEffects(a, q), Calls: map[string]int{}, Spec: s}
	f.Base.running = func() bool { return s.Running }
	f.Base.job = func() bool { return s.Job }
	f.Base.read = func() ([]byte, error) { return []byte("synthetic-profile"), s.Fail["read"] }
	f.Base.decode = func(b []byte) ([]map[string]any, error) {
		if err := s.Fail["decode"]; err != nil && (s.DecodeFailAt == 0 || s.DecodeFailAt == f.Calls["decode"]) {
			return nil, err
		}
		if b == nil {
			return nil, nil
		}
		return chainDiffCloneProxies(s.Proxies), nil
	}
	f.Base.save = func() error { return s.Fail["save"] }
	f.Base.now = func() time.Time { f.ClockCalls++; return s.Now }
	encode := f.Base.encode
	f.Base.encode = func(p []map[string]any) ([]byte, error) {
		if err := s.Fail["yaml"]; err != nil {
			return nil, err
		}
		return encode(p)
	}
	return f
}
func (f *chainDiffFX) capture(e chainEffect) {
	var state *chainServiceState
	if f.Base.a != nil {
		state = cloneChainServiceState(chainStateFromApp(f.Base.a))
	}
	f.Trace = append(f.Trace, chainDiffEvent{e, state, f.Base.owned, f.Base.borrowed})
}
func (f *chainDiffFX) Call(e chainEffect) chainReply {
	f.Calls[e.Kind]++
	input := e
	input.Body = append([]byte(nil), e.Body...)
	input.Proxies = chainDiffCloneProxies(e.Proxies)
	input.Next.Hops = appendPreserveChainSlice(e.Next.Hops)
	r := f.Base.Call(e)
	f.capture(input)
	return r
}
func (f *chainDiffFX) Release() {
	owned := f.Base.owned
	f.Base.Release()
	if owned {
		f.capture(chainEffect{Kind: "release"})
	}
}
func (f *chainDiffFX) FinalizeChainResult(q chainServiceRequest, r chainServiceResult) chainServiceResult {
	return f.Base.FinalizeChainResult(q, r)
}
func TestMigrationChainSafeSelectedLifecycle(t *testing.T) {
	type out struct {
		Results []chainServiceResult
		Trace   []chainDiffEvent
		State   *chainServiceState
	}
	execute := func(selected bool) out {
		s := chainDiffBase()
		s.State.Chain.Enabled = false
		var a *app
		o := out{}
		for _, q := range []chainServiceRequest{{Op: "save-profile", Name: "new", Hops: []string{"a", "b"}}, {Op: "update", Next: proxyChain{Name: "new", Enabled: true, Hops: []string{"a", "b"}}}, {Op: "build", Next: proxyChain{Enabled: true, Hops: []string{"a", "b"}}, Profile: []byte("synthetic")}, {Op: "update", Next: proxyChain{Enabled: false}}} {
			f := chainDiffNew(q, s)
			if a != nil {
				if q.Op == "build" {
					f.Base.a.mu.Unlock()
					a.mu.Lock()
				}
				f.Base.a = a
			}
			a = f.Base.a
			var r chainServiceResult
			if selected {
				r = runSelectedChainService(q, f)
			} else {
				r = runGoChainService(q, f)
			}
			r = f.FinalizeChainResult(q, r)
			if r.Err != nil {
				t.Fatalf("selector lifecycle failed %s %v", q.Op, r.Err)
			}
			if q.Op == "build" {
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("build unlocked caller")
				}
				a.mu.Unlock()
				if len(r.Body) == 0 {
					t.Fatal("build produced no YAML")
				}
			}
			o.Results = append(o.Results, r)
			o.Trace = append(o.Trace, f.Trace...)
		}
		o.State = cloneChainServiceState(chainStateFromApp(a))
		if o.State.Chain.Enabled || !reflect.DeepEqual(o.State.Chains["new"], []string{"a", "b"}) {
			t.Fatal("lifecycle did not save/disable")
		}
		if !a.mu.TryLock() {
			t.Fatal("selector lock leak")
		}
		a.mu.Unlock()
		return o
	}
	want, got := execute(false), execute(true)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("selector mismatch\nGo=%+v\nSelected=%+v", want, got)
	}
}

var _ = errors.Is

func chainLegacyIdentityContracts(t *testing.T, run func(chainServiceRequest, chainServiceEffects) chainServiceResult) {
	for _, op := range []string{"update", "save-profile"} {
		t.Run("rollback/"+op, func(t *testing.T) {
			s := chainDiffBase()
			q := chainServiceRequest{Op: op, Name: "new", Original: "old", Hops: []string{"a", "b"}, Next: proxyChain{Enabled: false}}
			f := chainDiffNew(q, s)
			oldHops := make([]string, 2, 5)
			copy(oldHops, []string{"a", "b"})
			oldMap := map[string][]string{"old": oldHops}
			f.Base.a.settings.Chain = proxyChain{Name: "old", Enabled: true, Hops: oldHops}
			f.Base.a.settings.Chains = oldMap
			f.Base.save = func() error { return errors.New("save fail") }
			r := run(q, f)
			if r.Status != 500 {
				t.Fatal(r.Status)
			}
			oldMap["after"] = []string{"visible"}
			if f.Base.a.settings.Chains["after"] == nil || &f.Base.a.settings.Chain.Hops[0] != &oldHops[0] || cap(f.Base.a.settings.Chain.Hops) != 5 {
				t.Fatal("rollback lost original map or Chain slice backing")
			}
			if op == "save-profile" && (&f.Base.a.settings.Chains["old"][0] != &oldHops[0] || cap(f.Base.a.settings.Chains["old"]) != 5) {
				t.Fatal("profiles rollback lost original slice backing")
			}
		})
	}
	t.Run("success-shallow-profile-map", func(t *testing.T) {
		s := chainDiffBase()
		hops := []string{"a", "b"}
		q := chainServiceRequest{Op: "save-profile", Name: "new", Original: "old", Hops: hops}
		f := chainDiffNew(q, s)
		f.Base.q = q
		oldMap := f.Base.a.settings.Chains
		oldOther := []string{"first", "second"}
		oldMap["other"] = oldOther
		r := run(q, f)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		hops[0] = "external"
		oldOther[0] = "also-external"
		if f.Base.a.settings.Chains["new"][0] != "external" || f.Base.a.settings.Chain.Hops[0] != "external" || f.Base.a.settings.Chains["other"][0] != "also-external" {
			t.Fatal("saved profiles should preserve request/retained slice alias")
		}
		if oldMap["old"] == nil || oldMap["new"] != nil {
			t.Fatal("maps.Clone should not mutate original top-level map")
		}
	})
	t.Run("compile-shallow-nested-map", func(t *testing.T) {
		s := chainDiffBase()
		nested := map[string]any{"value": "old"}
		p := map[string]any{"name": "a", "type": "trojan", "server": "a.invalid", "nested": nested}
		q := chainServiceRequest{Op: "compile", Hops: []string{"a"}, Index: map[string][]map[string]any{"a": {p}}}
		f := chainDiffNew(q, s)
		f.Base.q = q
		r := run(q, f)
		r = f.FinalizeChainResult(q, r)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		r.Proxies[0]["top"] = "private"
		r.Proxies[0]["nested"].(map[string]any)["value"] = "visible"
		if p["top"] != nil || nested["value"] != "visible" {
			t.Fatal("compile must clone top level only")
		}
	})
}
func TestMigrationChainSafeLegacyIdentity(t *testing.T) {
	chainLegacyIdentityContracts(t, runGoChainService)
}
