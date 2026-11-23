package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func chainProbeSyntheticNodes() []map[string]any {
	return []map[string]any{{"name": "a", "server": "a.invalid", "type": "http", "dialer-proxy": "old"}, {"name": "b", "server": "b.invalid", "type": "http", "dialer-proxy": "a"}}
}
func safeChainProbeFX(q chainProbeServiceRequest, trace *[]string) *appChainProbeEffects {
	e := newAppChainProbeEffects(nil, q, nil)
	now := time.Unix(1000, 0)
	e.hooks["now"] = func(chainProbeEffect) chainProbeReply { now = now.Add(time.Second); return chainProbeReply{Now: now} }
	e.hooks["resolver"] = func(chainProbeEffect) chainProbeReply {
		e.resolverReset = func() { *trace = append(*trace, "resolver") }
		return chainProbeReply{Bool: true}
	}
	e.hooks["ech"] = func(chainProbeEffect) chainProbeReply { e.ech = []byte("synthetic"); return chainProbeReply{} }
	e.hooks["entry-dialer"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{} }
	e.hooks["adapter"] = func(q chainProbeEffect) chainProbeReply {
		i := q.Index
		e.nodeClosers = append(e.nodeClosers, func() { *trace = append(*trace, []string{"node0", "node1"}[i]) })
		return chainProbeReply{}
	}
	e.hooks["transport"] = func(chainProbeEffect) chainProbeReply {
		e.idleClose = func() { *trace = append(*trace, "idle") }
		return chainProbeReply{}
	}
	e.hooks["request-new"] = func(chainProbeEffect) chainProbeReply {
		e.requestCancel = func() { *trace = append(*trace, "cancel") }
		return chainProbeReply{}
	}
	n := 0
	e.hooks["request-do"] = func(chainProbeEffect) chainProbeReply { n++; return chainProbeReply{HTTPStatus: 204, Reused: n > 1} }
	e.hooks["elapsed"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{Elapsed: 0} }
	return e
}
func TestChainProbeServiceResourceOrder(t *testing.T) {
	q := chainProbeServiceRequest{Op: "measure-both", Proxies: chainProbeSyntheticNodes(), Target: probeTarget}
	trace := []string{}
	e := safeChainProbeFX(q, &trace)
	r := runGoChainProbeService(q, e)
	e.Release()
	if r.Err != nil || r.Cold != 1 || r.Warm != 1 {
		t.Fatal("measurement result")
	}
	if !reflect.DeepEqual(trace, []string{"cancel", "cancel", "idle", "node1", "node0", "resolver"}) {
		t.Fatal(trace)
	}
	for _, p := range q.Proxies {
		if _, ok := p["dialer-proxy"]; ok {
			t.Fatal("original map not mutated")
		}
	}
}
func TestChainProbeServiceECHShallowCopy(t *testing.T) {
	nodes := chainProbeSyntheticNodes()
	ech := map[string]any{"enable": true, "query-server-name": "first.invalid"}
	nested := map[string]any{"x": 1}
	nodes[0]["ech-opts"] = ech
	nodes[0]["nested"] = nested
	q := chainProbeServiceRequest{Op: "measure-both", Proxies: nodes, EntryURLs: []string{"https://dns.invalid"}, Target: probeTarget}
	trace := []string{}
	e := safeChainProbeFX(q, &trace)
	old := e.hooks["adapter"]
	e.hooks["adapter"] = func(q chainProbeEffect) chainProbeReply {
		if q.Index == 0 {
			if e.current["ech-opts"].(map[string]any)["config"] != "c3ludGhldGlj" || ech["config"] != nil {
				t.Fatal("ECH clone contract")
			}
			e.current["nested"].(map[string]any)["later"] = true
		}
		return old(q)
	}
	e.hooks["ech"] = func(q chainProbeEffect) chainProbeReply {
		if q.Name != "first.invalid" || !q.Time.Equal(time.Unix(1013, 0)) {
			t.Fatal("ECH name/deadline")
		}
		e.ech = []byte("synthetic")
		return chainProbeReply{}
	}
	r := runGoChainProbeService(q, e)
	if r.Err != nil || nested["later"] != true {
		t.Fatal("nested shallow alias")
	}
}
func TestChainProbeServiceWarmFailureAndPrefix(t *testing.T) {
	for _, op := range []string{"measure-both", "measure-prefix"} {
		q := chainProbeServiceRequest{Op: op, Proxies: chainProbeSyntheticNodes(), Target: probeTarget}
		trace := []string{}
		e := safeChainProbeFX(q, &trace)
		calls := 0
		e.hooks["request-do"] = func(chainProbeEffect) chainProbeReply {
			calls++
			if calls == 2 {
				return chainProbeReply{HTTPStatus: 503}
			}
			return chainProbeReply{HTTPStatus: 204}
		}
		r := runGoChainProbeService(q, e)
		if r.Cold != 1 || r.Warm != 0 || (r.Err == nil) != (op == "measure-prefix") {
			t.Fatal("warm error projection")
		}
		if e.obs.stage != chainProbeStage(12) {
			t.Fatal("warm failure stage")
		}
	}
}
func TestChainProbeServiceDeadlineClassification(t *testing.T) {
	for _, deadline := range []bool{true, false} {
		q := chainProbeServiceRequest{Op: "timed-requests", Started: time.Unix(10, 0), Budget: 3 * time.Second, Target: probeTarget}
		trace := []string{}
		e := safeChainProbeFX(q, &trace)
		calls := 0
		e.hooks["request-new"] = func(v chainProbeEffect) chainProbeReply {
			if v.Budget != q.Budget || !v.Time.Equal(q.Started) {
				t.Fatal("request budget")
			}
			return chainProbeReply{}
		}
		e.hooks["request-do"] = func(chainProbeEffect) chainProbeReply {
			calls++
			err := context.Canceled
			if deadline {
				err = context.DeadlineExceeded
			}
			return chainProbeReply{Err: err, Deadline: deadline}
		}
		r := runGoChainProbeService(q, e)
		want := chainProbeError(4)
		if deadline {
			want = chainProbeError(3)
		}
		if r.Err == nil || r.Err.Error() != want || calls != 1 {
			t.Fatal("classification")
		}
	}
}
func TestChainProbeServiceCleanupPanic(t *testing.T) {
	q := chainProbeServiceRequest{Op: "measure-both", Proxies: chainProbeSyntheticNodes(), Target: probeTarget}
	trace := []string{}
	e := safeChainProbeFX(q, &trace)
	e.hooks["request-do"] = func(chainProbeEffect) chainProbeReply { panic("synthetic request") }
	e.hooks["transport"] = func(chainProbeEffect) chainProbeReply {
		e.idleClose = func() { trace = append(trace, "idle"); panic("synthetic cleanup") }
		return chainProbeReply{}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		runGoChainProbeService(q, e)
	}()
	e.Release()
	if !reflect.DeepEqual(trace, []string{"cancel", "idle", "node1", "node0", "resolver"}) {
		t.Fatal("remaining cleanup skipped", trace)
	}
}
func TestChainProbeServiceIndexFailureOrder(t *testing.T) {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	q := chainProbeServiceRequest{Op: "probe", Hops: []string{"a"}}
	e := newAppChainProbeEffects(a, q, nil)
	trace := []string{}
	e.hooks["index"] = func(chainProbeEffect) chainProbeReply {
		trace = append(trace, "index")
		return chainProbeReply{Err: errors.New("synthetic index")}
	}
	e.hooks["mode"] = func(chainProbeEffect) chainProbeReply {
		trace = append(trace, "mode")
		return chainProbeReply{State: &chainProbeState{Subscription: true}}
	}
	e.hooks["policy"] = func(chainProbeEffect) chainProbeReply { trace = append(trace, "policy"); return chainProbeReply{} }
	e.respond = func(status int, msg string, _ probeResult, _ []probeResult) {
		trace = append(trace, "response")
		if status != 400 || !strings.Contains(msg, "index") {
			t.Fatal("index error")
		}
		if !a.mu.TryLock() {
			t.Fatal("response unexpectedly locked")
		}
		a.mu.Unlock()
	}
	runGoChainProbeService(q, e)
	if !reflect.DeepEqual(trace, []string{"index", "mode", "policy", "response"}) {
		t.Fatal(trace)
	}
}
func TestChainProbeServiceModeSnapshotSurvivesUnlock(t *testing.T) {
	a := &app{store: &store{}}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	testEditSettings(a, func(s *settings) { s.DNSProtection = true })
	q := chainProbeServiceRequest{Op: "probe", Hops: []string{"a", "b"}, Proxies: chainProbeSyntheticNodes()}
	trace := []string{}
	e := safeChainProbeFX(q, &trace)
	e.a = a
	e.hooks["index"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{} }
	e.hooks["mode"] = func(chainProbeEffect) chainProbeReply {
		return chainProbeReply{State: &chainProbeState{Subscription: true, TUN: true, StrictDNS: true}}
	}
	e.hooks["policy"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{} }
	e.hooks["compile"] = func(chainProbeEffect) chainProbeReply {
		a.mu.Lock()
		testEditSettings(a, func(s *settings) { s.DNSProtection = false })
		testEditSettings(a, func(s *settings) { s.SubscriptionURL = "" })
		a.mu.Unlock()
		return chainProbeReply{State: &chainProbeState{ProxyCount: 2}}
	}
	e.hooks["parent-context"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{} }
	e.hooks["resolver"] = func(v chainProbeEffect) chainProbeReply {
		if !reflect.DeepEqual(v.URLs, []string{entryDoH}) {
			t.Fatal("prior locked mode overwritten", v.URLs)
		}
		return chainProbeReply{}
	}
	e.hooks["save"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{} }
	e.respond = func(code int, _ string, _ probeResult, _ []probeResult) {
		if code != 200 {
			t.Fatal(code)
		}
	}
	if r := runGoChainProbeService(q, e); r.Status != 200 {
		t.Fatal(r.Status)
	}
}
