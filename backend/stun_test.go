package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

// stunResponse builds a binding reply the way a server would, with the XORing
// done here rather than through the code under test.
func stunResponse(txid []byte, attribute uint16, family byte, address net.IP, port int, xor bool) []byte {
	value := []byte{0x00, family}
	encodedPort := uint16(port)
	if xor {
		encodedPort ^= uint16(stunMagicCookie >> 16)
	}
	value = binary.BigEndian.AppendUint16(value, encodedPort)
	raw := address.To4()
	if family == stunAddressIPv6 {
		raw = address.To16()
	}
	raw = append([]byte{}, raw...)
	if xor {
		mask := make([]byte, len(raw))
		binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
		if len(raw) == 16 {
			copy(mask[4:], txid[:12])
		}
		for i := range raw {
			raw[i] ^= mask[i]
		}
	}
	value = append(value, raw...)

	message := make([]byte, stunHeaderLength)
	binary.BigEndian.PutUint16(message[0:2], stunBindingSuccess)
	binary.BigEndian.PutUint16(message[2:4], uint16(4+len(value)))
	binary.BigEndian.PutUint32(message[4:8], stunMagicCookie)
	copy(message[8:20], txid)
	message = binary.BigEndian.AppendUint16(message, attribute)
	message = binary.BigEndian.AppendUint16(message, uint16(len(value)))
	return append(message, value...)
}

func TestBuildSTUNRequest(t *testing.T) {
	first, err := buildSTUNRequest()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != stunHeaderLength {
		t.Fatalf("length = %d", len(first))
	}
	if binary.BigEndian.Uint16(first[0:2]) != stunBindingRequest {
		t.Fatal("the request must be a binding request")
	}
	if binary.BigEndian.Uint32(first[4:8]) != stunMagicCookie {
		t.Fatal("the request must carry the magic cookie")
	}
	second, _ := buildSTUNRequest()
	// The transaction id is what tells a reply from this request apart from any
	// other datagram the relay hands back.
	if bytes.Equal(first[8:20], second[8:20]) {
		t.Fatal("each request needs its own transaction id")
	}
}

func TestParseSTUNAddressXorMapped(t *testing.T) {
	request, _ := buildSTUNRequest()
	txid := request[8:20]
	message := stunResponse(txid, stunAttributeXor, stunAddressIPv4, net.ParseIP("203.0.113.7"), 54321, true)

	address, err := parseSTUNAddress(message)
	if err != nil {
		t.Fatal(err)
	}
	if address != "203.0.113.7:54321" {
		t.Fatalf("address = %q", address)
	}
}

func TestParseSTUNAddressIPv6XorMapped(t *testing.T) {
	request, _ := buildSTUNRequest()
	txid := request[8:20]
	message := stunResponse(txid, stunAttributeXor, stunAddressIPv6,
		net.ParseIP("2001:db8::1234"), 443, true)

	address, err := parseSTUNAddress(message)
	if err != nil {
		t.Fatal(err)
	}
	if address != "[2001:db8::1234]:443" {
		t.Fatalf("address = %q", address)
	}
}

func TestParseSTUNAddressPlainFallback(t *testing.T) {
	request, _ := buildSTUNRequest()
	txid := request[8:20]
	// A server that still answers with MAPPED-ADDRESS must be understood.
	message := stunResponse(txid, stunAttributeMapped, stunAddressIPv4, net.ParseIP("198.51.100.9"), 1234, false)
	address, err := parseSTUNAddress(message)
	if err != nil {
		t.Fatal(err)
	}
	if address != "198.51.100.9:1234" {
		t.Fatalf("address = %q", address)
	}
}

func TestParseSTUNAddressRejections(t *testing.T) {
	request, _ := buildSTUNRequest()
	txid := request[8:20]
	cases := map[string][]byte{
		"short":              {0x01, 0x01},
		"not a response":     append([]byte{}, request...),
		"no address":         stunHeaderOnly(txid),
		"unknown family":     stunResponse(txid, stunAttributeXor, 0x05, net.ParseIP("203.0.113.7"), 443, true),
		"truncated address":  stunResponse(txid, stunAttributeXor, stunAddressIPv4, net.ParseIP("203.0.113.7"), 443, true)[:24],
	}
	for name, message := range cases {
		t.Run(name, func(t *testing.T) {
			if address, err := parseSTUNAddress(message); err == nil {
				t.Fatalf("expected a refusal, got %q", address)
			}
		})
	}
}

func stunHeaderOnly(txid []byte) []byte {
	message := make([]byte, stunHeaderLength)
	binary.BigEndian.PutUint16(message[0:2], stunBindingSuccess)
	binary.BigEndian.PutUint32(message[4:8], stunMagicCookie)
	copy(message[8:20], txid)
	return message
}

func TestUDPAndDNSVerdicts(t *testing.T) {
	if !udpMatchesExit("203.0.113.7:54321", "203.0.113.7") {
		t.Fatal("the same address on a different port is still the same exit")
	}
	if udpMatchesExit("198.51.100.9:443", "203.0.113.7") {
		t.Fatal("a different address means the datagrams left elsewhere")
	}
	for _, mapped := range []string{"", "203.0.113.7", "not-an-address"} {
		if udpMatchesExit(mapped, "203.0.113.7") {
			t.Errorf("mapped %q must not count as a match", mapped)
		}
	}

	if !dnsMatchesExit("JP", "jp") {
		t.Fatal("the same country in different cases is a match")
	}
	for _, pair := range [][2]string{{"", "JP"}, {"JP", ""}, {"CN", "JP"}} {
		if dnsMatchesExit(pair[0], pair[1]) {
			t.Errorf("%v must not count as a match", pair)
		}
	}
}
