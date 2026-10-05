package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestHostnameSuffix(t *testing.T) {
	cases := []struct {
		value string
		want  string
		ok    bool
	}{
		{"abc123.qos.onl", "+.qos.onl", true},
		{"ABC123.Qos.ONL", "+.qos.onl", true},
		{"node.example.com", "+.example.com", true},
		{`"node.example.com"`, "+.example.com", true},
		{"node.example.com.", "+.example.com", true},
		{"a.b.c.example.co.jp", "+.co.jp", true},
		{"1.2.3.4", "", false},
		{"2001:db8::1", "", false},
		{"localhost", "", false},
		{"", "", false},
		{"bad_underscore.example.com", "", false},
		{"high-.example.com", "", false},
		{"-low.example.com", "", false},
		{"node.example.com\nevil: true", "", false},
	}
	for _, tc := range cases {
		got, ok := hostnameSuffix(tc.value)
		if ok != tc.ok || got != tc.want {
			t.Errorf("hostnameSuffix(%q) = (%q, %v), want (%q, %v)", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHostnameSuffixKeepsATopLevelDomainOut(t *testing.T) {
	// A single-label entry would exclude a whole top-level domain from fake-ip.
	if entry, ok := hostnameSuffix("onl"); ok {
		t.Fatalf("a bare label must be refused, got %q", entry)
	}
}

func TestProxyServerDomains(t *testing.T) {
	body := `
proxies:
  - name: "日本 01"
    type: vmess
    server: aaa.qos.onl
    port: 10008
  - name: 家宽
    type: trojan
    server: 1.2.3.4
    sni: bbb.qos.onl
  - name: "日本 02"
    type: vmess
    server: aaa.qos.onl
    port: 10010
    ws-opts:
      headers:
        Host: ccc.qos.onl
`
	got := proxyServerDomains([]byte(body))
	if want := []string{"+.qos.onl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestProxyServerDomainsIgnoresNonServerKeys(t *testing.T) {
	// Only what the kernel dials matters: sni and Host are never resolved to
	// connect, so excluding them would widen the filter for nothing.
	if got := proxyServerDomains([]byte("  sni: aaa.qos.onl\n  Host: bbb.qos.onl\n")); len(got) != 0 {
		t.Fatalf("got %v, want nothing", got)
	}
	if got := proxyServerDomains([]byte("  server-port: 443\n  udp: true\n")); len(got) != 0 {
		t.Fatalf("got %v, want nothing", got)
	}
}

func TestProxyServerDomainsIsBoundedAndSorted(t *testing.T) {
	var body strings.Builder
	for i := 0; i < maxProxyServerDomains*2; i++ {
		body.WriteString("    server: node" + strconv.Itoa(i) + ".example" + strconv.Itoa(i) + ".com\n")
	}
	got := proxyServerDomains([]byte(body.String()))
	if len(got) != maxProxyServerDomains {
		t.Fatalf("got %d entries, want the cap of %d", len(got), maxProxyServerDomains)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("entries must be unique and sorted: %v", got[:4])
		}
	}
}

func TestProxyServerDomainsIgnoresAMissingProfile(t *testing.T) {
	if got := proxyServerDomains(nil); len(got) != 0 {
		t.Fatalf("got %v, want nothing", got)
	}
}
