//go:build windows && amd64 && rustcore

package main

import (
	"math/rand"
	"net/url"
	"os"
	"reflect"
	"testing"
)

func TestRustExtendedNodeParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_EXTENDED_NODE_RUST") == "1"
	if rustExtendedNodeFieldsAvailable() != expected {
		t.Fatalf("extended bridge availability=%v expected=%v", rustExtendedNodeFieldsAvailable(), expected)
	}
	count := 0
	check := func(kind string, u *url.URL) {
		t.Helper()
		var actual, want manualNode
		var ae, we error
		if kind == "tuic" {
			actual, ae = parseTuicLink(u)
			want, we = parseTuicLinkGo(u)
		} else {
			actual, ae = parseHysteriaLink(u)
			want, we = parseHysteriaLinkGo(u)
		}
		if expected {
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
				t.Fatalf("case %d error text differs", count)
			}
		} else if !reflect.DeepEqual(actual, want) {
			t.Fatalf("case %d typed node differs: %#v / %#v", count, actual, want)
		}
		count++
	}
	users := []*url.Userinfo{nil, url.User(""), url.User("uuid"), url.User(" "), url.UserPassword("uuid", "password"), url.UserPassword("", "password"), url.UserPassword(" ", "password"), url.UserPassword("uuid", ""), url.UserPassword("", ""), url.UserPassword("密钥", "密碼\u3000")}
	hosts := []string{"node.example.invalid", "node.example.invalid:8443", "[2001:db8::1]:443", "", "node.example.invalid:0", "node.example.invalid:65536"}
	speeds := []string{"", "1", "+12", "65535", "65536", "-1", "0", "1_000", "12.5", "１２", "0x10", " 42 ", "99999999999999999999999"}
	for _, kind := range []string{"tuic", "hysteria"} {
		for _, user := range users {
			for _, host := range hosts {
				for _, speed := range speeds {
					q := url.Values{"upmbps": {speed}, "downmbps": {speed}, "up": {"30"}, "down": {"60"}, "auth": {"queryauth"}, "alpn": {"h3, ,h2"}, "sni": {" sni.invalid "}, "peer": {" peer.invalid "}, "server_name": {" fallback.invalid "}, "udp_relay_mode": {" native "}, "obfs": {"  obfs  "}, "__smartvpn_tuic_username": {"malicious-query"}, "__smartvpn_tuic_password": {"malicious-query"}}
					check(kind, &url.URL{Scheme: kind, Host: host, User: user, RawQuery: q.Encode(), Fragment: "名字"})
				}
			}
		}
	}
	rng := rand.New(rand.NewSource(47))
	values := []string{"", " ", "\u0085", "\u3000yes\u3000", "true", "TRUE", "yes", "YES", "1", "0", "false", "+64", "65535", "65536", "-10", "1_000", "0x20", "bbr", "cubic", "native", "quic", "h3,,h2, ", "quote\"slash\\", "123456789012345678901"}
	keys := []string{"allow_insecure", "insecure", "allowInsecure", "congestion_controller", "congestion_control", "cc", "server_name", "sni", "peer", "alpn", "obfs", "udp_relay_mode", "auth", "upmbps", "up", "downmbps", "down"}
	for i := 0; i < 4000; i++ {
		kind := []string{"tuic", "hysteria"}[i%2]
		q := url.Values{}
		for _, key := range keys {
			q.Set(key, values[rng.Intn(len(values))])
		}
		check(kind, &url.URL{Scheme: kind, Host: "node.example.invalid:443", User: users[rng.Intn(len(users))], RawQuery: q.Encode(), Fragment: values[rng.Intn(len(values))]})
	}
	t.Logf("Compared %d synthetic TUIC/Hysteria-v1 field cases", count)
}
func TestRustExtendedInvalidUTF8Fallback(t *testing.T) {
	if !rustExtendedNodeFieldsAvailable() {
		t.Skip("extended DLL not selected")
	}
	for _, kind := range []string{"tuic", "hysteria"} {
		u := &url.URL{Scheme: kind, Host: "node.example.invalid:443", User: url.User(string([]byte{255}))}
		if _, _, used := rustNodeFields(u, kind); used {
			t.Fatal("invalid decoded UTF8 must fall back")
		}
		var a, b manualNode
		var ae, be error
		if kind == "tuic" {
			a, ae = parseTuicLink(u)
			b, be = parseTuicLinkGo(u)
		} else {
			a, ae = parseHysteriaLink(u)
			b, be = parseHysteriaLinkGo(u)
		}
		if !reflect.DeepEqual(a, b) || (ae == nil) != (be == nil) {
			t.Fatal("invalid-byte fallback differs")
		}
	}
}
