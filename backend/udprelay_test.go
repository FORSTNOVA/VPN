package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// dnsAnswerFor builds a minimal DNS reply to the probe's query: one A record
// pointing at 10.20.30.40, with the question section left in place.
func dnsAnswerFor(query []byte, mangleID bool) []byte {
	response := append([]byte{}, query...)
	if mangleID {
		response[0] ^= 0xff
	}
	response[2], response[3] = 0x81, 0x80 // response, recursion available
	response[6], response[7] = 0x00, 0x01 // one answer
	response = append(response, 0xc0, 0x0c)
	response = append(response, 0x00, 0x01, 0x00, 0x01) // type A, class IN
	response = append(response, 0x00, 0x00, 0x00, 0x3c) // ttl
	response = append(response, 0x00, 0x04, 10, 20, 30, 40)
	return response
}

// startFakeRelay serves the SOCKS5 UDP ASSOCIATE handshake the probe performs.
// It answers with 0.0.0.0, which is what the kernel actually reports, so the
// loopback substitution is covered too.
func startFakeRelay(t *testing.T, mode string) string {
	t.Helper()
	tunnel, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tunnel.Close()
		_ = socket.Close()
	})
	relay := socket.LocalAddr().(*net.UDPAddr)

	go func() {
		for {
			conn, err := tunnel.Accept()
			if err != nil {
				return
			}
			go serveFakeHandshake(conn, relay, mode)
		}
	}()
	go func() {
		buffer := make([]byte, 2048)
		for {
			read, from, err := socket.ReadFrom(buffer)
			if err != nil {
				return
			}
			payload, err := socks5UDPPayload(buffer[:read])
			if err != nil {
				continue
			}
			envelope := buffer[:read-len(payload)]
			switch mode {
			case "answer":
				_, _ = socket.WriteTo(append(append([]byte{}, envelope...), dnsAnswerFor(payload, false)...), from)
			case "stale":
				_, _ = socket.WriteTo(append(append([]byte{}, envelope...), dnsAnswerFor(payload, true)...), from)
			}
		}
	}()
	return tunnel.Addr().String()
}

func serveFakeHandshake(conn net.Conn, relay *net.UDPAddr, mode string) {
	defer conn.Close()
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	request := make([]byte, 10)
	if _, err := io.ReadFull(conn, request); err != nil {
		return
	}
	if mode == "refuse" {
		_, _ = conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	reply := []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0}
	reply = append(reply, byte(relay.Port>>8), byte(relay.Port))
	if _, err := conn.Write(reply); err != nil {
		return
	}
	// The association stays open for as long as the probe needs it.
	_, _ = io.Copy(io.Discard, conn)
}

func TestProbeUDPRelayReportsAForwardedAnswer(t *testing.T) {
	address := startFakeRelay(t, "answer")
	result := probeUDPRelay(address, "1.1.1.1:53", 3*time.Second)
	if !result.Reachable {
		t.Fatalf("a relayed answer must count as reachable: %+v", result)
	}
	if result.Answer != "10.20.30.40" {
		t.Fatalf("answer = %q, want the relayed address", result.Answer)
	}
	if result.Target != "1.1.1.1:53" {
		t.Fatalf("target = %q", result.Target)
	}
	if result.LatencyMs < 0 {
		t.Fatalf("latency = %d", result.LatencyMs)
	}
}

func TestProbeUDPRelayReportsSilenceAsUnreachable(t *testing.T) {
	address := startFakeRelay(t, "silent")
	result := probeUDPRelay(address, "1.1.1.1:53", 500*time.Millisecond)
	if result.Reachable {
		t.Fatal("a node that returns nothing must not count as relaying UDP")
	}
	if !strings.Contains(result.Reason, "超时") {
		t.Fatalf("reason = %q, want a timeout explanation", result.Reason)
	}
}

func TestProbeUDPRelayReportsARefusedAssociation(t *testing.T) {
	address := startFakeRelay(t, "refuse")
	result := probeUDPRelay(address, "1.1.1.1:53", time.Second)
	if result.Reachable {
		t.Fatal("a refused association must not count as reachable")
	}
	if !strings.Contains(result.Reason, "拒绝") {
		t.Fatalf("reason = %q, want the refusal to be reported", result.Reason)
	}
}

func TestProbeUDPRelayIgnoresAnUnrelatedDatagram(t *testing.T) {
	// A reply that does not match the query is not evidence that the node
	// relayed anything: the check has to key on the DNS transaction id.
	address := startFakeRelay(t, "stale")
	result := probeUDPRelay(address, "1.1.1.1:53", 500*time.Millisecond)
	if result.Reachable {
		t.Fatalf("an unrelated datagram must be ignored: %+v", result)
	}
}

func TestProbeUDPRelayReportsAnUnreachableProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	result := probeUDPRelay(address, "1.1.1.1:53", 500*time.Millisecond)
	if result.Reachable || !strings.Contains(result.Reason, "代理端口") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseDNSFirstA(t *testing.T) {
	query, _, err := buildDNSQuery("example.com")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := parseDNSFirstA(dnsAnswerFor(query, false))
	if err != nil {
		t.Fatal(err)
	}
	if answer != "10.20.30.40" {
		t.Fatalf("answer = %q", answer)
	}
	if _, err := parseDNSFirstA(query); err == nil {
		t.Fatal("a question with no answer section must not yield an address")
	}
	if _, err := parseDNSFirstA([]byte{0x00, 0x01}); err == nil {
		t.Fatal("a truncated message must be rejected")
	}
}

func TestDNSQueryEncoding(t *testing.T) {
	query, id, err := buildDNSQuery("a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(query) < 12 || binaryID(query) != id {
		t.Fatalf("the query must carry its own transaction id")
	}
	if got := query[12:]; string(got[:1]) != "\x01" || !strings.HasPrefix(string(got[1:]), "a") {
		t.Fatalf("unexpected question encoding: %q", query[12:])
	}
	if _, _, err := buildDNSQuery(""); err == nil {
		t.Fatal("an empty name must be rejected")
	}
	if _, _, err := buildDNSQuery(strings.Repeat("a", 64) + ".com"); err == nil {
		t.Fatal("an over-long label must be rejected")
	}
}

func binaryID(message []byte) uint16 {
	return uint16(message[0])<<8 | uint16(message[1])
}

func TestSkipDNSNameFollowsACompressionPointer(t *testing.T) {
	message := []byte{0x03, 'w', 'w', 'w', 0xc0, 0x00, 0x00, 0x01}
	next, err := skipDNSName(message, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next != 6 {
		t.Fatalf("next = %d, want 6", next)
	}
	if _, err := skipDNSName([]byte{0x05, 'a'}, 0); err == nil {
		t.Fatal("a label running past the message must be rejected")
	}
}

func TestSOCKS5UDPEnvelopeRoundTrip(t *testing.T) {
	destination := &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 53}
	header := socks5UDPHeader(destination)
	if len(header) != 10 || header[3] != 0x01 {
		t.Fatalf("unexpected header: %v", header)
	}
	packet := append(append([]byte{}, header...), []byte("payload")...)
	payload, err := socks5UDPPayload(packet)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q", payload)
	}
	if _, err := socks5UDPPayload([]byte{0x00, 0x00, 0x01, 0x01}); err == nil {
		t.Fatal("a fragment header must be rejected")
	}
}
