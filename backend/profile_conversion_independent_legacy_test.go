package main

import (
	"reflect"
	"testing"
)

func TestProfileConversionIndependentLegacyUnique(t *testing.T) {
	in := []proxyNode{{Name: "X", Type: "first"}, {Name: "X", Type: "last"}, {Name: "Y", SourceLabel: "source"}}
	out := uniqueNodes(in)
	if !reflect.DeepEqual(out, []proxyNode{in[0], in[2]}) {
		t.Fatalf("first wins %#v", out)
	}
}

func TestProfileConversionIndependentLegacyURI(t *testing.T) {
	n, err := parseNodeURI(" \tTrojan://synthetic@example.invalid#%2520Name%2520 \n")
	if err != nil || n.Name != "Name" || n.Port != 443 || n.Server != "example.invalid" || n.Type != "trojan" {
		t.Fatalf("decode/default %#v %v", n, err)
	}
	for _, s := range []string{"anytls://synthetic@example.invalid#A", "no scheme", "vless://example.invalid:443#A"} {
		if _, err := parseNodeURI(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestProfileConversionIndependentLegacyURL(t *testing.T) {
	for _, s := range []string{" https://example.invalid/a ", mergedSubscriptionURL, "trojan://synthetic@example.invalid:443#A"} {
		if err := validateSubscriptionURL(s); err != nil {
			t.Fatalf("valid %q: %v", s, err)
		}
	}
	for _, s := range []string{"", "https://", "ftp://example.invalid/a"} {
		if err := validateSubscriptionURL(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
