//go:build windows && amd64 && rustcore

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRustLegacyNodeFieldsParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_LEGACY_NODE_RUST") == "1"
	if rustLegacyNodeFieldsAvailable() != expected {
		t.Fatalf("legacy availability=%v expected=%v", rustLegacyNodeFieldsAvailable(), expected)
	}
	count := 0
	check := func(kind, text string) {
		t.Helper()
		var a, w manualNode
		var ae, we error
		switch kind {
		case "vmess":
			a, ae = parseVmessLink(text)
			w, we = parseVmessLinkGo(text)
		case "ss":
			u, e := url.Parse(text)
			if e != nil {
				return
			}
			a, ae = parseShadowsocksLink(text, u)
			w, we = parseShadowsocksLinkGo(text, u)
		case "ssr":
			a, ae = parseSSRLink(text)
			w, we = parseSSRLinkGo(text)
		}
		if (ae == nil) != (we == nil) {
			t.Fatalf("%s case %d success mismatch %v / %v", kind, count, ae, we)
		}
		if ae != nil {
			if ae.Error() != we.Error() {
				t.Fatalf("%s case %d error mismatch: %v / %v", kind, count, ae, we)
			}
		} else if !reflect.DeepEqual(a, w) {
			t.Fatalf("%s case %d output mismatch: %#v / %#v", kind, count, a, w)
		}
		count++
	}
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	ports := []any{443, 1, -1, 65536, 443.9, 0, "443", "+443", "-1", "65536", "oops", nil, true, 1e20}
	aids := []any{0, -1, 64, 1.5, "+64", "bad", nil, true, 1e20}
	tls := []string{"", "tls", "none", " false ", "0", " TRUE ", "𐐀", "\u0130"}
	nets := []string{"", "tcp", "ws", "websocket", "grpc", "h2", "http", " Bad\n", "unknown"}
	alpn := []any{nil, "h2, h3, h2", []any{"h2,h3", " h2 ", "", 7}, true, []any{}}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1200; i++ {
		link := map[string]any{"ps": []string{"name", " 名称 ", "", "\u3000"}[i%4], "add": "node.example.invalid", "port": ports[rng.Intn(len(ports))], "id": []string{"uuid", " uuid ", "", "\u00a0"}[i%4], "aid": aids[rng.Intn(len(aids))], "scy": []string{"", "AUTO", " None ", " aes-128-gcm "}[i%4], "tls": tls[rng.Intn(len(tls))], "net": nets[rng.Intn(len(nets))], "path": []string{"", " /path ", "svc\nvalue"}[i%3], "host": " host.invalid ", "sni": []string{"", " sni.invalid "}[i%2], "alpn": alpn[rng.Intn(len(alpn))], "fp": []string{"", " chrome "}[i%2]}
		b, e := json.Marshal(link)
		if e != nil {
			t.Fatal(e)
		}
		check("vmess", "vmess://"+encodings[i%4].EncodeToString(b))
	}
	// Existing numeric port behavior intentionally includes nonzero out-of-range values.
	for _, port := range []any{-1, 65536, 1e20, 443.9} {
		b, _ := json.Marshal(map[string]any{"add": "x.invalid", "port": port, "id": "id", "alpn": []any{"h2,h3", "h2"}})
		check("vmess", "vmess://"+base64.StdEncoding.EncodeToString(b))
	}
	for _, text := range []string{"vmess://", "vmess://!!!", "vmess://e30=", "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"id":1}`))} {
		check("vmess", text)
	}
	ciphers := []string{"aes-128-gcm", " none ", "", "\u3000"}
	passwords := []string{"pass", " pass ", "a:b:c", "", "\u3000", string([]byte{0xff, 0xfe})}
	endpoints := []string{"host.invalid:443", "host.invalid:0", "host.invalid:65536", "host.invalid:+443", "host.invalid:abc", "[::1]:443"}
	for _, cipher := range ciphers {
		for _, password := range passwords {
			for _, endpoint := range endpoints {
				credentials := cipher + ":" + password
				for _, encoding := range encodings {
					check("ss", "ss://"+encoding.EncodeToString([]byte(credentials))+"@"+endpoint+"#name")
					check("ss", "ss://"+encoding.EncodeToString([]byte(credentials+"@"+endpoint))+"#name")
				}
			}
		}
	}
	for _, text := range []string{"ss://aes-128-gcm:password@host.invalid:443#name", "ss://!!!", "ss://cA==@host.invalid:443?plugin=obfs", "ss://cA==@host.invalid:443?plugin="} {
		check("ss", text)
	}
	for i := 0; i < 1200; i++ {
		pass := passwords[i%len(passwords)]
		pass64 := encodings[i%4].EncodeToString([]byte(pass))
		if i%9 == 0 {
			pass64 = "!!!"
		}
		proto := []string{"origin", " auth_sha1_v4 ", ""}[i%3]
		obfs := []string{"plain", " http_simple ", ""}[i%3]
		method := ciphers[i%len(ciphers)]
		endpoint := []string{"host.invalid:443", "[2001:db8::1]:443", "host.invalid:+443", "host.invalid:0"}[i%4]
		name := []string{"name", " 名称 ", " ", "", string([]byte{0xff})}[i%5]
		remarks := base64.RawURLEncoding.EncodeToString([]byte(name))
		param := base64.RawURLEncoding.EncodeToString([]byte(" param "))
		raw := fmt.Sprintf("%s:%s:%s:%s:%s/?remarks=%s&obfsparam=%s&protoparam=%s", endpoint, proto, method, obfs, pass64, remarks, param, param)
		if i%7 == 0 {
			raw = strings.Replace(raw, "/?", "?", 1)
		}
		check("ssr", "ssr://"+encodings[i%4].EncodeToString([]byte(raw)))
	}
	for _, text := range []string{"ssr://", "ssr://!!!", "ssr://YQ=="} {
		check("ssr", text)
	}
	t.Logf("Compared %d synthetic VMess/SS/SSR cases", count)
}

func TestRustLegacyFieldsBoundary(t *testing.T) {
	if !rustLegacyNodeFieldsAvailable() {
		t.Skip("v3 DLL not enabled")
	}
	// Assert direct FFI participation, not merely Go fallback through wrappers.
	ssr, e, ok := rustLegacyNodeFields("test", "ssr", "x.invalid", 443, "n", "password", map[string]string{"__smartvpn_cipher": "aes-128-cfb", "__smartvpn_protocol": "origin", "__smartvpn_obfs": "plain"})
	if !ok || e != nil || ssr.Proxy["protocol"] != "origin" || ssr.Proxy["password"] != "password" {
		t.Fatalf("direct SSR v3 bridge unavailable: %v %v", ok, e)
	}
	_, e, ok = rustLegacyNodeFields("test", "vmess", "x.invalid", -1, "n", "id", map[string]string{"__smartvpn_alter_id": "0", "__smartvpn_alpn_count": "0"})
	if !ok || e != nil {
		t.Fatalf("direct v3 bridge unavailable: %v %v", ok, e)
	}
	_, _, ok = rustLegacyNodeFields("test", "ss", "x.invalid", 443, "n", string([]byte{0xff}), map[string]string{"__smartvpn_cipher": "none"})
	if ok {
		t.Fatal("invalid UTF-8 must request Go fallback")
	}
	_, e, ok = rustLegacyNodeFields("test", "ss", "x.invalid", 443, "n", strings.Repeat("p", 10000), map[string]string{"__smartvpn_cipher": "none"})
	if !ok || e != nil {
		t.Fatalf("large secret bridge failed: %v %v", ok, e)
	}
}
