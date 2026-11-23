package main

import (
	"bytes"
	"encoding/base64"
	"gopkg.in/yaml.v3"
	"reflect"
	"testing"
)

func TestConverterIndependentLegacyBytes(t *testing.T) {
	raw := []byte("# keep comment\nproxies:\n  - {name: keep, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: synthetic}\n")
	for _, body := range [][]byte{raw, []byte(base64.StdEncoding.EncodeToString(raw))} {
		out, n, err := ensureClashYAML(body)
		if err != nil || !bytes.Equal(out, raw) || len(n) != 1 {
			t.Fatalf("preservation %q %#v %v", out, n, err)
		}
	}
}

func TestConverterIndependentLegacyProjectionMismatch(t *testing.T) {
	body := []byte("proxies:\n- {name: missing-type, server: example.invalid}\n- {name: x, type: ss, network: ws, tls: true, ws-opts: {path: /a, headers: {HOST: example.invalid}}}\n- {name: x, type: trojan, network: tcp}\nsmartvpn-source-labels: {x: source}\n")
	p, n, err := parseSubscriptionBodyToProxies(body)
	if err != nil || len(p) != 3 || len(n) != 1 {
		t.Fatalf("lengths %d %#v %v", len(p), n, err)
	}
	want := proxyNode{Name: "x", Type: "ss", Network: "ws", TLS: true, WsHost: true, WsPath: true, SourceLabel: "source"}
	if !reflect.DeepEqual(n[0], want) {
		t.Fatalf("node %#v", n[0])
	}
}

func TestConverterIndependentLegacyOrphanAndIndexMerge(t *testing.T) {
	body := []byte("anytls://synthetic@example.invalid:443#orphan\ntrojan://synthetic@example.invalid:443#real\n")
	p, n, err := parseSubscriptionBodyToProxies(body)
	if err != nil || len(p) != 1 || len(n) != 2 || n[0].Name != "orphan" || n[1].Name != "real" {
		t.Fatalf("orphan %v %#v %v", p, n, err)
	}
	_, n, err = mergeSubscriptionSources([]SubscriptionSource{{Label: "S", Prefix: "P", Body: body}})
	if err != nil || len(n) != 2 || n[0].Name != "P real" || n[0].Type != "anytls" || n[0].SourceLabel != "S" || n[1].Name != "real" || n[1].SourceLabel != "" {
		t.Fatalf("index quirk %#v %v", n, err)
	}
}

func TestConverterIndependentLegacyMergePolicy(t *testing.T) {
	body := []byte("proxies:\n- {name: A, type: ss}\n- {name: A, type: ss}\n- {name: A (2), type: ss}\nproxy-groups:\n- {name: G, type: select, proxies: [A, DIRECT, REJECT]}\nrules: [\"DOMAIN,example.invalid,G\", \"IP-CIDR,192.0.2.0/24,A,no-resolve\"]\n")
	out, n, err := mergeSubscriptionSources([]SubscriptionSource{{Label: "L", Body: body}, {Label: "L", Body: []byte("proxies: [{name: B, type: ss}]\n")}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	ps := doc["proxies"].([]any)
	names := []string{}
	for _, p := range ps {
		names = append(names, p.(map[string]any)["name"].(string))
	}
	if !reflect.DeepEqual(names, []string{"A", "A (2)", "A (2) (2)", "B"}) {
		t.Fatalf("names %v", names)
	}
	groups := doc["proxy-groups"].([]any)
	g := groups[0].(map[string]any)
	if g["name"] != "L / G" || !reflect.DeepEqual(g["proxies"], []any{"A (2)", "DIRECT", "REJECT"}) {
		t.Fatalf("groups %#v", g)
	}
	if !reflect.DeepEqual(doc["smartvpn-source-rules"], []any{"DOMAIN,example.invalid,L / G", "IP-CIDR,192.0.2.0/24,A (2),no-resolve"}) {
		t.Fatalf("rules %#v", doc)
	}
	sections := doc["smartvpn-policy-sections"].([]any)
	sg := sections[0].(map[string]any)["groups"].([]any)[0].(map[string]any)
	if sg["name"] != "G" || !reflect.DeepEqual(sg["proxies"], []any{"A", "DIRECT", "REJECT"}) {
		t.Fatalf("original section mutated %#v", sg)
	}
	// Node extraction deduplicates before merge, then merge still rewrites by index.
	if len(n) != 3 || n[0].Name != "A" || n[1].Name != "A (2)" || n[2].SourceLabel != "L (2)" {
		t.Fatalf("nodes %#v", n)
	}
}

func TestConverterIndependentLegacyVmessDialects(t *testing.T) {
	link := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"ps":"  V  ","add":"example.invalid","port":"443","net":"unsupported","tls":"none"}`))
	p, n, err := parseSubscriptionBodyToProxies([]byte(link))
	if err != nil || len(p) != 1 || len(n) != 1 || p[0]["uuid"] != "00000000-0000-0000-0000-000000000000" || n[0].Network != "unsupported" {
		t.Fatalf("subscription permissive dialect %v %#v %v", p, n, err)
	}
	if _, err = parseNodeURI(link); err == nil {
		t.Fatal("manual dialect must reject missing UUID")
	}
	if _, _, err = parseSubscriptionBodyToProxies([]byte("VMESS://" + link[len("vmess://"):])); err == nil {
		t.Fatal("uppercase prefix quirk changed")
	}
	sing := []byte(`{"outbounds":[{"type":"vmess","tag":" S ","server":" example.invalid ","server_port":0,"uuid":"synthetic","transport":{"type":""}}]}`)
	p, n = parseSingboxProfile(sing)
	if len(p) != 1 || len(n) != 1 || n[0].Network != "" || p[0]["port"] != 0 || p[0]["server"] != "example.invalid" {
		t.Fatalf("singbox permissive dialect %v %#v", p, n)
	}
}

func TestConverterIndependentLegacyFormatPriority(t *testing.T) {
	body := []byte(`{"outbounds":[{"type":"shadowsocks","tag":"sing","method":"aes-128-gcm","password":"synthetic"}],"proxies":[{"name":"clash","type":"ss"}]}`)
	for _, b := range [][]byte{body, []byte(base64.StdEncoding.EncodeToString(body))} {
		p, n, e := parseSubscriptionBodyToProxies(b)
		if e != nil || len(p) != 1 || len(n) != 1 || n[0].Name != "sing" {
			t.Fatalf("priority %v %#v %v", p, n, e)
		}
	}
	// JSON Clash is re-encoded; direct YAML and decoded YAML are byte-preserving.
	body = []byte(`{"proxies":[{"name":"clash","type":"ss"}],"unknown":"retain only on byte-preserving branch"}`)
	out, _, err := ensureClashYAML(body)
	if err != nil || bytes.Equal(out, body) || bytes.Contains(out, []byte("unknown")) {
		t.Fatalf("JSON reencode %q %v", out, err)
	}
}

func TestConverterIndependentLegacyDNSFirstWins(t *testing.T) {
	a := []byte("proxies: [{name: A, type: ss, server: shared.invalid}]\ndns: {nameserver-policy: {shared.invalid: [https://one.invalid/dns-query]}}\n")
	b := []byte("proxies: [{name: B, type: ss, server: shared.invalid}]\ndns: {nameserver-policy: {shared.invalid: [https://two.invalid/dns-query]}}\n")
	out, _, err := mergeSubscriptionSources([]SubscriptionSource{{Label: "one", Body: a}, {Label: "two", Body: b}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	got := doc["dns"].(map[string]any)["nameserver-policy"].(map[string]any)["shared.invalid"]
	if !reflect.DeepEqual(got, []any{"https://one.invalid/dns-query"}) {
		t.Fatalf("DNS first wins %#v", got)
	}
}

func TestConverterIndependentLegacyProtocolEdges(t *testing.T) {
	body := []byte(`{"outbounds":[{"type":"hysteria2","tag":"H","password":"synthetic","obfs":{"type":"salamander","password":"obfs-synthetic"}},{"type":"tuic","tag":"T1","uuid":"uuid-only"},{"type":"tuic","tag":"T2","password":"password-only"},{"type":"trojan","tag":"T3","password":"synthetic","tls":{"enabled":false,"server_name":"still-sni"}},{"type":"vless","tag":"V","uuid":"u","tls":{"enabled":false,"reality":{"enabled":true,"public_key":"ignored"}}}]}`)
	p, n := parseSingboxProfile(body)
	if len(p) != 5 || len(n) != 5 {
		t.Fatalf("protocol acceptance %v %#v", p, n)
	}
	if p[0]["obfs"] != "salamander" || p[0]["obfs-password"] != "obfs-synthetic" || p[1]["password"] != "" || p[2]["uuid"] != "" {
		t.Fatalf("credential/obfs projection %#v", p)
	}
	if _, ok := p[3]["tls"]; ok || !n[3].TLS || p[3]["sni"] != "still-sni" {
		t.Fatal("trojan independent TLS metadata changed")
	}
	if _, ok := p[4]["reality-opts"]; ok || n[4].TLS {
		t.Fatal("disabled outer TLS must suppress Reality")
	}
}

func converterIndependentUnicodeCorpus() [][]byte {
	out := [][]byte{
		[]byte(`{"outbounds":[{"type":"hysterİa2","tag":"H","password":"synthetic"}]}`),
		[]byte(`{"outbounds":[{"type":"tuİc","tag":"T","uuid":"synthetic"}]}`),
		[]byte(`{"outbounds":[{"type":"VMESS","tag":"V","uuid":"synthetic","security":"AUTO","tls":{"enabled":true},"transport":{"type":"WEBSOCKET","headers":{"Host":"synthetic.invalid"}}}]}`),
		[]byte("proxies: [{name: '\u0130', type: ss, tls: '\u0130'}]"),
	}
	out = append(out, []byte("{\"outbounds\":[{\"type\":\"trojan\",\"tag\":\"N\xff\",\"password\":\"synthetic\"}]}"))
	out = append(out, []byte(`{"outbounds":[{"type":"vmess","tag":"Unicode-transport","uuid":"synthetic","tls":{"enabled":true,"server_name":"İ.invalid"},"transport":{"type":"İ","path":"/K"}}]}`))
	out = append(out, []byte("vmess://"+base64.StdEncoding.EncodeToString([]byte(`{"ps":"Unicode-tls","add":"example.invalid","port":"443","id":"synthetic","tls":"İ","net":"İ"}`))))
	return out
}
func TestConverterIndependentLegacyUnicode(t *testing.T) {
	for i, b := range converterIndependentUnicodeCorpus() {
		p, n, e := parseSubscriptionBodyToProxies(b)
		if e != nil || len(p) != 1 || len(n) != 1 {
			t.Fatalf("Unicode case%d %#v %#v %v", i, p, n, e)
		}
		if i == 0 && p[0]["type"] != "hysteria2" {
			t.Fatal("Go simple lower hysterIa2")
		}
		if i == 1 && p[0]["type"] != "tuic" {
			t.Fatal("Go simple lower tuIc")
		}
		if i == 4 && n[0].Name != "N\ufffd" {
			t.Fatalf("JSON invalidUTF8 replacement %q", n[0].Name)
		}
	}
}
