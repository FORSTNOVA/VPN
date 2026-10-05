package main

import "testing"

const ipwhoisSample = `{
  "ip": "203.0.113.7",
  "success": true,
  "type": "IPv4",
  "continent": "Asia",
  "country": "Japan",
  "country_code": "JP",
  "region": "Tokyo",
  "city": "Tokyo",
  "connection": {
    "asn": 2497,
    "org": "Internet Initiative Japan",
    "isp": "IIJ",
    "domain": "iij.ad.jp"
  }
}`

func TestParseIpwhois(t *testing.T) {
	facts, err := parseIpwhois([]byte(ipwhoisSample))
	if err != nil {
		t.Fatal(err)
	}
	if facts.IPv4 != "203.0.113.7" || facts.IPv6 != "" {
		t.Fatalf("unexpected address: %+v", facts)
	}
	if facts.Country != "Japan" || facts.CountryCode != "JP" || facts.City != "Tokyo" {
		t.Fatalf("unexpected location: %+v", facts)
	}
	if facts.ASN != "AS2497" || facts.ISP != "IIJ" || facts.Org != "Internet Initiative Japan" {
		t.Fatalf("unexpected operator: %+v", facts)
	}
}

func TestParseIpwhoisFilesAnIPv6ExitCorrectly(t *testing.T) {
	body := `{"ip":"2001:db8::1","success":true,"country":"Japan","country_code":"JP","connection":{"asn":2497}}`
	facts, err := parseIpwhois([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if facts.IPv6 != "2001:db8::1" || facts.IPv4 != "" {
		t.Fatalf("a v6 exit must not be reported as v4: %+v", facts)
	}
}

func TestEgressParsersRefuseWhatTheyCannotUse(t *testing.T) {
	for name, body := range map[string]string{
		"empty":          ``,
		"garbage":        `not json`,
		"no address":     `{"success":true,"country":"Japan"}`,
		"wrong shape":    `[1,2,3]`,
		"failed service": `{"success":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			if facts, err := parseIpwhois([]byte(body)); err == nil {
				t.Fatalf("expected a refusal, got %+v", facts)
			}
			if facts, err := parseIpSb([]byte(body)); err == nil {
				t.Fatalf("expected a refusal, got %+v", facts)
			}
		})
	}
}

func TestParseIpSbAcceptsBothSpellings(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "organisation",
			body: `{"ip":"198.51.100.4","country":"Singapore","country_code":"SG","city":"Singapore","asn":14061,"organization":"DigitalOcean, LLC"}`,
			want: "DigitalOcean, LLC",
		},
		{
			name: "org with a string asn",
			body: `{"ip":"198.51.100.4","asn":"AS14061","org":"DigitalOcean"}`,
			want: "DigitalOcean",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, err := parseIpSb([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if facts.IPv4 != "198.51.100.4" || facts.ASN != "AS14061" {
				t.Fatalf("unexpected facts: %+v", facts)
			}
			if facts.Org != tc.want {
				t.Fatalf("org = %q, want %q", facts.Org, tc.want)
			}
		})
	}
}

func TestAsnLabel(t *testing.T) {
	cases := map[any]string{
		float64(2497): "AS2497",
		"2497":        "AS2497",
		"AS2497":      "AS2497",
		"as2497":      "as2497",
		float64(0):    "",
		"":            "",
		nil:           "",
	}
	for raw, want := range cases {
		if got := asnLabel(raw); got != want {
			t.Errorf("asnLabel(%v) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseIpQuality(t *testing.T) {
	hostingSample := `{
		"connection": {"asn": 13335, "isp": "Cloudflare", "org": "Cloudflare, Inc."},
		"security": {"anonymous": false, "proxy": true, "vpn": false, "tor": false, "hosting": true}
	}`
	q := parseIpQuality([]byte(hostingSample))
	if !q.IsHosting || !q.IsProxy {
		t.Fatalf("expected hosting and proxy, got: %+v", q)
	}
	if q.FraudScore <= 25 {
		t.Fatalf("expected fraud score > 25, got: %d", q.FraudScore)
	}

	residentialSample := `{
		"connection": {"asn": 4134, "isp": "Chinanet", "org": "China Telecom"},
		"security": {"anonymous": false, "proxy": false, "vpn": false, "tor": false, "hosting": false}
	}`
	q2 := parseIpQuality([]byte(residentialSample))
	if q2.IsHosting || !q2.IsNative {
		t.Fatalf("expected residential native IP, got: %+v", q2)
	}
	if q2.FraudScore > 25 {
		t.Fatalf("expected low fraud score for clean residential, got: %d", q2.FraudScore)
	}
}
