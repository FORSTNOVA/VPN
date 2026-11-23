package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestMigrationDiagnosticSafeQualityGolden(t *testing.T) {
	for mask := 0; mask < 32; mask++ {
		body := []byte(fmt.Sprintf(`{"security":{"hosting":%t,"proxy":%t,"vpn":%t,"tor":%t,"anonymous":%t}}`, mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0, mask&16 != 0))
		q := parseIpQuality(body)
		score := 5
		if mask&1 != 0 {
			score = 25
		}
		if mask&6 != 0 {
			score += 30
		}
		if mask&8 != 0 {
			score += 50
		}
		if mask&16 != 0 {
			score += 15
		}
		if score > 100 {
			score = 100
		}
		if q.FraudScore != score || q.IsHosting != (mask&1 != 0) || q.IsNative == (mask&1 != 0) || q.IsProxy != (mask&2 != 0) || q.IsVPN != (mask&4 != 0) || q.IsTor != (mask&8 != 0) {
			t.Fatalf("mask %d quality %#v", mask, q)
		}
	}
	for _, isp := range []string{"Cloudflare", "AMAZON", "digitalocean", "Microsoft", "Google", "Oracle", "Alibaba", "Tencent", "Hetzner", "OVH", "Vultr", "Linode", "Choopa", "m247", "Data Center", "Hosting"} {
		b, _ := json.Marshal(map[string]any{"connection": map[string]any{"isp": isp}})
		q := parseIpQuality(b)
		if !q.IsHosting || q.IsNative || q.FraudScore != 25 {
			t.Fatalf("hosting recognition %s %#v", isp, q)
		}
	}
}
func TestMigrationDiagnosticSafeJSONShape(t *testing.T) {
	b, e := json.Marshal(diagnoseReport{})
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	if m["sites"] != nil {
		t.Fatal("nil sites changed")
	}
	if len(m["egress"].(map[string]any)) != 0 {
		t.Fatal("egress omitempty changed")
	}
	dns := m["dns"].(map[string]any)
	if len(dns) != 1 || dns["matchesExit"] != false {
		t.Fatal("dns zero shape")
	}
	quality := m["quality"].(map[string]any)
	if len(quality) != 10 || quality["isNative"] != false || quality["fraudScore"] != float64(0) {
		t.Fatal("quality must retain zero fields")
	}
}
