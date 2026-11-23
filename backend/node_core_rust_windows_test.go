//go:build windows && amd64 && rustcore

package main

import (
	"math/rand"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRustNodeFieldsParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_NODE_RUST") == "1"
	if rustNodeFieldsAvailable() != expected {
		t.Fatalf("node bridge availability=%v expected=%v", rustNodeFieldsAvailable(), expected)
	}
	schemes := []string{"vless", "trojan", "hysteria2"}
	count := 0
	check := func(kind string, u *url.URL) {
		t.Helper()
		var actual, want manualNode
		var ae, we error
		switch kind {
		case "vless":
			actual, ae = parseVlessLink(u)
			want, we = parseVlessLinkGo(u)
		case "trojan":
			actual, ae = parseTrojanLink(u)
			want, we = parseTrojanLinkGo(u)
		case "hysteria2":
			actual, ae = parseHysteria2Link(u)
			want, we = parseHysteria2LinkGo(u)
		}
		if expected && utf8.ValidString(u.String()) {
			_, _, used := rustNodeFields(u, kind)
			if !used {
				t.Fatalf("case %d unexpectedly fell back", count)
			}
		}
		if (ae == nil) != (we == nil) {
			t.Fatalf("case %d errors differ: %v / %v", count, ae, we)
		}
		if ae != nil {
			if ae.Error() != we.Error() {
				t.Fatalf("case %d error text differs: %v / %v", count, ae, we)
			}
		} else if !reflect.DeepEqual(actual, want) {
			t.Fatalf("case %d object differs: %#v / %#v", count, actual, want)
		}
		count++
	}
	for _, kind := range schemes {
		for _, host := range []string{"node.example.invalid", "node.example.invalid:8443", "[2001:db8::1]:443", "", "node.example.invalid:0", "node.example.invalid:65536"} {
			for _, secret := range []string{"password", "", " ", "pass@word", "密钥\u00a0", "quote\"slash\\"} {
				for _, network := range []string{"", "tcp", "ws", "websocket", "grpc", "h2", "http", "xhttp", " QuIc ", "\x00unknown"} {
					q := url.Values{"type": {network}, "sni": {" example.invalid "}, "security": {"tls"}, "path": {"/路径?x=1"}, "host": {"header.invalid"}, "serviceName": {"svc"}, "alpn": {"h2,,http/1.1, "}}
					u := &url.URL{Scheme: kind, Host: host, User: url.User(secret), RawQuery: q.Encode(), Fragment: "名\"称"}
					check(kind, u)
				}
			}
		}
	}
	rng := rand.New(rand.NewSource(43))
	vals := []string{"", " ", "\u0085", "\u3000trim\u3000", "true", "TRUE", "1", "yes", "false", "0", "none", "tls", "reality", "warp", "tcp", "ws", "grpc", "h2", "http", "xhttp", "a,b,, c", "PUBLICKEY", "\"\\\n\t", "hysteria", "İ", "", "example.invalid"}
	keys := []string{"security", "sni", "fp", "allowInsecure", "insecure", "pbk", "sid", "flow", "type", "path", "host", "serviceName", "peer", "alpn", "obfs", "obfs-password"}
	for i := 0; i < 2500; i++ {
		kind := schemes[i%3]
		q := url.Values{}
		for _, key := range keys {
			q.Set(key, vals[rng.Intn(len(vals))])
		}
		u := &url.URL{Scheme: kind, Host: "node.example.invalid:443", User: url.UserPassword("user", vals[rng.Intn(len(vals))]), RawQuery: q.Encode(), Fragment: vals[rng.Intn(len(vals))]}
		check(kind, u)
	}
	for _, kind := range schemes {
		for _, raw := range []string{kind + "://password@node.example.invalid:443?type=ws&type=grpc#name", kind + "://password@[2001:db8::1]?security=reality&pbk=k&sid=ab", kind + "://user:password@node.example.invalid?alpn=h2%2Chttp%2F1.1", kind + "://password@node.example.invalid?x=a;b&type=tcp"} {
			u, e := url.Parse(raw)
			if e != nil {
				t.Fatal(e)
			}
			check(kind, u)
		}
	}
	// Large but entirely synthetic payload exercises ABI output/capacity logic.
	u := &url.URL{Scheme: "trojan", Host: "node.example.invalid:443", User: url.User("p"), RawQuery: url.Values{"type": {"ws"}, "path": {strings.Repeat("中", 10000)}}.Encode()}
	check("trojan", u)
	t.Logf("Compared %d synthetic VLESS/Trojan/Hysteria2 field cases", count)
}
func TestRustNodeWireRejectsMalformed(t *testing.T) {
	for _, b := range [][]byte{nil, {1}, {5}, {5, 255, 255, 255, 255, 255, 255, 255, 255}, {1, 255, 255, 255, 255, 255, 255, 255, 255}} {
		if _, ok := decodeRustNodeValue(b); ok {
			t.Fatal("malformed accepted")
		}
	}
	rng := rand.New(rand.NewSource(44))
	for n := 0; n < 300; n++ {
		b := make([]byte, n)
		rng.Read(b)
		decodeRustNodeValue(b)
	}
}
