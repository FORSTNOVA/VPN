//go:build rustcore && windows && amd64

package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestUDPReplyParserParity(t *testing.T) {
	switch os.Getenv("SMARTVPN_TEST_EXPECT_RUST") {
	case "1":
		if udpReplyOffset(1, make([]byte, 4)) != -3 || udpReplyOffset(2, nil) != -4 {
			t.Fatal("Rust UDP parser export did not execute")
		}
	case "0":
		if udpReplyOffset(1, nil) != -1 {
			t.Fatal("expected Go fallback")
		}
	}
	for _, addr := range []string{"127.0.0.1:53", "[::1]:53", "example.invalid:53"} {
		header, err := socks5UDPHeader(addr)
		if err != nil {
			t.Fatal(err)
		}
		packet := append(header, 0xaa, 0xbb)
		for n := 0; n <= len(packet); n++ {
			want, e := socks5UDPPayloadGo(packet[:n])
			got, err := socks5UDPPayload(packet[:n])
			if !bytes.Equal(got, want) || fmt.Sprint(err) != fmt.Sprint(e) {
				t.Fatalf("SOCKS %s prefix %d: %x %v, want %x %v", addr, n, got, err, want, e)
			}
		}
	}
	query, _, err := buildDNSQuery("test.invalid")
	if err != nil {
		t.Fatal(err)
	}
	reply := dnsAnswerFor(query, false)
	for n := 0; n <= len(reply); n++ {
		checkDNSParity(t, reply[:n])
	}
	for i := range reply {
		original := reply[i]
		for _, b := range []byte{0, 1, 4, 63, 192, 255} {
			reply[i] = b
			checkDNSParity(t, reply)
		}
		reply[i] = original
	}
}

func checkDNSParity(t *testing.T, message []byte) {
	t.Helper()
	want, e := parseDNSFirstAGo(message)
	got, err := parseDNSFirstA(message)
	if got != want || fmt.Sprint(err) != fmt.Sprint(e) {
		t.Fatalf("DNS %x: %q %v, want %q %v", message, got, err, want, e)
	}
}

func TestDNSRelaySelectionParity(t *testing.T) {
	expect := os.Getenv("SMARTVPN_TEST_EXPECT_DNS_RELAY_RUST")
	if expect == "" {
		expect = os.Getenv("SMARTVPN_TEST_EXPECT_RUST")
	}
	available := rustCoreAvailable() && rustCore.dnsRelayReply != 0
	if expect == "1" && !available || expect == "0" && available {
		t.Fatalf("unexpected DNS relay selector availability: %v", available)
	}
	for _, target := range []string{"127.0.0.1:53", "[::1]:53", "test.invalid:53"} {
		header, err := socks5UDPHeader(target)
		if err != nil {
			t.Fatal(err)
		}
		packet := append(header, 0x12, 0x34, 0xfe)
		for end := 0; end <= len(packet); end++ {
			for _, id := range []uint16{0, 0x1234, 0x3412, 65535} {
				got := selectDNSRelayReply(packet[:end], id)
				want, err := socks5UDPPayloadGo(packet[:end])
				if err != nil || len(want) < 2 || uint16(want[0])<<8|uint16(want[1]) != id {
					want = nil
				}
				if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
					t.Fatalf("%s prefix %d id %d: got %x, want %x", target, end, id, got, want)
				}
			}
		}
	}
}

func TestSTUNRelaySelectionParity(t *testing.T) {
	expect := os.Getenv("SMARTVPN_TEST_EXPECT_STUN_RELAY_RUST")
	if expect == "" {
		expect = os.Getenv("SMARTVPN_TEST_EXPECT_RUST")
	}
	available := rustCoreAvailable() && rustCore.stunRelayReply != 0
	if expect == "1" && !available || expect == "0" && available {
		t.Fatalf("unexpected STUN relay selector availability: %v", available)
	}
	request := encodeSTUNRequestGo([12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 255})
	check := func(packet, req []byte) {
		t.Helper()
		want, err := socks5UDPPayloadGo(packet)
		if err != nil || !matchesSTUNReplyGo(req, want) {
			want = nil
		}
		got := selectSTUNRelayReply(packet, req)
		if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
			t.Fatalf("packet %x request %x: got %x, want %x", packet, req, got, want)
		}
	}
	for _, target := range []string{"127.0.0.1:53", "[::1]:53", "test.invalid:53"} {
		header, err := socks5UDPHeader(target)
		if err != nil {
			t.Fatal(err)
		}
		packet := append(header, request...)
		packet[len(header)] = 1
		for end := 0; end <= len(packet); end++ {
			check(packet[:end], request)
		}
		for end := 0; end <= len(request); end++ {
			check(packet, request[:end])
		}
		for i := range packet {
			packet[i] ^= 1
			check(packet, request)
			packet[i] ^= 1
		}
	}
}
