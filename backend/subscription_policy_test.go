package main

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestMergedPolicyReferencesAndRuleOrder(t *testing.T) {
	profile := []byte("proxies:\n  - {name: A, type: socks5, server: example.org, port: 80}\nproxy-groups:\n  - {name: G, type: select, proxies: [A]}\n  - {name: Ads, type: select, proxies: [REJECT]}\nrules:\n  - DOMAIN,allow.example,DIRECT\n  - DOMAIN-SUFFIX,example,Ads\n  - MATCH,G\n")
	b, nodes, err := mergeSubscriptionSources([]SubscriptionSource{{Body: profile}, {Body: profile, Prefix: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	var p sourcePolicy
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 4 || p.Groups[2].Members[0] != "two A" || p.Groups[2].Name == p.Groups[0].Name {
		t.Fatal("source group identity lost")
	}
	if len(p.Sections) != 2 || p.Sections[1].Groups[0].Name != "G" || p.Sections[1].Groups[0].Members[0] != "A" || p.Sections[1].Rules[2] != "MATCH,G" {
		t.Fatal("original policy not preserved")
	}
	restored := parseProfileNodes(b)
	if len(restored) != 2 || nodes[0].SourceLabel == nodes[1].SourceLabel || restored[1].SourceLabel != nodes[1].SourceLabel {
		t.Fatal("source identity not preserved")
	}
	preview, rules := supportedRules(p)
	if len(preview) != 6 || len(rules) != 4 || rules[1] != "DOMAIN-SUFFIX,example,REJECT" || preview[2].Applied != "" {
		t.Fatal(preview)
	}
	cfg := generatedConfig(configOptions{Rules: rules, SourceRuleOrder: true})
	if !strings.Contains(cfg, rules[0]) || strings.Index(cfg, rules[0]) > strings.Index(cfg, rules[1]) {
		t.Fatal("rule order lost")
	}
}
