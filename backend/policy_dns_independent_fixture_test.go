package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

type policyIndependentFX struct {
	Reply    policyDnsReply
	Trace    []policyDnsEffect
	Releases int
}

func (f *policyIndependentFX) Release() { f.Releases++ }
func (f *policyIndependentFX) Call(e policyDnsEffect) policyDnsReply {
	f.Trace = append(f.Trace, e)
	if e.Kind != "source" {
		panic("unsafe policy port " + e.Kind)
	}
	r := f.Reply
	r.Names = append([]string(nil), r.Names...)
	return r
}

type policyIndependentRunner func(policyDnsRequest, policyDnsEffects) policyDnsResult

func policyIndependentMatrix(t *testing.T, run policyIndependentRunner) {
	rng := rand.New(rand.NewSource(3601))
	for i := 0; i < 1200; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			ops := []string{"targets", "rules", "subrules", "suffix", "merge", "domains", "hostname", "entrydns"}
			q := policyDnsRequest{Op: ops[i%8], Path: []string{"A.B.COM.", "bad..com", "'Node.Example'", "::1", "例子.com", "x-y.example"}[i%6], Body: []byte("proxies:\n - {server: Node.Example}\n - {server: 1.2.3.4}\ndns:\n nameserver-policy:\n  node.example: [https://one.example/dns, http://bad.example]\nrules:\n - DOMAIN,example.com,DIRECT\n - IP-CIDR,bad,DIRECT\n - MATCH,REJECT\nserver: x.example\n")}
			p := sourcePolicy{}
			for j := 0; j < 8; j++ {
				p.Groups = append(p.Groups, sourceGroup{Name: fmt.Sprint("g", rng.Intn(6)), Members: []string{[]string{"DIRECT", "REJECT", "SmartVPN", fmt.Sprint("g", rng.Intn(6))}[rng.Intn(4)]}, IncludeAll: rng.Intn(8) == 0})
				if rng.Intn(8) == 0 {
					p.Groups[j].Use = []string{"u"}
				}
			}
			p.Rules = []string{"DOMAIN,x.example,g0", "IP-CIDR,::ffff:192.0.2.1/128,g1,no-resolve", "DOMAIN,has space,g2", "DOMAIN-SUFFIX,x,g3"}
			e := &appPolicyDnsEffects{q: q, policy: p, lists: [][]string{{"z", "a", ""}, {"a", "b"}}}
			reply := e.source(q.Op)
			a := &policyIndependentFX{Reply: reply}
			b := &policyIndependentFX{Reply: reply}
			ga := runGoPolicyDns(q, a)
			gb := run(q, b)
			if !reflect.DeepEqual(ga, gb) || !reflect.DeepEqual(a, b) {
				t.Fatalf("%s\ngo=%#v\nactual=%#v", q.Op, ga, gb)
			}
		})
	}
}
func TestPolicyDNSIndependentSelectedMatrix(t *testing.T) {
	policyIndependentMatrix(t, runSelectedPolicyDns)
}
func policyIndependentGoldens(t *testing.T, run policyIndependentRunner) {
	p := sourcePolicy{Groups: []sourceGroup{{Name: "dup", Members: []string{"DIRECT"}}, {Name: "dup", Members: []string{"REJECT"}}, {Name: "nested", Members: []string{"dup", "REJECT"}}, {Name: "cycle", Members: []string{"cycle"}}, {Name: "provider", Members: []string{"DIRECT"}, Use: []string{"x"}}}}
	q := policyDnsRequest{Op: "targets"}
	e := &appPolicyDnsEffects{q: q, policy: p}
	r := run(q, e)
	want := map[string]string{"DIRECT": "DIRECT", "REJECT": "REJECT", "SmartVPN": "SmartVPN", "dup": "REJECT", "nested": "REJECT", "cycle": ""}
	got := r.Data["targets"].(map[string]string)
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("target %s %q", k, got[k])
		}
	}
	if _, ok := got["provider"]; ok {
		t.Fatal("unresolved provider cached")
	}
	backing := []string{" DOMAIN ,x.example,dup ", "spare", "untouched"}
	p.Rules = backing[:1]
	p.MergedRules = []string{"IP-CIDR,::ffff:192.0.2.1/128,DIRECT,no-resolve"}
	q.Op = "rules"
	e = &appPolicyDnsEffects{q: q, policy: p}
	r = run(q, e)
	if backing[1] != p.MergedRules[0] || backing[2] != "untouched" {
		t.Fatal("native source lost append identity")
	}
	if !reflect.DeepEqual(r.Data["active"], []string{"DOMAIN,x.example,REJECT", p.MergedRules[0]}) {
		t.Fatal(r.Data)
	}
	q = policyDnsRequest{Op: "subrules", Body: []byte("rules:\n - DOMAIN,has space,DIRECT,no-resolve,extra\n - IP-CIDR,notCIDR,DIRECT\n - MATCH,DIRECT\n")}
	r = run(q, &appPolicyDnsEffects{q: q})
	if !reflect.DeepEqual(r.Data["rules"], []string{"DOMAIN,has space,DIRECT,no-resolve", "IP-CIDR,notCIDR,DIRECT"}) {
		t.Fatal(r.Data)
	}
	q = policyDnsRequest{Op: "entrydns", Body: []byte("proxies:\n - {server: A.EXAMPLE.COM}\n - {server: a.example.com}\n - {server: b.example.com}\ndns:\n nameserver-policy:\n  a.example.com: [https://one.example/x, 'https://user@bad.example/x', 'https://bad.example/x#f', 1]\n  '*.example.com': https://wild.example/x\n")}
	r = run(q, &appPolicyDnsEffects{q: q})
	if !reflect.DeepEqual(r.Data["policy"], map[string][]string{"a.example.com": {"https://one.example/x"}}) {
		t.Fatal(r.Data)
	}
}
func TestPolicyDNSIndependentSelectedGoldens(t *testing.T) {
	policyIndependentGoldens(t, runSelectedPolicyDns)
}
func policyIndependentLimits(t *testing.T, run policyIndependentRunner) {
	var b strings.Builder
	b.WriteString("rules:\n")
	for i := 0; i < 3001; i++ {
		fmt.Fprintf(&b, " - DOMAIN,x%d.example,DIRECT\n", i)
	}
	q := policyDnsRequest{Op: "subrules", Body: []byte(b.String())}
	r := run(q, &appPolicyDnsEffects{q: q})
	v := r.Data["rules"].([]string)
	if len(v) != 3000 || v[2999] != "DOMAIN,x2999.example,DIRECT" {
		t.Fatal("3000 rule limit")
	}
	b.Reset()
	for i := 256; i >= 0; i-- {
		fmt.Fprintf(&b, "server: x.d%03d.example\n", i)
	}
	q = policyDnsRequest{Op: "domains", Body: []byte(b.String())}
	r = run(q, &appPolicyDnsEffects{q: q})
	v = r.Data["domains"].([]string)
	if len(v) != 256 || v[0] != "+.d001.example" || v[255] != "+.d256.example" {
		t.Fatal("256 before sort", v)
	}
	for _, s := range []string{"\u2003A.Example.COM.\u2003", "'X.Example'", "x..example", "例子.com", "::ffff:192.0.2.1"} {
		q = policyDnsRequest{Op: "suffix", Path: s}
		r = run(q, &appPolicyDnsEffects{q: q})
		ok := s == "\u2003A.Example.COM.\u2003" || s == "'X.Example'"
		if r.Data["ok"] != ok {
			t.Fatalf("suffix %q %+v", s, r)
		}
	}
}
func TestPolicyDNSIndependentSelectedLimits(t *testing.T) {
	policyIndependentLimits(t, runSelectedPolicyDns)
}

// Additional Phase36 final protocol goldens: each port is fake, never platform IO.
type policyIndependentHTTPFX struct {
	Empty, Merged, SourceMatch bool
	Fail                       string
	Nodes                      int64
	Trace                      []string
	Source                     policyDnsReply
}

func (f *policyIndependentHTTPFX) Release() {}
func (f *policyIndependentHTTPFX) Call(e policyDnsEffect) policyDnsReply {
	f.Trace = append(f.Trace, e.Kind)
	if e.Kind == f.Fail {
		return policyDnsReply{Err: subHTTPIndependentError}
	}
	switch e.Kind {
	case "lock", "read-policy":
		return policyDnsReply{}
	case "subscription":
		n := int64(0)
		if f.Merged {
			n = 1
		}
		return policyDnsReply{Flag: !f.Empty, Number: n}
	case "match-read":
		return policyDnsReply{}
	case "match-nodes":
		return policyDnsReply{Number: f.Nodes}
	case "match-source":
		s := "wrong"
		if f.SourceMatch {
			s = "synthetic-hash"
			if f.Merged {
				s = mergedSubscriptionURL
			}
		}
		return policyDnsReply{Text: s}
	case "hash":
		return policyDnsReply{Text: "synthetic-hash"}
	case "source":
		return f.Source
	case "enabled", "running":
		return policyDnsReply{Flag: true}
	default:
		panic("unsafe policy HTTP port " + e.Kind)
	}
}
func policyIndependentFinalFacts(t *testing.T, run policyIndependentRunner) {
	for _, c := range []struct {
		s, w string
		ok   bool
	}{{"İ.COM", "+.i.com", true}, {"K.COM", "+.k.com", true}, {"\u2003K.COM.\u2003", "+.k.com", true}} {
		q := policyDnsRequest{Op: "suffix", Path: c.s}
		r := run(q, &appPolicyDnsEffects{q: q})
		if r.Text != c.w || r.Data["ok"] != c.ok {
			t.Fatalf("unicode %q %#v", c.s, r)
		}
	}
	tail := []string{"source", "enabled", "running"}
	prefix := []string{"lock", "subscription"}
	cases := []struct {
		name   string
		f      policyIndependentHTTPFX
		middle []string
		bad    bool
	}{
		{"empty", policyIndependentHTTPFX{Empty: true}, nil, false},
		{"read-error", policyIndependentHTTPFX{Fail: "match-read"}, []string{"match-read"}, false},
		{"zero-nodes", policyIndependentHTTPFX{}, []string{"match-read", "match-nodes"}, false},
		{"source-error", policyIndependentHTTPFX{Nodes: 1, Fail: "match-source"}, []string{"match-read", "match-nodes", "match-source"}, false},
		{"merged-match", policyIndependentHTTPFX{Nodes: 1, Merged: true, SourceMatch: true}, []string{"match-read", "match-nodes", "match-source", "subscription", "read-policy"}, false},
		{"merged-mismatch", policyIndependentHTTPFX{Nodes: 1, Merged: true}, []string{"match-read", "match-nodes", "match-source", "subscription"}, false},
		{"single-match", policyIndependentHTTPFX{Nodes: 1, SourceMatch: true}, []string{"match-read", "match-nodes", "match-source", "subscription", "subscription", "hash", "read-policy"}, false},
		{"single-mismatch", policyIndependentHTTPFX{Nodes: 1}, []string{"match-read", "match-nodes", "match-source", "subscription", "subscription", "hash"}, false},
		{"second-read-error", policyIndependentHTTPFX{Nodes: 1, SourceMatch: true, Fail: "read-policy"}, []string{"match-read", "match-nodes", "match-source", "subscription", "subscription", "hash", "read-policy"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := policyDnsRequest{Op: "http"}
			e := &appPolicyDnsEffects{q: q}
			c.f.Source = e.source("rules")
			r := run(q, &c.f)
			want := append(append([]string{}, prefix...), c.middle...)
			if !c.bad {
				want = append(want, tail...)
			}
			if !reflect.DeepEqual(c.f.Trace, want) {
				t.Fatalf("priority got %v want %v", c.f.Trace, want)
			}
			if c.bad {
				if r.Status != 400 || r.Err == nil || r.Err.Error() != policyDnsReadError {
					t.Fatalf("error result %+v", r)
				}
			} else if r.Err != nil || r.Data["enabled"] != true || r.Data["running"] != true {
				t.Fatalf("HTTP result %+v", r)
			}
		})
	}
}
func TestPolicyDNSIndependentSelectedFinalFacts(t *testing.T) {
	policyIndependentFinalFacts(t, runSelectedPolicyDns)
}

func TestPolicyDNSIndependentSelectedInvalidUTF8(t *testing.T) {
	q := policyDnsRequest{Op: "suffix", Path: "\xff.COM"}
	r := runSelectedPolicyDns(q, &appPolicyDnsEffects{q: q})
	if r.Err != nil || r.Text != "" || r.Data["ok"] != false {
		t.Fatalf("invalid UTF8 legacy result %+v", r)
	}
}
