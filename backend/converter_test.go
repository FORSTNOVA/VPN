package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSplitSubscriptionURLs(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "single url",
			input: "https://example.com/sub1",
			want:  []string{"https://example.com/sub1"},
		},
		{
			name:  "pipe separated",
			input: "https://example.com/sub1|https://example.com/sub2|https://example.com/sub3",
			want:  []string{"https://example.com/sub1", "https://example.com/sub2", "https://example.com/sub3"},
		},
		{
			name:  "newline separated",
			input: "https://example.com/sub1\r\nhttps://example.com/sub2\nhttps://example.com/sub3",
			want:  []string{"https://example.com/sub1", "https://example.com/sub2", "https://example.com/sub3"},
		},
		{
			name:  "mixed with spaces and empty lines",
			input: "  https://example.com/sub1 | \n\n https://example.com/sub2\n  ",
			want:  []string{"https://example.com/sub1", "https://example.com/sub2"},
		},
		{
			name:  "non http strings dropped",
			input: "not-a-url|https://example.com/sub1|ftp://ignored",
			want:  []string{"https://example.com/sub1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSubscriptionURLs(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d urls, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("url[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseSingboxProfile(t *testing.T) {
	singboxJSON := []byte(`{
		"outbounds": [
			{
				"type": "shadowsocks",
				"tag": "SS-Node-1",
				"server": "1.2.3.4",
				"server_port": 8388,
				"method": "aes-128-gcm",
				"password": "secret_password"
			},
			{
				"type": "vmess",
				"tag": "VMess-Node-2",
				"server": "5.6.7.8",
				"server_port": 443,
				"uuid": "22222222-3333-4444-5555-666666666666",
				"alter_id": 0,
				"security": "auto",
				"tls": {
					"enabled": true,
					"server_name": "vmess.example.com",
					"insecure": true
				},
				"transport": {
					"type": "ws",
					"path": "/ws",
					"headers": {
						"Host": "vmess.example.com"
					}
				}
			},
			{
				"type": "vless",
				"tag": "VLESS-Reality",
				"server": "9.10.11.12",
				"server_port": 443,
				"uuid": "33333333-4444-5555-6666-777777777777",
				"flow": "xtls-rprx-vision",
				"tls": {
					"enabled": true,
					"server_name": "vless.example.com",
					"reality": {
						"enabled": true,
						"public_key": "reality_pub_key",
						"short_id": "reality_short_id"
					}
				}
			},
			{
				"type": "hysteria2",
				"tag": "Hy2-Node",
				"server": "13.14.15.16",
				"server_port": 8443,
				"password": "hy2_password",
				"tls": {
					"server_name": "hy2.example.com"
				},
				"obfs": {
					"type": "salamander",
					"password": "obfs_pass"
				}
			},
			{
				"type": "tuic",
				"tag": "TUIC-Node",
				"server": "17.18.19.20",
				"server_port": 9443,
				"uuid": "tuic-uuid-4444",
				"password": "tuic-password",
				"congestion_controller": "bbr",
				"tls": {
					"server_name": "tuic.example.com"
				}
			},
			{
				"type": "direct",
				"tag": "direct"
			},
			{
				"type": "block",
				"tag": "block"
			},
			{
				"type": "vmess",
				"tag": "剩余流量：100GB",
				"server": "1.1.1.1",
				"server_port": 443
			}
		]
	}`)

	proxies, nodes := parseSingboxProfile(singboxJSON)
	if len(proxies) != 5 {
		t.Fatalf("got %d proxies, want 5: %+v", len(proxies), proxies)
	}
	if len(nodes) != 5 {
		t.Fatalf("got %d nodes, want 5: %+v", len(nodes), nodes)
	}

	byName := make(map[string]map[string]any)
	for _, p := range proxies {
		byName[stringValue(p["name"])] = p
	}

	// Verify SS
	ss := byName["SS-Node-1"]
	if ss["type"] != "ss" || ss["server"] != "1.2.3.4" || ss["port"] != 8388 || ss["cipher"] != "aes-128-gcm" {
		t.Errorf("ss proxy unexpected: %+v", ss)
	}

	// Verify VMess
	vmess := byName["VMess-Node-2"]
	if vmess["type"] != "vmess" || vmess["tls"] != true || vmess["servername"] != "vmess.example.com" || vmess["skip-cert-verify"] != true {
		t.Errorf("vmess proxy unexpected: %+v", vmess)
	}

	// Verify VLESS Reality
	vless := byName["VLESS-Reality"]
	if vless["type"] != "vless" || vless["flow"] != "xtls-rprx-vision" {
		t.Errorf("vless proxy unexpected: %+v", vless)
	}
	ropts, ok := vless["reality-opts"].(map[string]any)
	if !ok || ropts["public-key"] != "reality_pub_key" || ropts["short-id"] != "reality_short_id" {
		t.Errorf("vless reality-opts unexpected: %+v", vless)
	}

	// Verify Hy2
	hy2 := byName["Hy2-Node"]
	if hy2["type"] != "hysteria2" || hy2["obfs"] != "salamander" || hy2["obfs-password"] != "obfs_pass" {
		t.Errorf("hy2 proxy unexpected: %+v", hy2)
	}

	// Verify TUIC
	tuic := byName["TUIC-Node"]
	if tuic["type"] != "tuic" || tuic["congestion-controller"] != "bbr" || tuic["uuid"] != "tuic-uuid-4444" {
		t.Errorf("tuic proxy unexpected: %+v", tuic)
	}
}

func TestParseSSRLink(t *testing.T) {
	// ssr://127.0.0.1:1234:auth_sha1_v4:aes-128-cfb:plain:dGVzdA/?remarks=dGVzdF9ub2Rl&obfsparam=b2Jmc19wYXJhbQ&protoparam=cHJvdG9fcGFyYW0
	// base64 password "dGVzdA" -> "test"
	// base64 remarks "dGVzdF9ub2Rl" -> "test_node"
	// base64 obfsparam "b2Jmc19wYXJhbQ" -> "obfs_param"
	// base64 protoparam "cHJvdG9fcGFyYW0" -> "proto_param"
	raw := "127.0.0.1:1234:auth_sha1_v4:aes-128-cfb:plain:dGVzdA/?remarks=dGVzdF9ub2Rl&obfsparam=b2Jmc19wYXJhbQ&protoparam=cHJvdG9fcGFyYW0"
	encoded := "ssr://" + base64.RawURLEncoding.EncodeToString([]byte(raw))

	node, err := parseNodeURI(encoded)
	if err != nil {
		t.Fatalf("parseNodeURI error: %v", err)
	}
	if node.Type != "ssr" || node.Name != "test_node" || node.Server != "127.0.0.1" || node.Port != 1234 {
		t.Fatalf("unexpected node: %+v", node)
	}
	p := node.Proxy
	if p["cipher"] != "aes-128-cfb" || p["password"] != "test" || p["protocol"] != "auth_sha1_v4" || p["obfs"] != "plain" {
		t.Errorf("unexpected proxy properties: %+v", p)
	}
	if p["obfs-param"] != "obfs_param" || p["protocol-param"] != "proto_param" {
		t.Errorf("unexpected params: %+v", p)
	}
}

func TestParseTuicLink(t *testing.T) {
	link := "tuic://my-uuid:my-password@tuic.example.com:8443/?sni=tuic.example.com&congestion_controller=bbr&allow_insecure=1&alpn=h3#TUIC%20Node%201"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatalf("parseNodeURI error: %v", err)
	}
	if node.Type != "tuic" || node.Name != "TUIC Node 1" || node.Server != "tuic.example.com" || node.Port != 8443 {
		t.Fatalf("unexpected node: %+v", node)
	}
	p := node.Proxy
	if p["uuid"] != "my-uuid" || p["password"] != "my-password" || p["sni"] != "tuic.example.com" || p["skip-cert-verify"] != true {
		t.Errorf("unexpected proxy: %+v", p)
	}
	if p["congestion-controller"] != "bbr" {
		t.Errorf("congestion controller = %v, want bbr", p["congestion-controller"])
	}
}

func TestMergeSubscriptionProfiles(t *testing.T) {
	sub1YAML := []byte(`
proxies:
  - name: "香港 01"
    type: vmess
    server: 1.1.1.1
    port: 443
    uuid: 11111111-1111-1111-1111-111111111111
  - name: "日本 01"
    type: ss
    server: 2.2.2.2
    port: 8388
    cipher: aes-128-gcm
    password: pass
`)

	sub2JSON := []byte(`{
		"outbounds": [
			{
				"type": "vless",
				"tag": "香港 01",
				"server": "3.3.3.3",
				"server_port": 443,
				"uuid": "22222222-2222-2222-2222-222222222222"
			},
			{
				"type": "trojan",
				"tag": "新加坡 01",
				"server": "4.4.4.4",
				"server_port": 443,
				"password": "pass"
			}
		]
	}`)

	mergedYAML, nodes, err := mergeSubscriptionProfiles([][]byte{sub1YAML, sub2JSON})
	if err != nil {
		t.Fatalf("mergeSubscriptionProfiles failed: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("got %d nodes, want 4: %+v", len(nodes), nodes)
	}

	var parsed struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(mergedYAML, &parsed); err != nil {
		t.Fatalf("unmarshal merged YAML failed: %v", err)
	}
	if len(parsed.Proxies) != 4 {
		t.Fatalf("got %d proxies in YAML, want 4", len(parsed.Proxies))
	}

	names := make([]string, 0, 4)
	for _, p := range parsed.Proxies {
		names = append(names, stringValue(p["name"]))
	}

	// Conflict between sub1's "香港 01" and sub2's "香港 01" should result in "香港 01 (2)"
	foundRenamed := false
	for _, name := range names {
		if name == "香港 01 (2)" {
			foundRenamed = true
		}
	}
	if !foundRenamed {
		t.Errorf("expected duplicate name to be disambiguated as '香港 01 (2)', got names: %v", names)
	}
}

func TestEnsureClashYAML(t *testing.T) {
	// Base64 URI list
	uriList := "ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.2.3.4:8388#SS-Node\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(uriList))

	yamlOut, nodes, err := ensureClashYAML([]byte(b64))
	if err != nil {
		t.Fatalf("ensureClashYAML error: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "SS-Node" {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(yamlOut)), "proxies:") {
		t.Fatalf("expected valid Clash YAML starting with proxies:, got:\n%s", string(yamlOut))
	}
}

func TestCategorizeSubscriptionInput(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantRemote int
		wantInline int
	}{
		{
			name:       "pure hysteria2 link",
			input:      "hysteria2://GrxjK7Wwri@hy2.tingle.com:443#Node1",
			wantRemote: 0,
			wantInline: 1,
		},
		{
			name:       "multiple mixed links",
			input:      "https://sub.com/sub|hysteria2://GrxjK7Wwri@hy2.tingle.com:443#Node1|vmess://abc",
			wantRemote: 1,
			wantInline: 2,
		},
		{
			name:       "pure remote multi",
			input:      "https://sub1.com|https://sub2.com",
			wantRemote: 2,
			wantInline: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote, inline := categorizeSubscriptionInput(tc.input)
			if len(remote) != tc.wantRemote || len(inline) != tc.wantInline {
				t.Fatalf("got remote=%d inline=%d, want remote=%d inline=%d",
					len(remote), len(inline), tc.wantRemote, tc.wantInline)
			}
		})
	}
}

func TestValidateSubscriptionAcceptsNodeLinks(t *testing.T) {
	nodeLinks := []string{
		"hysteria2://GrxjK7Wwri@hy2.tingle.com:443#Node1",
		"vmess://eyJaddIjoiMS4yLjMuNCJ9",
		"https://sub.com|hysteria2://pw@hy2.com:443",
	}
	for _, link := range nodeLinks {
		if err := validateSubscriptionURL(link); err != nil {
			t.Errorf("link %q should be accepted by validateSubscriptionURL, got error: %v", link, err)
		}
	}
}
