package main

import (
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestSubscriptionYAMLCodecs(t *testing.T) {
	const source = "proxies:\n  - {name: n, type: vmess, server: 127.0.0.1, port: 443, tls: true, network: ws, ws-opts: {path: /x, headers: {HOST: domain}}, extra: [null, false, 18446744073709551615]}\n  - {name: n, type: http}\n  - {name: skipped}\nsmartvpn-source-labels: {n: provider}\n"
	body := []byte(source)
	expected := os.Getenv("SMARTVPN_TEST_YAML_CODECS")
	wantNative := expected == "rust"
	nodes, parsed := nativeProfileNodes(body)
	objects, decoded := nativeProfileObjects(body)
	if expected != "" && (parsed != wantNative || decoded != wantNative) {
		t.Fatalf("nodes=%t objects=%t expected %q", parsed, decoded, expected)
	}
	var legacy struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(body, &legacy); err != nil {
		t.Fatal(err)
	}
	if decoded && !reflect.DeepEqual(objects, legacy.Proxies) {
		t.Fatal("raw proxy field type or value changed")
	}
	if parsed && !reflect.DeepEqual(nodes, yamlProfileNodesGo(body)) {
		t.Fatal("node metadata differs from Go")
	}
	if !reflect.DeepEqual(yamlProfileNodes(body), []proxyNode{{Name: "n", Type: "vmess", SourceLabel: "provider", Network: "ws", TLS: true, WsPath: true, WsHost: true}}) {
		t.Fatal("wrong node projection")
	}
	group := sourceGroup{Name: "group", Type: "select", Members: []string{"n"}, Use: []string{"provider"}, IncludeAll: true, Extra: map[string]any{"url": "https://example.invalid", "interval": 42}}
	doc := map[string]any{"proxies": legacy.Proxies, "proxy-groups": []sourceGroup{group}, "smartvpn-policy-sections": []policySection{{Label: "yes", Groups: []sourceGroup{group}}}, "smartvpn-source-rules": []string{"MATCH,n"}, "dns": map[string]any{"nameserver-policy": map[string][]string{"example.invalid": {"127.0.0.1"}}}, "smartvpn-source-labels": map[string]string{"n": "001", "extra": "2026-01-01", "weird": "a\u0085b\u2028z"}}
	encoded, ok := nativeMergedYAML(doc)
	if expected != "" && ok != wantNative {
		t.Fatalf("merged native=%t expected %q", ok, expected)
	}
	if ok {
		prior, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		if yaml.Unmarshal(encoded, &got) != nil || yaml.Unmarshal(prior, &want) != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("merged YAML differs after decoding")
		}
	}
	for _, src := range []string{"proxies: [{name: 010, type: http}]", "default: &p {name: sample, type: http}\nproxies: [{<<: *p}]", "proxies: [{name: n, type: http, port: 077}]"} {
		if _, ok := nativeProfileObjects([]byte(src)); os.Getenv("SMARTVPN_TEST_YAML_EVENTS") != "" && ok != (os.Getenv("SMARTVPN_TEST_YAML_EVENTS") == "rust") {
			t.Fatal("incorrect event decoder capability")
		}
		if !reflect.DeepEqual(yamlProfileNodes([]byte(src)), yamlProfileNodesGo([]byte(src))) {
			t.Fatal("compatibility projection changed")
		}
	}
	if _, ok := nativeMergedYAML(map[string]any{"proxies": []map[string]any{{"stamp": time.Unix(1, 0)}}}); ok {
		t.Fatal("timestamp silently converted")
	}
}

func TestSubscriptionYAMLAnchorsAndNumbers(t *testing.T) {
	const src = `base: &base {type: http, server: 127.0.0.1, port: 077, network: ws}
other: &other {port: 010, tls: true, ws-opts: {headers: {Host: local}, path: /ws}}
nested: &nested {<<: [*base, *other], name: 010}
proxies:
  - *nested
  - name: last
    port: 0x50
    <<: [*other, *base]
    extra: [08, 1e3, -0b11, 0o77, 1_000, .125, +4.0, 18446744073709551615, "077", "1e3", "2026-11-19"]
  - {<<: *nested, port: 33, name: third}
smartvpn-source-labels: {"010": 001}
`
	body := []byte(src)
	wantMode := os.Getenv("SMARTVPN_TEST_YAML_EVENTS")
	var reference struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(body, &reference); err != nil {
		t.Fatal(err)
	}
	nodes, projected := nativeProfileNodes(body)
	rows, parsed := nativeProfileObjects(body)
	if wantMode != "" && (parsed != (wantMode == "rust") || projected != (wantMode == "rust")) {
		t.Fatalf("expected %s: nodes=%t objects=%t", wantMode, projected, parsed)
	}
	if parsed && !reflect.DeepEqual(reference.Proxies, rows) {
		t.Fatalf("objects differ: got %#v want %#v", rows, reference.Proxies)
	}
	if parsed {
		literal, ok := nativeProfileObjects([]byte("proxies: [{name: literal, extra: {'<<': 3}}]"))
		if !ok || !reflect.DeepEqual(literal[0]["extra"], map[string]any{"<<": 3}) {
			t.Fatal("quoted merge key lost its literal value")
		}
	}
	wantNodes := yamlProfileNodesGo(body)
	if projected && !reflect.DeepEqual(nodes, wantNodes) {
		t.Fatalf("nodes differ: got %#v want %#v", nodes, wantNodes)
	}
	if len(wantNodes) != 3 || wantNodes[0].Name != "010" || wantNodes[0].SourceLabel != "001" || reference.Proxies[0]["port"] != 63 || reference.Proxies[1]["port"] != 80 {
		t.Fatal("unexpected Go reference")
	}
	merged, _, err := mergeSubscriptionSources([]SubscriptionSource{{Label: "fixture", Body: body}})
	if err != nil {
		t.Fatal(err)
	}
	// The converter skips the raw first row because its name has integer type.
	// Compare the surviving extension values after the same YAML marshal/read boundary.
	var result struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err = yaml.Unmarshal(merged, &result); err != nil || len(result.Proxies) != 2 {
		t.Fatalf("merged rows %d err %v", len(result.Proxies), err)
	}
	prior, err := yaml.Marshal(reference.Proxies[1]["extra"])
	if err != nil {
		t.Fatal(err)
	}
	var want []any
	if err = yaml.Unmarshal(prior, &want); err != nil || !reflect.DeepEqual(result.Proxies[0]["extra"], want) {
		t.Fatalf("merged scalar types: %#v / %#v", result.Proxies[0]["extra"], want)
	}
	if parsed {
		_, serialized := nativeMergedYAML(map[string]any{"proxies": rows})
		if !serialized {
			t.Fatal("finite numeric merge used the fallback")
		}
	}
	for _, bad := range []string{"proxies: [{<<: missing}]", "defaults: &seq [{type: http}]\nproxies: [{<<: *seq}]", "proxies: [{name: x, name: y}]", "proxies: [*unknown]", "proxies: [&self {name: recursive, child: *self}]"} {
		if _, ok := nativeProfileObjects([]byte(bad)); ok {
			t.Fatal("invalid document accepted")
		}
	}
}

func TestSubscriptionYAMLNumericParity(t *testing.T) {
	equal := func(a, b any) bool {
		if x, ok := a.(float64); ok {
			y, ok := b.(float64)
			return ok && (math.Float64bits(x) == math.Float64bits(y) || (math.IsNaN(x) && math.IsNaN(y)))
		}
		return reflect.DeepEqual(a, b)
	}
	for _, literal := range []string{"0", "00", "010", "0o17", "-0O10", "+0b11", "0xF_F", "-0x8000000000000000", "0xffffffffffffffff", "18446744073709551615", "18446744073709551616", "08", "+08", "08_9", "1__0", "1.", "-.25", ".2_5", ".2__5", "-0.0", "1e-310", "2.2250738585072014e-308", "4.9406564584124654e-324", "1e308", "1e999", "1e6", ".nan", "+.Inf", "-.INF", "'010'", "'1e2'", "'-.Inf'"} {
		body := []byte("proxies: [{name: n, type: http, n: " + literal + "}]")
		var legacy struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(body, &legacy); err != nil {
			t.Fatal(err)
		}
		rows, ok := nativeProfileObjects(body)
		if !ok {
			if os.Getenv("SMARTVPN_TEST_YAML_EVENTS") == "rust" {
				t.Fatalf("unexpected fallback for %q", literal)
			}
			continue
		}
		got := rows[0]["n"]
		want := legacy.Proxies[0]["n"]
		if !equal(got, want) {
			t.Fatalf("parse %q: %T %v / Go %T %v", literal, got, got, want, want)
		}
		encoded, ok := nativeMergedYAML(map[string]any{"proxies": rows})
		if !ok && os.Getenv("SMARTVPN_TEST_YAML_EVENTS") != "rust" {
			continue
		}
		if !ok {
			t.Fatalf("marshal fallback for %q", literal)
		}
		var decoded struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		oracle, err := yaml.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err = yaml.Unmarshal(oracle, &legacy); err != nil {
			t.Fatal(err)
		}
		got, want = decoded.Proxies[0]["n"], legacy.Proxies[0]["n"]
		if !equal(got, want) {
			t.Fatalf("marshal %q: %T %v / Go %T %v", literal, got, got, want, want)
		}
	}
}
