package main

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSubscriptionHTTPIndependentPayloadLegacy(t *testing.T) {
	cases := []struct{ body, format string }{
		{"", "empty"}, {"  ", "unknown"}, {"proxies: []", "clash-yaml"}, {"{broken", "singbox-json"},
		{"hy2://invalid", "uri-list"}, {"hysteria://invalid", "uri-list"}, {"VMESS://x", "unknown"},
		{"#comment\nproxies: []", "clash-yaml"},
	}
	for _, c := range cases {
		r := analyzeSubscriptionPayload([]byte(c.body))
		if r.Format != c.format || r.Bytes != len(c.body) || r.Present != (len(c.body) > 0) {
			t.Fatalf("%q: %+v", c.body, r)
		}
		if r.UriCounts == nil || r.Networks == nil || r.TlsModes == nil {
			t.Fatal("nil maps")
		}
	}
	for _, p := range []struct{ body, format string }{{"proxies: []", "base64-clash-yaml"}, {"{broken", "base64-singbox-json"}, {"vless://invalid", "base64-uri-list"}} {
		raw := base64.StdEncoding.EncodeToString([]byte(p.body))
		r := analyzeSubscriptionPayload([]byte(raw))
		if r.Format != p.format || r.Bytes != len(raw) {
			t.Fatalf("base64 %+v", r)
		}
	}
	vm := base64.RawStdEncoding.EncodeToString([]byte(`{"ps":"normal","net":"ws","tls":"reality","host":"h","path":"/"}`))
	r := analyzeSubscriptionPayload([]byte("vmess://" + vm + "\n vmess://" + vm + "\nhy2://x"))
	if r.UriCounts["vmess"] != 1 || r.ValidVmess != 1 || r.Networks["ws"] != 1 || r.TlsModes["reality"] != 1 || r.WsHostSet != 1 || r.WsPathSet != 1 {
		t.Fatalf("URI columns %+v", r)
	}
	if _, ok := r.UriCounts["hy2"]; ok {
		t.Fatal("legacy counter excludes hy2")
	}
}

func TestPolicyDNSIndependentLegacyTargetsAndAlias(t *testing.T) {
	p := sourcePolicy{Groups: []sourceGroup{
		{Name: "same", Members: []string{"DIRECT"}}, {Name: "same", Members: []string{"REJECT"}},
		{Name: "nested", Members: []string{"same", "REJECT"}}, {Name: "mixed", Members: []string{"DIRECT", "REJECT"}},
		{Name: "self", Members: []string{"self"}}, {Name: "use", Members: []string{"DIRECT"}, Use: []string{"x"}},
		{Name: "all", Members: []string{"DIRECT"}, IncludeAll: true},
	}}
	got := policyTargets(p)
	for k, w := range map[string]string{"same": "REJECT", "nested": "REJECT", "mixed": "", "self": "", "use": "", "all": "", "SmartVPN": "SmartVPN"} {
		if got[k] != w {
			t.Fatalf("%s=%q want %q", k, got[k], w)
		}
	}
	backing := []string{" DOMAIN ,example.com,DIRECT ", "sentinel", "untouched"}
	p.Rules = backing[:1]
	p.MergedRules = []string{"IP-CIDR,::ffff:192.0.2.1/128,REJECT,no-resolve"}
	preview, active := supportedRules(p)
	if backing[1] != p.MergedRules[0] || backing[2] != "untouched" {
		t.Fatal("append backing identity changed")
	}
	if len(preview) != 2 || preview[0].Source != backing[0] || !reflect.DeepEqual(active, []string{"DOMAIN,example.com,DIRECT", p.MergedRules[0]}) {
		t.Fatalf("rules %#v %#v", preview, active)
	}
	p.Rules = []string{"DOMAIN,has space,DIRECT", "DOMAIN,x,DIRECT,no-resolve", "IP-CIDR6,192.0.2.1/24,DIRECT", "IP-CIDR,broken,DIRECT", "MATCH,DIRECT"}
	p.MergedRules = nil
	_, active = supportedRules(p)
	if active != nil {
		t.Fatalf("invalid rules %#v", active)
	}
	raw := []byte("rules:\n - DOMAIN,has space,DIRECT,no-resolve,extra\n - IP-CIDR,broken,DIRECT\n - DOMAIN,x,PASS\n")
	if v := subscriptionRules(raw); !reflect.DeepEqual(v, []string{"DOMAIN,has space,DIRECT,no-resolve", "IP-CIDR,broken,DIRECT"}) {
		t.Fatalf("distinct lax rules %#v", v)
	}
}

func TestPolicyDNSIndependentLegacyFiltering(t *testing.T) {
	for _, c := range []struct {
		in, out string
		ok      bool
	}{{" \"A.B.Example.COM.\" ", "+.example.com", true}, {"a..com", "", false}, {"例子.com", "", false}, {"127.0.0.1", "", false}, {"localhost", "", false}, {"-x.com", "", false}} {
		s, ok := hostnameSuffix(c.in)
		if s != c.out || ok != c.ok {
			t.Fatalf("suffix %q %q %v", c.in, s, ok)
		}
	}
	var b strings.Builder
	for i := 256; i >= 0; i-- {
		fmt.Fprintf(&b, "server: x.d%03d.example\n", i)
	}
	d := proxyServerDomains([]byte(b.String()))
	if len(d) != 256 || d[0] != "+.d001.example" || d[255] != "+.d256.example" {
		t.Fatalf("cap before sort %v", d)
	}
	if v := mergeProxyServerDomains(nil, []string{"z", "", "a", "z"}); !reflect.DeepEqual(v, []string{"a", "z"}) {
		t.Fatal(v)
	}
	profile := []byte("proxies:\n - {server: A.EXAMPLE.COM}\n - {server: A.EXAMPLE.COM}\n - {server: b.example.com}\ndns:\n nameserver-policy:\n  a.example.com: [https://resolver.example/dns-query, 'https://user@bad.example/x', 'https://bad.example/x#f', 42, udp://bad.example]\n  '*.example.com': https://wild.example/dns-query\n")
	want := map[string][]string{"a.example.com": {"https://resolver.example/dns-query"}}
	if v := entryDNS(profile); !reflect.DeepEqual(v, want) {
		t.Fatalf("exact policy %#v", v)
	}
	if v := proxyServerHostname(profile); v != "A.EXAMPLE.COM" {
		t.Fatalf("hostname original %q", v)
	}
}
