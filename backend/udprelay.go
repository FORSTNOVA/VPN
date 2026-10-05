package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// udpRelayTarget is a public resolver. A DNS query is the smallest round trip
// that proves the node carried a datagram and brought an answer back, and the
// answer is verifiable without trusting the target.
const udpRelayTarget = "1.1.1.1:53"

const udpRelayTimeout = 7 * time.Second

type udpRelayResult struct {
	Target    string `json:"target"`
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// probeUDPRelay asks the running kernel to forward one datagram through the node
// currently in use, over SOCKS5 UDP ASSOCIATE on the local mixed port.
//
// A subscription node that cannot relay UDP leaves HTTP/3 (QUIC) requests
// hanging, which is what makes some desktop clients report a failed request
// instead of falling back to TCP.
func probeUDPRelay(proxyAddress, target string, timeout time.Duration) udpRelayResult {
	result := udpRelayResult{Target: target}
	query, id, err := buildDNSQuery("example.com")
	if err != nil {
		result.Reason = "无法构造探测报文"
		return result
	}
	answer, elapsed, err := socksUDPExchange(proxyAddress, target, query,
		func(payload []byte) bool {
			return len(payload) >= 2 && binary.BigEndian.Uint16(payload[:2]) == id
		}, timeout)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	address, err := parseDNSFirstA(answer)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	result.Reachable = true
	result.LatencyMs = elapsed
	result.Answer = address
	return result
}

// socksUDPExchange sends one datagram to target through the kernel's SOCKS5 UDP
// association and returns the first reply that match accepts, along with how
// long the round trip took. The QUIC probe and the STUN probe are the same
// exchange with a different payload; replies that do not match are ignored
// rather than believed, because a relay may hand back anything.
func socksUDPExchange(proxyAddress, target string, payload []byte,
	match func([]byte) bool, timeout time.Duration) ([]byte, int64, error) {
	deadline := time.Now().Add(timeout)

	tunnel, err := net.DialTimeout("tcp", proxyAddress, timeout)
	if err != nil {
		return nil, 0, errors.New("无法连接本机代理端口")
	}
	defer tunnel.Close()
	if err := tunnel.SetDeadline(deadline); err != nil {
		return nil, 0, errors.New("无法设置超时")
	}
	relay, err := socks5UDPAssociate(tunnel)
	if err != nil {
		return nil, 0, err
	}

	destination, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, 0, errors.New("探测目标地址无效")
	}
	socket, err := net.DialTimeout("udp", relay, timeout)
	if err != nil {
		return nil, 0, errors.New("无法建立 UDP 转发通道")
	}
	defer socket.Close()
	if err := socket.SetDeadline(deadline); err != nil {
		return nil, 0, errors.New("无法设置超时")
	}

	packet := append(socks5UDPHeader(destination), payload...)
	start := time.Now()
	if _, err := socket.Write(packet); err != nil {
		return nil, 0, errors.New("UDP 报文发送失败")
	}

	buffer := make([]byte, 2048)
	for {
		read, err := socket.Read(buffer)
		if err != nil {
			return nil, 0, errors.New("节点没有回传 UDP（超时未收到应答）")
		}
		reply, err := socks5UDPPayload(buffer[:read])
		if err != nil || len(reply) == 0 {
			continue
		}
		if match != nil && !match(reply) {
			continue
		}
		return reply, time.Since(start).Milliseconds(), nil
	}
}

// socks5UDPAssociate performs the SOCKS5 handshake and asks the proxy for a UDP
// relay address. The returned address is where datagrams have to be sent.
func socks5UDPAssociate(conn net.Conn) (string, error) {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return "", errors.New("SOCKS5 握手失败")
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return "", errors.New("SOCKS5 握手没有响应")
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		return "", errors.New("本机代理不接受无认证的 SOCKS5 连接")
	}
	// The client address is left unspecified: the relay address in the reply is
	// what matters.
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", errors.New("UDP 转发请求发送失败")
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", errors.New("本机代理没有回应 UDP 转发请求")
	}
	if head[0] != 0x05 {
		return "", errors.New("UDP 转发响应无效")
	}
	if head[1] != 0x00 {
		return "", fmt.Errorf("本机代理拒绝了 UDP 转发（状态码 %d）", head[1])
	}
	host, port, err := readSocksAddress(conn, head[3])
	if err != nil {
		return "", err
	}
	// A relay that reports 0.0.0.0 means "this host"; sending to it directly
	// would leave through the default interface instead of the relay.
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func readSocksAddress(r io.Reader, atyp byte) (string, int, error) {
	var host string
	switch atyp {
	case 0x01:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(r, raw); err != nil {
			return "", 0, errors.New("UDP 转发响应被截断")
		}
		host = net.IP(raw).String()
	case 0x04:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(r, raw); err != nil {
			return "", 0, errors.New("UDP 转发响应被截断")
		}
		host = net.IP(raw).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(r, length); err != nil {
			return "", 0, errors.New("UDP 转发响应被截断")
		}
		raw := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, raw); err != nil {
			return "", 0, errors.New("UDP 转发响应被截断")
		}
		host = string(raw)
	default:
		return "", 0, errors.New("UDP 转发响应使用了未知的地址类型")
	}
	raw := make([]byte, 2)
	if _, err := io.ReadFull(r, raw); err != nil {
		return "", 0, errors.New("UDP 转发响应被截断")
	}
	return host, int(binary.BigEndian.Uint16(raw)), nil
}

// socks5UDPHeader is the per-datagram envelope a SOCKS5 relay expects.
func socks5UDPHeader(destination *net.UDPAddr) []byte {
	header := []byte{0x00, 0x00, 0x00}
	if ipv4 := destination.IP.To4(); ipv4 != nil {
		header = append(header, 0x01)
		header = append(header, ipv4...)
	} else {
		header = append(header, 0x04)
		header = append(header, destination.IP.To16()...)
	}
	return append(header, byte(destination.Port>>8), byte(destination.Port))
}

// socks5UDPPayload strips the envelope from a datagram the relay returned.
func socks5UDPPayload(packet []byte) ([]byte, error) {
	if len(packet) < 4 || packet[2] != 0x00 {
		return nil, errors.New("UDP 回包无效")
	}
	offset := 4
	switch packet[3] {
	case 0x01:
		offset += 4
	case 0x04:
		offset += 16
	case 0x03:
		if len(packet) < 5 {
			return nil, errors.New("UDP 回包无效")
		}
		offset += 1 + int(packet[4])
	default:
		return nil, errors.New("UDP 回包使用了未知的地址类型")
	}
	offset += 2
	if offset > len(packet) {
		return nil, errors.New("UDP 回包无效")
	}
	return packet[offset:], nil
}

func buildDNSQuery(name string) ([]byte, uint16, error) {
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, 0, err
	}
	encoded, err := encodeDNSName(name)
	if err != nil {
		return nil, 0, err
	}
	id := binary.BigEndian.Uint16(idBytes[:])
	message := make([]byte, 0, 12+len(encoded)+4)
	message = append(message, idBytes[:]...)
	message = append(message, 0x01, 0x00) // standard query, recursion desired
	message = append(message, 0x00, 0x01) // one question
	message = append(message, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	message = append(message, encoded...)
	message = append(message, 0x00, 0x01, 0x00, 0x01) // type A, class IN
	return message, id, nil
}

func encodeDNSName(name string) ([]byte, error) {
	if name == "" || len(name) > 253 {
		return nil, errors.New("查询名称无效")
	}
	var encoded []byte
	for _, label := range splitLabels(name) {
		if label == "" || len(label) > 63 {
			return nil, errors.New("查询名称无效")
		}
		encoded = append(encoded, byte(len(label)))
		encoded = append(encoded, label...)
	}
	return append(encoded, 0x00), nil
}

func splitLabels(name string) []string {
	var labels []string
	current := ""
	for _, char := range name {
		if char == '.' {
			labels = append(labels, current)
			current = ""
			continue
		}
		current += string(char)
	}
	return append(labels, current)
}

func parseDNSFirstA(message []byte) (string, error) {
	if len(message) < 12 {
		return "", errors.New("DNS 应答过短")
	}
	questions := int(binary.BigEndian.Uint16(message[4:6]))
	answers := int(binary.BigEndian.Uint16(message[6:8]))
	offset := 12
	for i := 0; i < questions; i++ {
		next, err := skipDNSName(message, offset)
		if err != nil {
			return "", err
		}
		offset = next + 4
		if offset > len(message) {
			return "", errors.New("DNS 应答被截断")
		}
	}
	for i := 0; i < answers; i++ {
		next, err := skipDNSName(message, offset)
		if err != nil {
			return "", err
		}
		offset = next
		if offset+10 > len(message) {
			return "", errors.New("DNS 应答被截断")
		}
		recordType := binary.BigEndian.Uint16(message[offset : offset+2])
		length := int(binary.BigEndian.Uint16(message[offset+8 : offset+10]))
		offset += 10
		if offset+length > len(message) {
			return "", errors.New("DNS 应答被截断")
		}
		if recordType == 1 && length == 4 {
			return net.IP(message[offset : offset+4]).String(), nil
		}
		offset += length
	}
	return "", errors.New("DNS 应答里没有地址记录")
}

func skipDNSName(message []byte, offset int) (int, error) {
	for {
		if offset >= len(message) {
			return 0, errors.New("DNS 名称越界")
		}
		length := int(message[offset])
		if length == 0 {
			return offset + 1, nil
		}
		if length&0xc0 == 0xc0 {
			if offset+2 > len(message) {
				return 0, errors.New("DNS 名称越界")
			}
			return offset + 2, nil
		}
		offset += 1 + length
	}
}
