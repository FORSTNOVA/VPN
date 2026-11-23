package main

import (
	"strings"
	"testing"
)

func TestConfigIndependentLegacyRules(t *testing.T) {
	raw := []string{"A,B,DIRECT", "MATCH,DIRECT", "AND,((NETWORK,tcp),(DST-PORT,443)),REJECT", "A,B,\u0085REJECT\u2003", "A,B,DIRECT\xff"}
	o := configOptions{Rules: raw}
	got := generatedConfigGo(o)
	want := "rules:\n  - \"A,B,DIRECT\"\n  - \"A,B,\\u0085REJECT\\u2003\"\n  - \"AND,((NETWORK,udp),(DST-PORT,443)),REJECT\"\n  - \"MATCH,DIRECT\"\n  - \"AND,((NETWORK,tcp),(DST-PORT,443)),REJECT\"\n  - \"A,B,DIRECT\\xff\"\n  - MATCH,SmartVPN\n"
	at := strings.LastIndex(got, "rules:\n")
	if at < 0 || got[at:] != want {
		t.Fatalf("legacy third-segment/order golden: %q", got)
	}
	o.TUN = true
	o.DNSProtection = true
	o.SourceRuleOrder = true
	got = generatedConfigGo(o)
	want = "rules:\n  - \"A,B,\\u0085REJECT\\u2003\"\n  - \"AND,((NETWORK,udp),(DST-PORT,443)),REJECT\"\n  - \"MATCH,DIRECT\"\n  - \"AND,((NETWORK,tcp),(DST-PORT,443)),REJECT\"\n  - \"A,B,DIRECT\\xff\"\n  - MATCH,SmartVPN\n"
	at = strings.LastIndex(got, "rules:\n")
	if at < 0 || got[at:] != want {
		t.Fatalf("legacy reject second filter golden: %q", got[at:])
	}
}
