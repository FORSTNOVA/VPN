//go:build windows && amd64 && rustcore

package main

import (
	"os"
	"strings"
	"testing"
)

func TestRustConfigParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_CONFIG_RUST") == "1"
	if configRustAvailable() != expected {
		t.Fatal("config DLL availability")
	}
	for bits := 0; bits < 1024; bits++ {
		o := configOptions{ClashRS: bits&512 != 0, MixedPort: -7, ControllerPort: 12345, ProviderPath: "providers\\合成\"\n.yaml", ControllerSecret: "synthetic\"\n\x00\u2028", Rules: []string{"DOMAIN,a.invalid,DIRECT", "DOMAIN,b.invalid,REJECT", "DOMAIN,c.invalid,SmartVPN", "MATCH,DIRECT", "DOMAIN,quote\".invalid,SmartVPN"}, ProxyServerDomains: []string{"+.test.invalid", "quote\"\n.invalid"}, trafficPolicy: trafficPolicy{DNSProtection: bits&1 != 0, WebRTCProtection: bits&2 != 0, DomesticWeb: bits&4 != 0, GameBypass: []string{"game.exe", "chrome.exe", "bad/name"}}, TUN: bits&8 != 0, SourceRuleOrder: bits&16 != 0, SubscriptionProvider: bits&32 != 0, ManualProvider: bits&64 != 0, ChinaDirect: bits&128 != 0}
		if bits&256 != 0 {
			o.ChainProxies = []byte("proxies:\n  - name: synthetic-chain\n")
		}
		if bits%3 == 0 {
			o.dnsRelayAddr = "127.0.0.1:5999"
		}
		if bits%5 == 0 {
			o.EntryDNS = map[string][]string{"z.invalid": {"1.1.1.1"}, "a.invalid": {"https://example.invalid/dns-query"}}
		}
		want := generatedConfigGo(o)
		got := generatedConfig(o)
		if got != want {
			t.Fatalf("config mismatch bits=%d\ngot:\n%s\nwant:\n%s", bits, got, want)
		}
		if expected {
			s, ok := rustConfig(0, o, false, o.ProxyServerDomains)
			if !ok || s != want {
				t.Fatal("config Rust not exercised", bits)
			}
		}
		var a, b strings.Builder
		writeDomesticTCPRules(&a, o)
		writeDomesticTCPRulesGo(&b, o)
		if a.String() != b.String() {
			t.Fatal("domestic", bits)
		}
	}
	for _, android := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			for _, domestic := range []bool{false, true} {
				for _, relay := range []string{"", "127.0.0.1:9999", "synthetic\"\n"} {
					filters := []string{"+.invalid", "\"\n\x00"}
					if expected {
						o := configOptions{dnsRelayAddr: relay, trafficPolicy: trafficPolicy{DNSProtection: strict, DomesticWeb: domestic}}
						got, ok := rustConfig(1, o, android, filters)
						if !ok || got != dnsConfigSectionGo(filters, strict, domestic, android, relay) {
							t.Fatal("DNS FFI not exercised")
						}
						got, ok = rustConfig(2, o, android, nil)
						if !ok || got != dnsUpstreamsGo(strict, domestic, relay) {
							t.Fatal("upstream FFI not exercised")
						}
					}
					if dnsConfigSection(filters, strict, domestic, android, relay) != dnsConfigSectionGo(filters, strict, domestic, android, relay) {
						t.Fatal("DNS", android, strict, domestic, relay)
					}
					if dnsUpstreams(strict, domestic, relay) != dnsUpstreamsGo(strict, domestic, relay) {
						t.Fatal("upstreams")
					}
				}
			}
		}
	}
	large := configOptions{ControllerSecret: strings.Repeat("synthetic", 1000)}
	if got := generatedConfig(large); got != generatedConfigGo(large) {
		t.Fatal("capacity retry")
	}
	// Raw invalid UTF8 chain data is rejected by Rust, then preserved by Go.
	invalid := configOptions{ChainProxies: []byte{0xff, 0, 0xfe}}
	if _, ok := rustConfig(0, invalid, false, nil); ok {
		t.Fatal("invalid UTF8 should fallback")
	}
	if generatedConfig(invalid) != generatedConfigGo(invalid) {
		t.Fatal("raw bytes fallback")
	}

}
