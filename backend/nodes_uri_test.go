package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func vmessURI(overrides map[string]string) string {
	fields := map[string]string{
		"v": "2", "ps": "香港 01", "add": "hk1.example.com", "port": "443",
		"id": "11111111-2222-3333-4444-555555555555", "aid": "0", "scy": "auto",
		"net": "ws", "type": "none", "host": "hk1.example.com", "path": "/ws",
		"tls": "tls", "sni": "hk1.example.com",
	}
	for key, value := range overrides {
		fields[key] = value
	}
	body := "{"
	first := true
	for key, value := range fields {
		if !first {
			body += ","
		}
		first = false
		body += `"` + key + `":"` + value + `"`
	}
	body += "}"
	return "vmess://" + base64.StdEncoding.EncodeToString([]byte(body))
}

func TestParseVmessLink(t *testing.T) {
	node, err := parseNodeURI(vmessURI(nil))
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != "vmess" || node.Server != "hk1.example.com" || node.Port != 443 {
		t.Fatalf("unexpected node: %+v", node)
	}
	if node.Name != "香港 01" {
		t.Fatalf("name = %q, want the link's own name", node.Name)
	}
	proxy := node.Proxy
	if proxy["uuid"] != "11111111-2222-3333-4444-555555555555" || proxy["cipher"] != "auto" {
		t.Fatalf("credentials: %+v", proxy)
	}
	if proxy["tls"] != true || proxy["servername"] != "hk1.example.com" {
		t.Fatalf("tls settings: %+v", proxy)
	}
	if proxy["network"] != "ws" {
		t.Fatalf("network: %+v", proxy)
	}
	options, ok := proxy["ws-opts"].(map[string]any)
	if !ok || options["path"] != "/ws" {
		t.Fatalf("ws options: %+v", proxy)
	}
	headers, ok := options["headers"].(map[string]any)
	if !ok || headers["Host"] != "hk1.example.com" {
		t.Fatalf("ws host header: %+v", options)
	}
}

func TestParseVmessLinkVariants(t *testing.T) {
	// No TLS and plain TCP: nothing transport related may be written.
	node, err := parseNodeURI(vmessURI(map[string]string{"net": "tcp", "tls": "", "path": "", "host": ""}))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tls", "servername", "network", "ws-opts"} {
		if _, present := node.Proxy[key]; present {
			t.Errorf("a plain vmess node must not carry %q: %+v", key, node.Proxy)
		}
	}

	// gRPC takes its service name from the same field as a ws path.
	node, err = parseNodeURI(vmessURI(map[string]string{"net": "grpc", "path": "gsvc"}))
	if err != nil {
		t.Fatal(err)
	}
	options, ok := node.Proxy["grpc-opts"].(map[string]any)
	if !ok || options["grpc-service-name"] != "gsvc" {
		t.Fatalf("grpc options: %+v", node.Proxy)
	}

	// alpn arrives as a comma separated string here.
	node, err = parseNodeURI(vmessURI(map[string]string{"alpn": "h2,http/1.1"}))
	if err != nil {
		t.Fatal(err)
	}
	alpn, ok := node.Proxy["alpn"].([]string)
	if !ok || len(alpn) != 2 || alpn[0] != "h2" {
		t.Fatalf("alpn: %+v", node.Proxy["alpn"])
	}
}

func TestParseVmessLinkRejections(t *testing.T) {
	cases := map[string]string{
		"no uuid":     vmessURI(map[string]string{"id": ""}),
		"no server":   vmessURI(map[string]string{"add": ""}),
		"no port":     vmessURI(map[string]string{"port": "0"}),
		"bad base64":  "vmess://not base64 at all!!",
		"not json":    "vmess://" + base64.StdEncoding.EncodeToString([]byte("hello")),
		"bad network": vmessURI(map[string]string{"net": "xhttp"}),
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseNodeURI(link); err == nil {
				t.Fatalf("%s must be refused", link)
			}
		})
	}
}

func TestParseVlessLink(t *testing.T) {
	link := "vless://22222222-3333-4444-5555-666666666666@1.2.3.4:8443" +
		"?encryption=none&security=tls&sni=a.example.com&type=ws&host=a.example.com&path=%2Fvless&fp=chrome#VLESS-WS"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != "vless" || node.Server != "1.2.3.4" || node.Port != 8443 || node.Name != "VLESS-WS" {
		t.Fatalf("unexpected node: %+v", node)
	}
	proxy := node.Proxy
	if proxy["uuid"] != "22222222-3333-4444-5555-666666666666" || proxy["tls"] != true {
		t.Fatalf("credentials or tls: %+v", proxy)
	}
	if proxy["servername"] != "a.example.com" || proxy["client-fingerprint"] != "chrome" {
		t.Fatalf("tls extras: %+v", proxy)
	}
	options, ok := proxy["ws-opts"].(map[string]any)
	if !ok || options["path"] != "/vless" {
		t.Fatalf("ws options: %+v", proxy)
	}
}

func TestParseVlessReality(t *testing.T) {
	link := "vless://22222222-3333-4444-5555-666666666666@r.example.com:443" +
		"?security=reality&pbk=PUBLICKEY&sid=ab12&fp=safari&sni=www.example.com&flow=xtls-rprx-vision&type=tcp#REALITY"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatal(err)
	}
	proxy := node.Proxy
	if proxy["flow"] != "xtls-rprx-vision" || proxy["client-fingerprint"] != "safari" {
		t.Fatalf("flow or fingerprint: %+v", proxy)
	}
	reality, ok := proxy["reality-opts"].(map[string]any)
	if !ok || reality["public-key"] != "PUBLICKEY" || reality["short-id"] != "ab12" {
		t.Fatalf("reality options: %+v", proxy)
	}
	// reality without a public key cannot be dialled at all.
	if _, err := parseNodeURI("vless://22222222-3333-4444-5555-666666666666@r.example.com:443?security=reality#R"); err == nil {
		t.Fatal("reality without pbk must be refused")
	}
	// An unknown security mode is refused rather than guessed at.
	if _, err := parseNodeURI("vless://22222222-3333-4444-5555-666666666666@r.example.com:443?security=warp#R"); err == nil {
		t.Fatal("an unknown security mode must be refused")
	}
}

func TestParseTrojanLink(t *testing.T) {
	link := "trojan://pass%40word@tj.example.com:443?sni=tj.example.com&allowInsecure=1&type=grpc&serviceName=gs#TJ"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != "trojan" || node.Port != 443 || node.Name != "TJ" {
		t.Fatalf("unexpected node: %+v", node)
	}
	proxy := node.Proxy
	if proxy["password"] != "pass@word" {
		t.Fatalf("password must be URL-decoded: %+v", proxy["password"])
	}
	if proxy["sni"] != "tj.example.com" || proxy["skip-cert-verify"] != true {
		t.Fatalf("tls settings: %+v", proxy)
	}
	options, ok := proxy["grpc-opts"].(map[string]any)
	if !ok || options["grpc-service-name"] != "gs" {
		t.Fatalf("grpc options: %+v", proxy)
	}

	// A bare trojan link still works: port defaults to 443, no transport opts.
	node, err = parseNodeURI("trojan://secret@tj2.example.com#TJ2")
	if err != nil {
		t.Fatal(err)
	}
	if node.Port != 443 || node.Proxy["network"] != nil {
		t.Fatalf("unexpected node: %+v", node.Proxy)
	}
	if _, err := parseNodeURI("trojan@tj3.example.com:443"); err == nil {
		t.Fatal("a link with no scheme must be refused")
	}
}

func TestParseShadowsocksLink(t *testing.T) {
	sip002 := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secret")) +
		"@ss.example.com:8388#SS-01"
	node, err := parseNodeURI(sip002)
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != "ss" || node.Server != "ss.example.com" || node.Port != 8388 || node.Name != "SS-01" {
		t.Fatalf("unexpected node: %+v", node)
	}
	if node.Proxy["cipher"] != "aes-256-gcm" || node.Proxy["password"] != "secret" {
		t.Fatalf("credentials: %+v", node.Proxy)
	}

	// The older shape wraps method, password and host together.
	legacy := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:pw@ss2.example.com:8389")) + "#Legacy"
	node, err = parseNodeURI(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if node.Server != "ss2.example.com" || node.Port != 8389 || node.Name != "Legacy" {
		t.Fatalf("unexpected node: %+v", node)
	}
	if node.Proxy["cipher"] != "aes-128-gcm" || node.Proxy["password"] != "pw" {
		t.Fatalf("credentials: %+v", node.Proxy)
	}
}

func TestParseShadowsocksRejections(t *testing.T) {
	plugin := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secret")) +
		"@ss.example.com:8388?plugin=obfs-local%3Bobfs%3Dhttp#P"
	if _, err := parseNodeURI(plugin); err == nil || !strings.Contains(err.Error(), "plugin") {
		t.Fatalf("a plugin link must be refused with a reason, got %v", err)
	}
	for _, link := range []string{
		"ss://@@@#X",
		"ss://" + base64.RawStdEncoding.EncodeToString([]byte("nocolon")),
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secret@host-only")),
	} {
		if _, err := parseNodeURI(link); err == nil {
			t.Fatalf("%q must be refused", link)
		}
	}
}

func TestParseHysteria2Link(t *testing.T) {
	link := "hysteria2://mypass@hy.example.com:443?sni=hy.example.com&insecure=1" +
		"&obfs=salamander&obfs-password=obfspw#HY2"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != "hysteria2" || node.Port != 443 || node.Name != "HY2" {
		t.Fatalf("unexpected node: %+v", node)
	}
	proxy := node.Proxy
	if proxy["password"] != "mypass" || proxy["sni"] != "hy.example.com" {
		t.Fatalf("credentials: %+v", proxy)
	}
	if proxy["skip-cert-verify"] != true || proxy["obfs"] != "salamander" || proxy["obfs-password"] != "obfspw" {
		t.Fatalf("obfs settings: %+v", proxy)
	}

	// The short scheme name works too, and the port falls back to 443.
	node, err = parseNodeURI("hy2://pw@hy2.example.com#HY2B")
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != "hysteria2" || node.Port != 443 {
		t.Fatalf("unexpected node: %+v", node)
	}
	if node.Proxy["skip-cert-verify"] != nil {
		t.Fatalf("verification must not be skipped unless the link says so: %+v", node.Proxy)
	}

	if _, err := parseNodeURI("hysteria2://@hy.example.com:443#N"); err == nil {
		t.Fatal("a link without a password must be refused")
	}
}

func TestParseNodesURISSR(t *testing.T) {
	raw := "1.2.3.4:8388:origin:aes-256-cfb:plain:cGFzc3dvcmQ/?remarks=U1NSLU5vZGU&obfsparam=&protoparam="
	link := "ssr://" + base64.RawURLEncoding.EncodeToString([]byte(raw))
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatalf("parse SSR failed: %v", err)
	}
	if node.Type != "ssr" || node.Name != "SSR-Node" || node.Server != "1.2.3.4" || node.Port != 8388 {
		t.Fatalf("unexpected SSR node: %+v", node)
	}
	if node.Proxy["cipher"] != "aes-256-cfb" || node.Proxy["password"] != "password" {
		t.Fatalf("unexpected SSR proxy: %+v", node.Proxy)
	}
}

func TestParseNodesURITUIC(t *testing.T) {
	link := "tuic://my-uuid:my-pass@tuic.example.com:8443/?sni=tuic.example.com&congestion_controller=bbr#TUIC-Node"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatalf("parse TUIC failed: %v", err)
	}
	if node.Type != "tuic" || node.Name != "TUIC-Node" || node.Server != "tuic.example.com" || node.Port != 8443 {
		t.Fatalf("unexpected TUIC node: %+v", node)
	}
	if node.Proxy["uuid"] != "my-uuid" || node.Proxy["password"] != "my-pass" || node.Proxy["congestion-controller"] != "bbr" {
		t.Fatalf("unexpected TUIC proxy: %+v", node.Proxy)
	}
}

func TestParseNodesURIHysteria(t *testing.T) {
	link := "hysteria://hy.example.com:443?auth=secret&upmbps=50&downmbps=100&peer=hy.example.com#Hy1-Node"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatalf("parse Hysteria failed: %v", err)
	}
	if node.Type != "hysteria" || node.Name != "Hy1-Node" || node.Server != "hy.example.com" || node.Port != 443 {
		t.Fatalf("unexpected Hysteria node: %+v", node)
	}
	if node.Proxy["auth_str"] != "secret" || node.Proxy["up"] != 50 || node.Proxy["down"] != 100 {
		t.Fatalf("unexpected Hysteria proxy: %+v", node.Proxy)
	}
}

func TestParseNodeURIRejections(t *testing.T) {
	for _, link := range []string{
		"",
		"   ",
		"example.com:443",
		"http://example.com/sub",
		"socks5://1.2.3.4:1080",
		"vmess1://abc",
	} {
		if _, err := parseNodeURI(link); err == nil {
			t.Fatalf("%q must be refused", link)
		}
	}
}
