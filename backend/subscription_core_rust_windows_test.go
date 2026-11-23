//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRustSubscriptionParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_SUBSCRIPTION_RUST") == "1"
	if rustSubscriptionAvailable() != expected {
		t.Fatalf("subscription Rust availability=%v expected=%v", rustSubscriptionAvailable(), expected)
	}
	corpus := []string{"", " ", "proxies:\n - name: HK", "{not json", "https://example.invalid/sub|vmess://abc", "HTTPS://example.invalid/a", "\r\n VMESS://abc \r anytls://host", "剩余流量 10G", "正常香港", "Zm8=", "Zh==", "Zg", "Zg===", "++//", "--__", "+-/_", "a\nb\rc", "proxies:\r - a", "hello\nss://x", "hello\n SS://x", "anytls://x", string([]byte{0xff, 0xfe, 's', 's', ':', '/', '/'})}
	for _, space := range []string{"\u0085", "\u00a0", "\u1680", "\u2000", "\u2001", "\u2002", "\u2003", "\u2004", "\u2005", "\u2006", "\u2007", "\u2008", "\u2009", "\u200a", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000", "\ufeff", "\u001c", "\u001f"} {
		corpus = append(corpus, space+"Zg=="+space, space+"SS://node"+space, "Z"+space+"g==")
	}
	for _, scheme := range []string{"vmess", "vless", "ss", "ssr", "trojan", "hysteria2", "hy2", "hysteria", "tuic", "anytls", "other"} {
		for _, prefix := range []string{"", "\n", "  ", "a\n", "a\r"} {
			corpus = append(corpus, prefix+scheme+"://test", prefix+strings.ToUpper(scheme)+"://test")
		}
	}
	for _, marker := range []string{"若您未看见节点", "客户端版本低", "获取最新客户端", "请到", "永久网址", "剩余流量", "过期时间", "到期时间", "距离下次重置", "重置剩余", "网站客服", "联系网站客服", "点击订阅", "点击更新", "官方网址", "续费"} {
		corpus = append(corpus, "prefix"+marker+"suffix")
	}
	rng := rand.New(rand.NewSource(42))
	alphabet := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/-_= \t\r\n:|{}")
	for i := 0; i < 2000; i++ {
		data := make([]byte, rng.Intn(160))
		for j := range data {
			data[j] = alphabet[rng.Intn(len(alphabet))]
		}
		corpus = append(corpus, string(data))
		raw := make([]byte, rng.Intn(64))
		rng.Read(raw)
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			corpus = append(corpus, encoding.EncodeToString(raw))
		}
	}
	for _, raw := range []string{"vmess://abc\nss://def", "proxies:\n  - name: n", "{\"outbounds\":[]}", "not a subscription"} {
		corpus = append(corpus, base64.StdEncoding.EncodeToString([]byte(raw)))
	}
	// Force the output-capacity retry, but use synthetic links only.
	corpus = append(corpus, strings.Repeat("ss://x|", 1000))
	for i, input := range corpus {
		if expected && utf8.ValidString(input) {
			for op := uint32(1); op <= 6; op++ {
				_, status, ok := rustSubscriptionCall(op, input)
				if !ok || (op != 1 && status != 0) {
					t.Fatalf("operation %d case %d unexpectedly fell back", op, i)
				}
			}
		}
		actual, ae := decodeSubscriptionBase64(input)
		want, we := decodeSubscriptionBase64Go(input)
		if !bytes.Equal(actual, want) || (ae == nil) != (we == nil) {
			t.Fatalf("decode case %d differs", i)
		}
		if ae != nil && ae.Error() != we.Error() {
			t.Fatalf("decode error %d differs", i)
		}
		for _, fn := range []struct {
			name         string
			actual, want func(string) bool
		}{{"prefix", isProxyURILink, isProxyURILinkGo}, {"lines", hasProxyURI, hasProxyURIGo}, {"notice", isInformationalNode, isInformationalNodeGo}, {"inline", isInlineSubscription, isInlineSubscriptionGo}} {
			if fn.actual(input) != fn.want(input) {
				t.Fatalf("%s case %d differs: %q", fn.name, i, input)
			}
		}
		ar, ai := categorizeSubscriptionInput(input)
		wr, wi := categorizeSubscriptionInputGo(input)
		if !reflect.DeepEqual(ar, wr) || !reflect.DeepEqual(ai, wi) {
			t.Fatalf("classify case %d differs: %q => %#v/%#v vs %#v/%#v", i, input, ar, ai, wr, wi)
		}
	}
	t.Logf("Compared %d synthetic inputs across six subscription functions", len(corpus))
}
