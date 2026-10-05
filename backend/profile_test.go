package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeSubscriptionBase64(t *testing.T) {
	payload := []byte("vless://uuid@1.2.3.4:443#node")
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"standard", base64.StdEncoding.EncodeToString(payload), string(payload), true},
		{"raw standard", base64.RawStdEncoding.EncodeToString(payload), string(payload), true},
		{"url safe", base64.URLEncoding.EncodeToString(payload), string(payload), true},
		{"raw url safe", base64.RawURLEncoding.EncodeToString(payload), string(payload), true},
		{"whitespace stripped", "  " + base64.StdEncoding.EncodeToString(payload) + "\n", string(payload), true},
		{"invalid", "!!!not base64!!!", "", false},
		{"empty", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeSubscriptionBase64(tc.input)
			if tc.ok && err != nil {
				t.Fatalf("expected success, got error %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseProfileNodesFromYAML(t *testing.T) {
	body := []byte(`
proxies:
  - name: "香港 01"
    type: vmess
    network: ws
    tls: true
    ws-opts:
      path: /socket
      headers:
        Host: hk.example.com
  - name: "日本 02"
    type: trojan
    network: tcp
    tls: "none"
  - name: "剩余流量：100GB"
    type: vmess
    network: ws
  - name: ""
    type: vmess
  - name: "duplicate"
    type: ss
  - name: "duplicate"
    type: ss
`)
	nodes := parseProfileNodes(body)
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3: %+v", len(nodes), nodes)
	}
	first := nodes[0]
	if first.Name != "香港 01" || first.Type != "vmess" || first.Network != "ws" {
		t.Fatalf("unexpected first node: %+v", first)
	}
	if !first.TLS || !first.WsHost || !first.WsPath {
		t.Fatalf("expected TLS, ws host and ws path on first node: %+v", first)
	}
	second := nodes[1]
	if second.TLS {
		t.Fatalf("tls: none must not report TLS: %+v", second)
	}
	if second.WsHost || second.WsPath {
		t.Fatalf("expected no websocket details on second node: %+v", second)
	}
	for _, node := range nodes {
		if isInformationalNode(node.Name) {
			t.Fatalf("informational entry %q must be filtered out", node.Name)
		}
	}
}

func TestParseProfileNodesAnnouncementOnlyYAML(t *testing.T) {
	body := []byte(`
proxies:
  - name: "剩余流量：100GB"
    type: vmess
  - name: "到期时间：2027-01-01"
    type: vmess
`)
	if nodes := parseProfileNodes(body); len(nodes) != 0 {
		t.Fatalf("announcement-only subscription must yield no nodes, got %+v", nodes)
	}
}

func TestParseProfileNodesFromURIs(t *testing.T) {
	vmessPayload, err := json.Marshal(map[string]any{
		"ps": "节点 A", "add": "1.2.3.4", "port": 443,
		"net": "ws", "tls": "tls", "host": "a.example.com", "path": "/ws",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.Join([]string{
		"vmess://" + base64.StdEncoding.EncodeToString(vmessPayload),
		"vless://uuid@1.2.3.4:443?type=ws&security=tls&host=b.example.com&path=%2Fws#节点 B",
		"trojan://secret@1.2.3.4:443?security=none#节点 C",
		"ss://YWVzLTI1Ni1nY206cGFzcw==@1.2.3.4:8388#节点 D",
		"not-a-node",
		"",
	}, "\n"))
	nodes := parseProfileNodes(body)
	if len(nodes) != 4 {
		t.Fatalf("got %d nodes, want 4: %+v", len(nodes), nodes)
	}
	byName := map[string]proxyNode{}
	for _, node := range nodes {
		byName[node.Name] = node
	}
	vmess, ok := byName["节点 A"]
	if !ok {
		t.Fatalf("vmess node missing: %+v", nodes)
	}
	if vmess.Type != "Vmess" || vmess.Network != "ws" || !vmess.TLS || !vmess.WsHost || !vmess.WsPath {
		t.Fatalf("unexpected vmess node: %+v", vmess)
	}
	vless, ok := byName["节点 B"]
	if !ok {
		t.Fatalf("vless node missing: %+v", nodes)
	}
	if vless.Type != "vless" || vless.Network != "ws" || !vless.TLS || !vless.WsHost || !vless.WsPath {
		t.Fatalf("unexpected vless node: %+v", vless)
	}
	trojan, ok := byName["节点 C"]
	if !ok {
		t.Fatalf("trojan node missing: %+v", nodes)
	}
	if trojan.Type != "trojan" || trojan.TLS {
		t.Fatalf("unexpected trojan node: %+v", trojan)
	}
	if _, ok := byName["节点 D"]; !ok {
		t.Fatalf("ss node missing: %+v", nodes)
	}
}

func TestParseProfileNodesFromBase64URIList(t *testing.T) {
	list := "vless://uuid@1.2.3.4:443?security=tls#节点 A\ntrojan://secret@5.6.7.8:443#节点 B\n"
	body := []byte(base64.StdEncoding.EncodeToString([]byte(list)))
	nodes := parseProfileNodes(body)
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(nodes), nodes)
	}
}

func TestParseProfileNodesFromBase64ClashYAML(t *testing.T) {
	yamlBody := "proxies:\n  - name: \"香港 01\"\n    type: vmess\n    network: ws\n    tls: true\n"
	body := []byte(base64.StdEncoding.EncodeToString([]byte(yamlBody)))
	nodes := parseProfileNodes(body)
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "香港 01" || nodes[0].Type != "vmess" || !nodes[0].TLS || nodes[0].Network != "ws" {
		t.Fatalf("unexpected node: %+v", nodes[0])
	}
}

func TestParseProfileNodesEmptyAndGarbage(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"whitespace", "   \n\t "},
		{"garbage", "this is not a subscription"},
		{"broken base64", "???"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if nodes := parseProfileNodes([]byte(tc.body)); len(nodes) != 0 {
				t.Fatalf("expected no nodes, got %+v", nodes)
			}
		})
	}
}

func TestParseProfileNodesDropsNonStringWebsocketHost(t *testing.T) {
	body := []byte(`
proxies:
  - name: "numeric host"
    type: vmess
    ws-opts:
      path: /ws
      headers:
        Host: 12345
`)
	nodes := parseProfileNodes(body)
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if nodes[0].WsHost {
		t.Fatalf("a non-string Host header must not count as a configured host: %+v", nodes[0])
	}
	if !nodes[0].WsPath {
		t.Fatalf("expected the ws path to be detected: %+v", nodes[0])
	}
}

func TestTLSLEnabled(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string tls", "tls", true},
		{"string none", "none", false},
		{"string false", "false", false},
		{"string zero", "0", false},
		{"empty string", "", false},
		{"number", 1, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tlsEnabled(tc.value); got != tc.want {
				t.Fatalf("tlsEnabled(%#v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestStringValue(t *testing.T) {
	if got := stringValue("text"); got != "text" {
		t.Fatalf("got %q, want %q", got, "text")
	}
	for _, value := range []any{1, true, nil, 1.5} {
		if got := stringValue(value); got != "" {
			t.Fatalf("stringValue(%#v) = %q, want empty", value, got)
		}
	}
}

func TestUniqueNodesKeepsFirstOccurrence(t *testing.T) {
	nodes := []proxyNode{
		{Name: "a", Type: "vmess"},
		{Name: "b", Type: "trojan"},
		{Name: "a", Type: "ss"},
	}
	got := uniqueNodes(nodes)
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(got), got)
	}
	if got[0].Type != "vmess" || got[1].Name != "b" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestSubscriptionRules(t *testing.T) {
	body := []byte(`
rules:
  - DOMAIN-SUFFIX,google.com,Proxy
  - DOMAIN,example.com,DIRECT
  - IP-CIDR,1.2.3.0/24,REJECT,no-resolve
  - IP-CIDR6,2001:db8::/32,Proxy
  - DOMAIN-KEYWORD,ads,AdGroup
  - "DOMAIN,evil\ninjected,Proxy"
  - MATCH,Proxy
  - GEOIP,CN,DIRECT
  - PROCESS-NAME,foo,Proxy
  - RULE-SET,reject,REJECT
  - DOMAIN
  - DIRECT,useless
  - IP-CIDR,,Proxy
  - DOMAIN-SUFFIX,lower.example,direct
  - DOMAIN-SUFFIX,lower2.example,reject
`)
	rules := subscriptionRules(body)
	want := []string{
		"DOMAIN-SUFFIX,google.com,SmartVPN",
		"DOMAIN,example.com,DIRECT",
		"IP-CIDR,1.2.3.0/24,REJECT,no-resolve",
		"IP-CIDR6,2001:db8::/32,SmartVPN",
		"DOMAIN-KEYWORD,ads,SmartVPN",
		"DOMAIN-SUFFIX,lower.example,DIRECT",
		"DOMAIN-SUFFIX,lower2.example,REJECT",
	}
	if len(rules) != len(want) {
		t.Fatalf("got %d rules, want %d:\n%s", len(rules), len(want), strings.Join(rules, "\n"))
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d = %q, want %q", i, rules[i], want[i])
		}
	}
}

// A subscription's own groups cannot be reproduced here, but their names say
// what they are for, and reading them is what keeps a domestic service off the
// proxy and an advertisement out of the tunnel.
func TestMapGroupTarget(t *testing.T) {
	cases := map[string]string{
		"DIRECT":       "DIRECT",
		"direct":       "DIRECT",
		"REJECT":       "REJECT",
		"reject":       "REJECT",
		"REJECT-DROP":  "REJECT",
		"Proxy":        "SmartVPN",
		"🚀 节点选择":      "SmartVPN",
		"♻️ 自动选择":      "SmartVPN",
		"🌍 国外媒体":      "SmartVPN",
		"🎬Netflix":    "SmartVPN",
		"✈️Telegram":   "SmartVPN",
		"🎬哔哩哔哩":       "DIRECT",
		"🎬抖音":         "DIRECT",
		"🍎苹果服务":       "DIRECT",
		"🎯 全球直连":      "DIRECT",
		"🇨🇳 国内":       "DIRECT",
		"🛑 广告拦截":      "REJECT",
		"AdBlock":      "REJECT",
		"BLOCK":        "REJECT",
		"":             "SmartVPN",
		"   ":          "SmartVPN",
	}
	for name, want := range cases {
		if got := mapGroupTarget(name); got != want {
			t.Errorf("mapGroupTarget(%q) = %q, want %q", name, got, want)
		}
	}
	// PASS decides nothing, which is what dropping the rule does; a caller that
	// keeps an empty target would emit a rule with no outbound at all.
	if got := mapGroupTarget("PASS"); got != "" {
		t.Fatalf("PASS must be dropped, got %q", got)
	}
}

func TestSubscriptionRulesMapsGroupsByTheirNames(t *testing.T) {
	body := []byte(`
rules:
  - DOMAIN-SUFFIX,bilibili.com,🎬哔哩哔哩
  - DOMAIN-SUFFIX,apple.com,🍎苹果服务
  - DOMAIN-SUFFIX,doubleclick.net,🛑 广告拦截
  - DOMAIN-SUFFIX,netflix.com,🎬Netflix
  - DOMAIN-SUFFIX,cn.example,🇨🇳 国内
  - DOMAIN-SUFFIX,dropped.example,PASS
`)
	rules := subscriptionRules(body)
	want := []string{
		"DOMAIN-SUFFIX,bilibili.com,DIRECT",
		"DOMAIN-SUFFIX,apple.com,DIRECT",
		"DOMAIN-SUFFIX,doubleclick.net,REJECT",
		"DOMAIN-SUFFIX,netflix.com,SmartVPN",
		"DOMAIN-SUFFIX,cn.example,DIRECT",
	}
	if len(rules) != len(want) {
		t.Fatalf("got %d rules, want %d:\n%s", len(rules), len(want), strings.Join(rules, "\n"))
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d = %q, want %q", i, rules[i], want[i])
		}
	}
}

func TestSplitRulesByTarget(t *testing.T) {
	rules := []string{
		"DOMAIN-SUFFIX,a.example,SmartVPN",
		"DOMAIN-SUFFIX,local,DIRECT",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,ads.example,REJECT",
		"DOMAIN-SUFFIX,b.example,SmartVPN",
	}
	decided, proxied := splitRulesByTarget(rules)
	wantDecided := []string{
		"DOMAIN-SUFFIX,local,DIRECT",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,ads.example,REJECT",
	}
	wantProxied := []string{
		"DOMAIN-SUFFIX,a.example,SmartVPN",
		"DOMAIN-SUFFIX,b.example,SmartVPN",
	}
	if strings.Join(decided, "|") != strings.Join(wantDecided, "|") {
		t.Errorf("decided = %v, want %v", decided, wantDecided)
	}
	if strings.Join(proxied, "|") != strings.Join(wantProxied, "|") {
		t.Errorf("proxied = %v, want %v", proxied, wantProxied)
	}
	// Anything whose target cannot be read belongs to the proxy: the safe
	// direction for a rule this code does not understand.
	decided, proxied = splitRulesByTarget([]string{"DOMAIN,a.example", "DOMAIN,b.example,SmartVPN"})
	if len(decided) != 0 || len(proxied) != 2 {
		t.Fatalf("an unreadable target must stay on the proxy side: %v %v", decided, proxied)
	}
}

func TestSubscriptionRulesCapsAtThreeThousand(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("rules:\n")
	for i := 0; i < 3500; i++ {
		builder.WriteString("  - DOMAIN-SUFFIX,d")
		builder.WriteString(strings.Repeat("a", 1+i%3))
		builder.WriteString(".example,Proxy\n")
	}
	rules := subscriptionRules([]byte(builder.String()))
	if len(rules) != 3000 {
		t.Fatalf("got %d rules, want 3000", len(rules))
	}
}

func TestSubscriptionRulesIgnoresMissingSection(t *testing.T) {
	if rules := subscriptionRules([]byte("proxies:\n  - name: a\n")); len(rules) != 0 {
		t.Fatalf("expected no rules, got %+v", rules)
	}
	if rules := subscriptionRules([]byte("")); len(rules) != 0 {
		t.Fatalf("expected no rules, got %+v", rules)
	}
}

func TestClashFormatURL(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantHas []string
		wantNot []string
	}{
		{
			name: "adds cla and drops sub", input: "https://example.com/sub?sub=1&token=abc",
			wantOK: true, wantHas: []string{"cla=1", "token=abc"}, wantNot: []string{"sub=1"},
		},
		{
			name: "already clash format", input: "https://example.com/sub?sub=1&cla=1", wantOK: false,
		},
		{
			name: "no sub parameter", input: "https://example.com/sub?token=abc", wantOK: false,
		},
		{
			name: "invalid url", input: "://bad", wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := clashFormatURL(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("clashFormatURL(%q) ok = %v, want %v", tc.input, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("%q must contain %q", got, want)
				}
			}
			for _, not := range tc.wantNot {
				if strings.Contains(got, not) {
					t.Errorf("%q must not contain %q", got, not)
				}
			}
		})
	}
}

func TestIsInformationalNode(t *testing.T) {
	informational := []string{
		"剩余流量：100GB", "过期时间：2027-01-01", "网站客服", "联系网站客服",
		"永久网址：example.com", "若您未看见节点请更新订阅", "客户端版本低",
		"获取最新客户端", "请到官网查看",
		"到期时间：2027-01-01", "距离下次重置剩余：3 天", "重置剩余流量",
		"点击订阅", "点击更新", "官方网址：example.com", "如需续费请联系客服",
	}
	for _, name := range informational {
		if !isInformationalNode(name) {
			t.Errorf("%q should be informational", name)
		}
	}
	for _, name := range []string{
		"香港 01", "日本 V3", "US-Premium", "",
		"韩国V1|直连|x0.8", "日本 官网专线", "新加坡 02 | 优化 x1",
	} {
		if isInformationalNode(name) {
			t.Errorf("%q should be selectable", name)
		}
	}
}

func TestHasProxyURI(t *testing.T) {
	for _, text := range []string{"vmess://x", "vless://x", "ss://x", "trojan://x", "ssr://x", "hysteria2://x", "tuic://x", "header\nvmess://x"} {
		if !hasProxyURI(text) {
			t.Errorf("%q should be detected as a proxy URI list", text)
		}
	}
	for _, text := range []string{"", "proxies:\n  - name: a", "https://vmess://x", "just random text"} {
		if hasProxyURI(text) {
			t.Errorf("%q should not be detected as a proxy URI list", text)
		}
	}
}
