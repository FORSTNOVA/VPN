package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"
	"time"
)

type probeDiffSpec struct {
	Store, Subscription, TUN, StrictDNS, Resolver, Reused, Deadline bool
	FailKind                                                        string
	FailAt                                                          int
	Cold, Warm                                                      int64
	Status                                                          int
	IP, Attempt                                                     string
	Policy                                                          map[string][]string
	Records                                                         []probeRecord
}
type probeDiffFX struct {
	a        *app
	e        *appChainProbeEffects
	trace    []chainProbeEffect
	cleanup  []string
	counts   map[string]int
	clock    int
	response chainProbeServiceResult
	save     []probeRecord
}

func probeDiffBase() probeDiffSpec {
	return probeDiffSpec{Store: true, Subscription: true, Resolver: true, Reused: true, Cold: 3, Warm: 2, Status: 204, IP: "192.0.2.1", Attempt: "2001:db8::1", Policy: map[string][]string{"node.test": {"synthetic-dns"}}}
}
func probeDiffRequest(op string) chainProbeServiceRequest {
	return chainProbeServiceRequest{Op: op, Hops: []string{"a", "b"}, EntryURLs: []string{"synthetic-dns"}, Proxies: []map[string]any{{"name": "a", "server": "node.test", "type": "ss", "dialer-proxy": "old", "ech-opts": map[string]any{"enable": true, "query-server-name": "ech.test"}, "nested": map[string]any{"value": 1}}, {"name": "b", "server": "second.test", "type": "ss", "dialer-proxy": "a"}}, Target: probeTarget, Context: context.Background(), Budget: 12 * time.Second, Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}
func probeDiffNew(q chainProbeServiceRequest, s probeDiffSpec) *probeDiffFX {
	a := &app{}
	if s.Store {
		a.store = &store{}
	}
	if s.Subscription {
		testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	}
	e := newAppChainProbeEffects(a, q, nil)
	f := &probeDiffFX{a: a, e: e, counts: map[string]int{}}
	cleanup := func(k string) { f.cleanup = append(f.cleanup, k) }
	fail := func(k string) error {
		n := f.counts[k]
		f.counts[k]++
		if s.FailKind == k && n == s.FailAt {
			return errors.New("synthetic " + k)
		}
		return nil
	}
	e.hooks["index"] = func(chainProbeEffect) chainProbeReply {
		e.index = probeDiffIndex(q.Proxies)
		return chainProbeReply{Err: fail("index")}
	}
	e.hooks["mode"] = func(chainProbeEffect) chainProbeReply {
		r := e.snapshot()
		r.TUN = s.TUN
		r.StrictDNS = s.StrictDNS
		return chainProbeReply{State: r}
	}
	e.hooks["policy"] = func(chainProbeEffect) chainProbeReply { e.policy = s.Policy; return chainProbeReply{Policy: s.Policy} }
	e.hooks["parent-context"] = func(chainProbeEffect) chainProbeReply {
		e.parentCancel = func() { cleanup("parent-cancel") }
		return chainProbeReply{}
	}
	e.now = func() time.Time { v := q.Started.Add(time.Duration(f.clock) * time.Second); f.clock++; return v }
	e.hooks["resolver"] = func(chainProbeEffect) chainProbeReply {
		err := fail("resolver")
		if err == nil && s.Resolver {
			e.resolverReset = func() { cleanup("resolver-reset") }
		}
		return chainProbeReply{Bool: err == nil && s.Resolver, Err: err}
	}
	e.hooks["ech"] = func(chainProbeEffect) chainProbeReply {
		e.ech = []byte{1, 2, 3}
		return chainProbeReply{Err: fail("ech")}
	}
	e.hooks["entry-dialer"] = func(chainProbeEffect) chainProbeReply { return chainProbeReply{} }
	e.hooks["adapter"] = func(x chainProbeEffect) chainProbeReply {
		err := fail("adapter")
		if err == nil {
			i := len(e.nodeClosers)
			e.nodeClosers = append(e.nodeClosers, func() { cleanup(fmt.Sprintf("node-%d", i)) })
		}
		return chainProbeReply{Err: err}
	}
	e.hooks["transport"] = func(chainProbeEffect) chainProbeReply {
		e.idleClose = func() { cleanup("idle-close") }
		return chainProbeReply{}
	}
	e.hooks["request-new"] = func(chainProbeEffect) chainProbeReply {
		e.requestCancel = func() { cleanup("request-cancel") }
		return chainProbeReply{Err: fail("request-new")}
	}
	e.hooks["request-do"] = func(chainProbeEffect) chainProbeReply {
		err := fail("request-do")
		if err == nil {
			e.body = migrationProbeBody{close: func() { cleanup("body-close") }}
		}
		return chainProbeReply{Err: err, Deadline: s.Deadline, Reused: s.Reused, HTTPStatus: s.Status}
	}
	e.hooks["elapsed"] = func(chainProbeEffect) chainProbeReply {
		n := f.counts["elapsed"]
		f.counts["elapsed"]++
		v := s.Cold
		if n > 0 {
			v = s.Warm
		}
		return chainProbeReply{Elapsed: v}
	}
	e.hooks["observation"] = func(x chainProbeEffect) chainProbeReply {
		e.obs.ip = s.IP
		e.obs.attempt = s.Attempt
		delete(e.hooks, "observation")
		r := e.Call(x)
		e.hooks["observation"] = func(chainProbeEffect) chainProbeReply { return r }
		return r
	}
	e.hooks["save"] = func(x chainProbeEffect) chainProbeReply {
		f.save = append(f.save, x.Report)
		return chainProbeReply{Err: fail("save")}
	}
	e.hooks["records"] = func(chainProbeEffect) chainProbeReply {
		e.records = append([]probeRecord(nil), s.Records...)
		return chainProbeReply{Records: e.records, Err: fail("records")}
	}
	e.respond = func(code int, msg string, r probeResult, rs []probeResult) {
		if !a.mu.TryLock() {
			panic("response while lock held")
		}
		a.mu.Unlock()
		f.response = chainProbeServiceResult{Status: code, Message: msg, Result: r, Results: rs}
	}
	return f
}
func (f *probeDiffFX) Call(x chainProbeEffect) chainProbeReply {
	if f.e.hooks[x.Kind] == nil {
		switch x.Kind {
		case "lock", "unlock", "snapshot", "compile", "fingerprint", "parent-cancel", "entry-policy", "entry-select", "now", "stage", "node", "ech-apply", "body-close", "request-cancel", "idle-close", "node-close", "resolver-reset", "store-check", "path-encode", "result-encode", "details-decode", "path-decode", "response":
		default:
			panic("unmocked probe port: " + x.Kind)
		}
	}
	f.trace = append(f.trace, x)
	return f.e.Call(x)
}
func (f *probeDiffFX) Release() { f.e.Release() }
func probeDiffRun(t *testing.T, q chainProbeServiceRequest, s probeDiffSpec, run func(chainProbeServiceRequest, chainProbeServiceEffects) chainProbeServiceResult) (chainProbeServiceResult, *probeDiffFX) {
	t.Helper()
	f := probeDiffNew(q, s)
	r := run(q, f)
	f.Release()
	if !f.a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	f.a.mu.Unlock()
	return r, f
}
func probeDiffCompare(t *testing.T, op string, s probeDiffSpec, run func(chainProbeServiceRequest, chainProbeServiceEffects) chainProbeServiceResult) {
	t.Helper()
	a, x := probeDiffRun(t, probeDiffRequest(op), s, runGoChainProbeService)
	b, y := probeDiffRun(t, probeDiffRequest(op), s, run)
	if fmt.Sprint(a.Err) != fmt.Sprint(b.Err) {
		t.Fatalf("error mismatch %v %v", a.Err, b.Err)
	}
	a.Err = nil
	b.Err = nil
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.cleanup, y.cleanup) || !reflect.DeepEqual(x.save, y.save) || !reflect.DeepEqual(x.e.proxies, y.e.proxies) {
		t.Fatalf("%s\nGo=%#v\nRust=%#v\nGoTrace=%#v\nRustTrace=%#v\ncleanup=%v / %v", op, a, b, x.trace, y.trace, x.cleanup, y.cleanup)
	}
}
func probeDiffRecords() []probeRecord {
	q := probeDiffRequest("list")
	index := probeDiffIndex(q.Proxies)
	p, _ := compilePrefix([]string{"a", "b"}, index)
	path, _ := json.Marshal([]string{"a", "b"})
	sig := probeFingerprint(p)
	return []probeRecord{{Path: string(path), Fingerprint: sig, Delay: 10, Measured: 100}, {Path: "bad", Fingerprint: sig, Delay: 20}, {Path: string(path), Fingerprint: "old", Delay: 30}, {Path: string(path), Fingerprint: sig, Delay: 40, Measured: 200}}
}
func probeDiffIndex(p []map[string]any) map[string][]map[string]any {
	a, b := maps.Clone(p[0]), maps.Clone(p[1])
	delete(a, "dialer-proxy")
	delete(b, "dialer-proxy")
	return map[string][]map[string]any{"a": {a}, "b": {b}}
}
func TestMigrationChainProbeSafeSelectedLifecycle(t *testing.T) {
	for _, op := range []string{"probe", "list", "measure-both", "measure-prefix", "timed-requests"} {
		s := probeDiffBase()
		s.Records = probeDiffRecords()
		probeDiffCompare(t, op, s, runSelectedChainProbeService)
	}
}
func probeCount(f *probeDiffFX, k string) int {
	n := 0
	for _, e := range f.trace {
		if e.Kind == k {
			n++
		}
	}
	return n
}
func probeLegacyGoldens(t *testing.T, run func(chainProbeServiceRequest, chainProbeServiceEffects) chainProbeServiceResult) {
	for mode := 0; mode < 7; mode++ {
		s := probeDiffBase()
		switch mode {
		case 1:
			s.FailKind = "request-do"
		case 2:
			s.FailKind = "request-do"
			s.FailAt = 1
		case 3:
			s.Reused = false
		case 4:
			s.Cold = 0
			s.Warm = -1
		case 5:
			s.Store = false
		case 6:
			s.FailKind = "save"
		}
		q := probeDiffRequest("probe")
		r, f := probeDiffRun(t, q, s, run)
		want := 200
		if mode == 5 {
			want = 503
		}
		if mode == 6 {
			want = 500
		}
		if r.Status != want {
			t.Fatalf("mode=%d status=%d want=%d", mode, r.Status, want)
		}
		if mode == 1 && f.counts["request-do"] != 1 {
			t.Fatal("cold failure ran warm")
		}
		if mode == 2 || mode == 3 {
			if r.Result.Delay < 1 || r.Result.WarmDelay != 0 || r.Result.Error != "" || r.Result.WarmError == "" {
				t.Fatal("warm failure classification")
			}
		}
		if mode == 4 && (r.Result.Delay != 1 || r.Result.WarmDelay != 1) {
			t.Fatal("minimum elapsed not 1ms")
		}
		if mode == 5 && (f.counts["request-do"] != 2 || len(f.save) != 0) {
			t.Fatal("store checked before measurement")
		}
		if mode == 6 && len(f.save) != 1 {
			t.Fatal("save retried")
		}
		if mode == 0 {
			if r.Result.EntryIP != s.IP || r.Result.EntryAttempt || r.Result.EntryFamily != "IPv4" || r.Result.FailureStage != "" {
				t.Fatal("observation priority")
			}
			if len(f.save) != 1 || f.save[0].Fingerprint == probeFingerprint(f.e.proxies) {
				t.Fatal("fingerprint computed after mutation")
			}
			tail := f.cleanup[len(f.cleanup)-5:]
			if !reflect.DeepEqual(tail, []string{"idle-close", "node-1", "node-0", "resolver-reset", "parent-cancel"}) {
				t.Fatal("resource unwind order", f.cleanup)
			}
		}
	}
	s := probeDiffBase()
	s.Reused = false
	r, _ := probeDiffRun(t, probeDiffRequest("measure-prefix"), s, run)
	if r.Cold < 1 || r.Err != nil {
		t.Fatal("prefix failed despite successful cold")
	}
	for _, ip := range []string{"", "::ffff:192.0.2.8"} {
		s := probeDiffBase()
		s.IP = ip
		r, _ := probeDiffRun(t, probeDiffRequest("probe"), s, run)
		if ip == "" {
			if !r.Result.EntryAttempt || r.Result.EntryIP != s.Attempt || r.Result.EntryFamily != "IPv6" {
				t.Fatal("attempt fallback")
			}
		} else if r.Result.EntryFamily != "IPv4" {
			t.Fatal("unmap family")
		}
	}
	for mode := 0; mode < 3; mode++ {
		s := probeDiffBase()
		s.Records = probeDiffRecords()
		if mode == 1 {
			s.Records[1].Details = "{"
		}
		if mode == 2 {
			s.Records[0].Details = `{"delay":999,"hops":["wrong"],"target":"custom"}`
		}
		r, _ := probeDiffRun(t, probeDiffRequest("list"), s, run)
		if mode == 1 {
			if r.Status != 500 {
				t.Fatal("malformed details not all-fail")
			}
			continue
		}
		if r.Status != 200 || len(r.Results) != 2 || r.Results[1].Delay != 40 {
			t.Fatalf("list filter/order %#v", r)
		}
		if mode == 0 && r.Results[0].Delay != 10 {
			t.Fatal("record order")
		}
		if mode == 2 && (r.Results[0].Delay != 999 || r.Results[0].Target != "custom" || !reflect.DeepEqual(r.Results[0].Hops, []string{"a", "b"})) {
			t.Fatal("details/path overlay")
		}
	}
}
func TestMigrationChainProbeSafeLegacyGoldens(t *testing.T) {
	probeLegacyGoldens(t, runSelectedChainProbeService)
}
func probeAliasGoldens(t *testing.T, run func(chainProbeServiceRequest, chainProbeServiceEffects) chainProbeServiceResult) {
	for mode := 0; mode < 4; mode++ {
		q := probeDiffRequest("measure-both")
		p := q.Proxies[0]
		ech := p["ech-opts"].(map[string]any)
		p["servername"] = "server-name"
		p["sni"] = "sni-name"
		want := "ech.test"
		if mode >= 1 {
			delete(ech, "query-server-name")
			want = "server-name"
		}
		if mode >= 2 {
			delete(p, "servername")
			want = "sni-name"
		}
		if mode >= 3 {
			delete(p, "sni")
			want = "node.test"
		}
		f := probeDiffNew(q, probeDiffBase())
		var first map[string]any
		adapter := f.e.hooks["adapter"]
		f.e.hooks["adapter"] = func(x chainProbeEffect) chainProbeReply {
			if x.Index == 0 {
				first = f.e.current
			}
			return adapter(x)
		}
		r := run(q, f)
		f.Release()
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if _, ok := p["dialer-proxy"]; ok {
			t.Fatal("dialer deletion no longer in-place")
		}
		if ech["config"] != nil {
			t.Fatal("ECH config mutated original options")
		}
		first["test-top"] = "private"
		if p["test-top"] != nil {
			t.Fatal("ECH top clone missing")
		}
		first["nested"].(map[string]any)["value"] = 2
		if p["nested"].(map[string]any)["value"] != 2 {
			t.Fatal("ECH over-deep cloned nested alias")
		}
		if first["ech-opts"].(map[string]any)["config"] != "AQID" {
			t.Fatal("ECH bytes not applied")
		}
		var seen bool
		for _, x := range f.trace {
			if x.Kind == "ech" {
				seen = true
				if x.Name != want || !x.Time.Equal(q.Started.Add(12*time.Second)) {
					t.Fatal("ECH name/deadline precedence", x)
				}
			}
		}
		if !seen {
			t.Fatal("ECH not invoked")
		}
	}
}
func TestMigrationChainProbeSafeAlias(t *testing.T) {
	probeAliasGoldens(t, runSelectedChainProbeService)
}
func probeEdgeGoldens(t *testing.T, run func(chainProbeServiceRequest, chainProbeServiceEffects) chainProbeServiceResult) {
	s := probeDiffBase()
	r, _ := probeDiffRun(t, probeDiffRequest("list"), s, run)
	if r.Status != 200 || r.Results == nil || len(r.Results) != 0 {
		t.Fatal("empty list shape")
	}
	q := probeDiffRequest("measure-both")
	q.Proxies = nil
	r, f := probeDiffRun(t, q, s, run)
	if r.Err == nil || r.Cold != 0 || probeCount(f, "resolver") != 0 || probeCount(f, "request-do") != 0 {
		t.Fatal("empty measure attempted resources")
	}
	for mode := 0; mode < 5; mode++ {
		q = probeDiffRequest("measure-both")
		s = probeDiffBase()
		switch mode {
		case 0:
			q.EntryURLs = nil
		case 1:
			s.Resolver = false
		case 2:
			delete(q.Proxies[0], "ech-opts")
		case 3:
			q.Proxies[0]["ech-opts"].(map[string]any)["enable"] = false
		case 4:
			q.Proxies[0]["ech-opts"].(map[string]any)["config"] = "preconfigured"
		}
		r, f = probeDiffRun(t, q, s, run)
		if r.Err != nil || probeCount(f, "ech") != 0 {
			t.Fatal("ECH gating changed", mode, r.Err)
		}
	}
}
func TestMigrationChainProbeSafeEdges(t *testing.T) {
	probeEdgeGoldens(t, runSelectedChainProbeService)
}
