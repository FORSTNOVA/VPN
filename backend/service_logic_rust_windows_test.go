//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"math/rand"
	"os"
	"testing"
)

func TestRustServiceLogicParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_SERVICE_RUST") == "1"
	if serviceRustAvailable() != expected {
		t.Fatalf("service DLL availability mismatch")
	}
	for f := 0; f < 16; f++ {
		a, b, c, d := f&1 != 0, f&2 != 0, f&4 != 0, f&8 != 0
		if serviceTakeoverDecision(a, b, c, d) != serviceTakeoverDecisionGo(a, b, c, d) {
			t.Fatal("takeover mismatch", f)
		}
	}
	r := rand.New(rand.NewSource(20261116))
	for i := 0; i < 5000; i++ {
		body := make([]byte, r.Intn(512))
		_, _ = r.Read(body)
		if i%3 == 0 {
			for j := range body {
				body[j] %= 5
			}
		}
		if i%7 == 0 {
			body = append(body, '\n')
		}
		secret := make([]byte, r.Intn(12))
		_, _ = r.Read(secret)
		if len(body) > 0 && i%2 == 0 {
			start := r.Intn(len(body))
			secret = body[start:min(len(body), start+1+r.Intn(4))]
		}
		want := redactLogBytesGo(body, secret)
		got := redactLogBytes(body, secret)
		if !bytes.Equal(got, want) {
			t.Fatal("redaction mismatch", i)
		}
		if expected && len(secret) > 0 {
			v, ok := rustRedactLogBytes(body, secret)
			if !ok || !bytes.Equal(v, want) {
				t.Fatal("FFI not exercised", i)
			}
		}
		limit := r.Intn(len(body) + 5)
		if !bytes.Equal(tailFromLine(body, limit), tailFromLineGo(body, limit)) {
			t.Fatal("tail mismatch", i)
		}
	}
	for _, body := range [][]byte{[]byte("abc\ndef\nxyz"), []byte("\n"), nil, []byte("abc"), bytes.Repeat([]byte("x"), 1000)} {
		for limit := 0; limit <= len(body)+1; limit++ {
			if !bytes.Equal(tailFromLine(body, limit), tailFromLineGo(body, limit)) {
				t.Fatal("tail boundary")
			}
		}
		if !bytes.Equal(redactLogBytes(body, []byte("x")), redactLogBytesGo(body, []byte("x"))) {
			t.Fatal("growth/retry")
		}
	}
}
