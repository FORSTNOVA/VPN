//go:build rustcore && windows && amd64

package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestUDPRequestEncoderParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_REQUEST_RUST")
	if expected == "" {
		expected = os.Getenv("SMARTVPN_TEST_EXPECT_RUST")
	}
	label := strings.Repeat("a", 63)
	maxName := strings.Repeat(label+".", 3) + strings.Repeat("b", 61)
	for _, name := range []string{"a", "例子.test", maxName, "", ".a", "a.", "a..b", label + "a", maxName + "b", "a\xff.b"} {
		for _, id := range []uint16{0, 0x1234, 65535} {
			want, wantErr := encodeDNSQueryGo(name, id)
			got, err := encodeDNSQuery(name, id)
			if !bytes.Equal(got, want) || fmt.Sprint(err) != fmt.Sprint(wantErr) {
				t.Fatalf("DNS %q id %d: %x %v, want %x %v", name, id, got, err, want, wantErr)
			}
			raw, status := nativeUDPRequest(0, []byte(name), id)
			if expected == "0" && status != -1 {
				t.Fatal("expected missing request export")
			}
			if expected == "1" {
				switch {
				case name == "a\xff.b":
					if status != -1 {
						t.Fatal("expected invalid UTF-8 fallback")
					}
				case wantErr != nil:
					if status != -2 {
						t.Fatalf("native DNS rejection %q: %d", name, status)
					}
				default:
					if !bytes.Equal(raw, want) {
						t.Fatalf("native DNS %q: %x", name, raw)
					}
				}
			}
		}
	}
	for _, tc := range []struct {
		kind    byte
		address []byte
	}{
		{1, []byte{192, 0, 2, 1}}, {4, make([]byte, 16)}, {3, bytes.Repeat([]byte{0xfe}, 255)},
	} {
		want := encodeRelayHeaderGo(tc.kind, tc.address, 65535)
		got := encodeRelayHeader(tc.kind, tc.address, 65535)
		raw, _ := nativeUDPRequest(tc.kind, tc.address, 65535)
		if !bytes.Equal(got, want) || (expected == "1" && !bytes.Equal(raw, want)) {
			t.Fatalf("SOCKS ATYP %d encoding mismatch", tc.kind)
		}
	}
}

func TestSTUNTransactionParity(t *testing.T) {
	expect := os.Getenv("SMARTVPN_TEST_EXPECT_STUN_RUST")
	if expect == "" {
		expect = os.Getenv("SMARTVPN_TEST_EXPECT_RUST")
	}
	for seed := 0; seed < 256; seed++ {
		var id [12]byte
		for i := range id {
			id[i] = byte(seed + 17*i)
		}
		packet := encodeSTUNRequest(id)
		want := encodeSTUNRequestGo(id)
		raw, status := nativeUDPRequest(2, id[:], 0)
		if !bytes.Equal(packet, want) ||
			(expect == "1" && (rustCore.stunMatch == 0 || !bytes.Equal(raw, want))) ||
			(expect == "0" && (rustCore.stunMatch != 0 || status != -1)) {
			t.Fatalf("unexpected STUN encoder or matcher for seed %d", seed)
		}
		reply := append([]byte(nil), packet...)
		reply[0] = 1
		for n := 0; n <= len(reply); n++ {
			if matchesSTUNReply(packet, reply[:n]) != (n == stunHeaderLength) ||
				matchesSTUNReply(packet[:n], reply) != (n == stunHeaderLength) {
				t.Fatalf("STUN header length %d", n)
			}
		}
		for i := range reply {
			reply[i] ^= 1
			if matchesSTUNReply(packet, reply) != matchesSTUNReplyGo(packet, reply) {
				t.Fatalf("STUN header mismatch at byte %d", i)
			}
			reply[i] ^= 1
		}
	}
}

func TestRelayEndpointExport(t *testing.T) {
	expect := os.Getenv("SMARTVPN_TEST_EXPECT_RELAY_RUST")
	if expect == "" {
		expect = os.Getenv("SMARTVPN_TEST_EXPECT_RUST")
	}
	available := rustCoreAvailable() && rustCore.relayEndpoint != 0
	if expect == "1" && !available || expect == "0" && available {
		t.Fatalf("unexpected relay endpoint export availability: %v", available)
	}
}

func TestNativeRelayHandshake(t *testing.T) {
	expect := os.Getenv("SMARTVPN_TEST_EXPECT_HANDSHAKE_RUST")
	if expect == "" {
		expect = os.Getenv("SMARTVPN_TEST_EXPECT_RUST")
	}
	for _, method := range [][]byte{nil, {}, {5}, {4, 0}, {5, 0}, {5, 2}, {5, 255}, {5, 0, 0}} {
		op := byte(6)
		if method == nil {
			op = 5
		}
		want, accepted := relayHandshakeFrameGo(method)
		frame, ok := relayHandshakeFrame(method)
		raw, status := nativeUDPRequest(op, method, 0)
		if ok != accepted || !bytes.Equal(frame, want) ||
			expect == "1" && (accepted && (!bytes.Equal(raw, want) || status != len(want)) || !accepted && status != -3) ||
			expect == "0" && status != -1 {
			t.Fatalf("method %#v: got %x %v, native status %d", method, frame, ok, status)
		}
	}
}
