package main

import (
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"testing"
)

func TestSubscriptionPolicyYAMLProjection(t *testing.T) {
	const fixture = `template: &g {type: select, include-all: on, use: [a], interval: 010, threshold: 1.25}
proxies: [{name: 010, type: http, server: Node.Example}]
proxy-groups: [{<<: *g, name: 007, proxies: [010, null, 1e4], custom: {flag: true}}]
rules: ["DOMAIN,node.example,007", 010]
smartvpn-source-rules: ["MATCH,007"]
smartvpn-policy-sections: [{label: 001, groups: [{<<: *g, name: nested, proxies: []}], rules: []}]
dns: {nameserver-policy: {node.example: [https://resolver.example/dns, 010]}}
`
	for _, s := range []string{fixture, "{}", "proxies: []\nproxy-groups: []\nrules: []\nsmartvpn-policy-sections: []", "proxy-groups: [null, {proxies: [], use: [], extra: null}]"} {
		b := []byte(s)
		var oracle sourcePolicy
		if err := yaml.Unmarshal(b, &oracle); err != nil {
			t.Fatal(err)
		}
		native := nativeSubscriptionPolicy(b)
		if mode := os.Getenv("SMARTVPN_TEST_POLICY_YAML"); mode != "" && (native != nil) != (mode == "rust") {
			t.Fatalf("mode %s, native=%t", mode, native != nil)
		}
		if native != nil {
			var oracleDNS struct {
				DNS struct {
					Policy map[string]any `yaml:"nameserver-policy"`
				}
				Proxies []subscriptionProxy
			}
			if err := yaml.Unmarshal(b, &oracleDNS); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(native.dns, oracleDNS.DNS.Policy) || len(native.servers) != len(oracleDNS.Proxies) {
				t.Fatal("raw DNS values or proxy count differ")
			}
			for i, n := range oracleDNS.Proxies {
				if native.servers[i] != n.Server {
					t.Fatal("proxy host differs")
				}
			}
		}
		p, err := decodeSourcePolicy(b, native)
		if err != nil || !reflect.DeepEqual(p, oracle) {
			t.Fatalf("policy differs: %#v / %#v (%v)", p, oracle, err)
		}
		if !reflect.DeepEqual(entryDNSParsed(b, native), entryDNSParsed(b, nil)) || !reflect.DeepEqual(subscriptionRulesParsed(b, native), subscriptionRulesParsed(b, nil)) {
			t.Fatal("native and Go DNS/rules differ")
		}
	}
	// A malformed group invalidates the policy but not the independent DNS
	// projection. A malformed DNS section likewise must not discard rules.
	badGroup := []byte("proxy-groups: [{include-all: wrong}]\nproxies: [{server: n.example}]\ndns: {nameserver-policy: {n.example: https://dns.example/query}}")
	if got := entryDNS(badGroup)["n.example"]; !reflect.DeepEqual(got, []string{"https://dns.example/query"}) {
		t.Fatalf("lost valid DNS on group fallback: %v", got)
	}
	badDNS := []byte("dns: []\nrules: ['DOMAIN,n.example,DIRECT']")
	if got := subscriptionRules(badDNS); !reflect.DeepEqual(got, subscriptionRulesParsed(badDNS, nil)) || len(got) != 1 {
		t.Fatalf("lost valid rule on DNS fallback: %v", got)
	}
	var cache policyPreviewCache
	b := []byte(fixture)
	first, err := cache.read(b)
	if err != nil {
		t.Fatal(err)
	}
	saved := cache.native
	again, err := cache.read(append([]byte(nil), b...))
	if err != nil || cache.native != saved || &first.Groups[0] != &again.Groups[0] {
		t.Fatal("identical profile reparsed")
	}
	changed, err := cache.read([]byte("rules: [different]"))
	if err != nil || !reflect.DeepEqual(changed.Rules, []string{"different"}) {
		t.Fatal("stale policy preview")
	}
	for _, s := range []string{"rules: [{bad: shape}]", "dns: []\nrules: [still-valid]", "proxy-groups: [{include-all: wrong}]", "rules: !!seq [valid]", "proxy-groups: [{name: a, name: b}]"} {
		b := []byte(s)
		if nativeSubscriptionPolicy(b) != nil {
			t.Fatalf("expected compatibility fallback for %q", s)
		}
		var want sourcePolicy
		wantErr := yaml.Unmarshal(b, &want)
		got, err := cache.read(b)
		if (err == nil) != (wantErr == nil) || err != nil && err.Error() != wantErr.Error() || err == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("fallback differs for %q", s)
		}
	}
}
