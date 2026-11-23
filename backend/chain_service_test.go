package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func safeChainFX(a *app, q chainServiceRequest) *appChainEffects {
	e := newAppChainEffects(a, q)
	e.running = func() bool { return false }
	e.job = func() bool { return false }
	e.read = func() ([]byte, error) { return nil, nil }
	e.save = func() error { return nil }
	e.now = func() time.Time { return time.Unix(100000, 0) }
	return e
}
func chainFixtureNodes() []manualNode {
	return []manualNode{{Proxy: map[string]any{"name": "a", "server": "a.invalid", "type": "http"}}, {Proxy: map[string]any{"name": "b", "server": "b.invalid", "type": "http"}}}
}
func TestChainServiceCompileAlias(t *testing.T) {
	nested := map[string]any{"x": 1}
	nodes := chainFixtureNodes()
	nodes[0].Proxy["nested"] = nested
	nodes[0].Proxy["dialer-proxy"] = false
	q := chainServiceRequest{Op: "chain", Hops: []string{"a", "b"}, Manual: nodes}
	e := safeChainFX(nil, q)
	r := runGoChainService(q, e)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if r.Proxies[0]["name"] != "SmartVPNChainHop1" || r.Proxies[1]["name"] != chainExit || r.Proxies[1]["dialer-proxy"] != "SmartVPNChainHop1" || r.Proxies[0]["dialer-proxy"] != false {
		t.Fatal("wrong aliases")
	}
	r.Proxies[0]["nested"].(map[string]any)["later"] = true
	if nested["later"] != true || nodes[0].Proxy["name"] != "a" {
		t.Fatal("shallow-copy contract")
	}
}
func TestChainServiceValidationPriority(t *testing.T) {
	for _, op := range []string{"chain", "prefix"} {
		q := chainServiceRequest{Op: op, Profile: []byte("["), Hops: nil}
		e := safeChainFX(nil, q)
		reads := 0
		e.decode = func([]byte) ([]map[string]any, error) { reads++; return nil, errors.New("bad") }
		r := runGoChainService(q, e)
		if op == "chain" {
			if reads != 0 || r.Message != chainCompileMessage0() {
				t.Fatal("chain length priority")
			}
		} else if reads != 1 || r.Message != chainCompileMessage1() {
			t.Fatal("prefix decode priority")
		}
	}
	a := &app{}
	q := chainServiceRequest{Op: "update", Next: proxyChain{Enabled: false}}
	e := safeChainFX(a, q)
	e.running = func() bool { return true }
	e.job = func() bool { t.Fatal("short circuit job"); return false }
	r := runGoChainService(q, e)
	if r.Status != 409 {
		t.Fatal(r.Status)
	}
}
func TestChainServiceSaveDoubleRollback(t *testing.T) {
	h := make([]string, 2, 5)
	copy(h, []string{"a", "b"})
	old := map[string][]string{"old": h, "unrelated": h}
	a := &app{manualNodes: chainFixtureNodes()}
	a.settings.Chains = old
	a.settings.Chain = proxyChain{Name: "old", Enabled: true, Hops: h}
	q := chainServiceRequest{Op: "save-profile", Name: " new ", Original: "old", Hops: []string{"b", "a"}}
	e := safeChainFX(a, q)
	count := 0
	e.decode = func([]byte) ([]map[string]any, error) { count++; return nil, nil }
	e.save = func() error {
		if a.settings.Chain.Name != "new" || a.settings.Chains["new"] == nil {
			t.Fatal("commit ordering")
		}
		return errors.New("fake")
	}
	r := runGoChainService(q, e)
	if r.Status != 500 || count != 2 || a.settings.Chain.Name != "old" || reflect.ValueOf(a.settings.Chains).UnsafePointer() != reflect.ValueOf(old).UnsafePointer() || &a.settings.Chain.Hops[0] != &h[0] || cap(a.settings.Chain.Hops) != 5 {
		t.Fatal("double rollback identity")
	}
}
func TestChainServiceRunningScopeAndPresence(t *testing.T) {
	a := &app{manualNodes: chainFixtureNodes()}
	a.settings.Chains = map[string][]string{"nil": nil, "empty": {}}
	q := chainServiceRequest{Op: "save-profile", Name: "nil", Hops: []string{"a", "b"}}
	e := safeChainFX(a, q)
	e.running = func() bool { t.Fatal("noncurrent checked running"); return true }
	if r := runGoChainService(q, e); r.Status != 200 {
		t.Fatal(r.Status)
	}
	q.Name = "empty"
	e = safeChainFX(a, q)
	if r := runGoChainService(q, e); r.Status != 409 {
		t.Fatal("empty slice must count existing")
	}
	q.Name = "new"
	q.Original = " nil "
	e = safeChainFX(a, q)
	if r := runGoChainService(q, e); r.Status != 409 {
		t.Fatal("Original unexpectedly trimmed")
	}
}
func TestChainServiceRegionClockAndBorrowed(t *testing.T) {
	a := testHealthApp(testControlApp(&app{manualNodes: chainFixtureNodes()}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "US"})
	}), func(p *healthDomainProjection) {
		p.Regions = map[string]regionRecord{"b": {Country: "US", Status: regionVerified, VerifiedAt: time.Unix(100000, 0).Add(-regionTTL)}}
	})
	q := chainServiceRequest{Op: "build", Next: proxyChain{Enabled: true, Hops: []string{"a", "b"}}}
	e := safeChainFX(a, q)
	a.mu.Lock()
	r := runGoChainService(q, e)
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed mutex released")
	}
	a.mu.Unlock()
	if r.Err != nil {
		t.Fatal("TTL equality rejected")
	}
	v := a.healthDomainLocked().Regions["b"]
	v.Status = regionUnreachable
	a.writeRegionRecordLocked("b", v)
	e = safeChainFX(a, q)
	e.now = func() time.Time { t.Fatal("clock before verified match"); return time.Time{} }
	if r := runGoChainService(q, e); r.Err == nil {
		t.Fatal("unverified accepted")
	}
}
func TestChainServiceUnicodeAndDisabled(t *testing.T) {
	a := &app{}
	q := chainServiceRequest{Op: "save-profile", Name: strings.Repeat("界", 81)}
	e := safeChainFX(a, q)
	a.mu.Lock()
	r := runGoChainService(q, e)
	a.mu.Unlock()
	if r.Status != 400 {
		t.Fatal("Unicode early validation")
	}
	q = chainServiceRequest{Op: "build", Next: proxyChain{Enabled: false}}
	e = safeChainFX(a, q)
	e.decode = func([]byte) ([]map[string]any, error) { t.Fatal("disabled decoded"); return nil, nil }
	if r := runGoChainService(q, e); r.Err != nil || r.Body != nil {
		t.Fatal("disabled build")
	}
}
