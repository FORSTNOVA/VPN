//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// The packaged core is resolved once per process. Restart to change the selection.
var rustCore struct {
	once               sync.Once
	parse, rate        uintptr
	udpReply           uintptr
	udpRequest         uintptr
	stunMatch          uintptr
	relayEndpoint      uintptr
	dnsRelayReply      uintptr
	stunRelayReply     uintptr
	subscription       uintptr
	nodeFields         uintptr
	nodeFieldsExtended uintptr
	nodeFieldsLegacy   uintptr
	handle             nativeHandle
}

func rustCoreAvailable() bool {
	rustCore.once.Do(func() {
		path := nativeCorePath()
		if !filepath.IsAbs(path) {
			return
		}
		handle, err := nativeLoad(path)
		if err != nil {
			return
		}
		parse, err := nativeLookup(handle, "smartvpn_parse_stun_v1")
		if err != nil {
			_ = nativeClose(handle)
			return
		}
		rate, err := nativeLookup(handle, "smartvpn_throughput_v1")
		if err != nil {
			_ = nativeClose(handle)
			return
		}
		if err := nativeCoreCompatible(handle); err != nil {
			_ = nativeClose(handle)
			return
		}
		rustCore.handle, rustCore.parse, rustCore.rate = handle, parse, rate
		rustCore.udpReply, _ = nativeLookup(handle, "smartvpn_udp_reply_offset_v1")
		rustCore.udpRequest, _ = nativeLookup(handle, "smartvpn_udp_request_v1")
		rustCore.stunMatch, _ = nativeLookup(handle, "smartvpn_stun_match_v1")
		rustCore.relayEndpoint, _ = nativeLookup(handle, "smartvpn_relay_endpoint_v1")
		rustCore.dnsRelayReply, _ = nativeLookup(handle, "smartvpn_dns_relay_reply_v1")
		rustCore.stunRelayReply, _ = nativeLookup(handle, "smartvpn_stun_relay_reply_v1")
		rustCore.subscription, _ = nativeLookup(handle, "smartvpn_subscription_v1")
		rustCore.nodeFields, _ = nativeLookup(handle, "smartvpn_node_fields_v1")
		rustCore.nodeFieldsExtended, _ = nativeLookup(handle, "smartvpn_node_fields_v2")
		rustCore.nodeFieldsLegacy, _ = nativeLookup(handle, "smartvpn_node_fields_v3")
	})
	return rustCore.handle != 0
}

func parseSTUNAddress(message []byte) (string, error) {
	if !rustCoreAvailable() {
		return parseSTUNAddressGo(message)
	}
	var input *byte
	if len(message) != 0 {
		input = &message[0]
	}
	output := make([]byte, 256)
	var written uintptr
	status, _, _ := nativeCall(rustCore.parse, uintptr(unsafe.Pointer(input)), uintptr(len(message)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&written)))
	runtime.KeepAlive(message)
	runtime.KeepAlive(output)
	if written > uintptr(len(output)) {
		return parseSTUNAddressGo(message)
	}
	text := string(output[:written])
	switch int32(status) {
	case 0:
		return text, nil
	case 1:
		return "", errors.New(text)
	default:
		return parseSTUNAddressGo(message)
	}
}

func throughputMbps(bytes int64, elapsed time.Duration) float64 {
	if !rustCoreAvailable() {
		return throughputMbpsGo(bytes, elapsed)
	}
	var output float64
	status, _, _ := nativeCall(rustCore.rate, uintptr(bytes), uintptr(elapsed), uintptr(unsafe.Pointer(&output)))
	if int32(status) != 0 {
		return throughputMbpsGo(bytes, elapsed)
	}
	return output
}

// -1 requests Go fallback. Other negative values are parser errors in the v1 ABI.
func udpReplyOffset(op uintptr, packet []byte) int {
	if !rustCoreAvailable() || rustCore.udpReply == 0 {
		return -1
	}
	n, _, _ := nativeCall(rustCore.udpReply, op, uintptr(unsafe.Pointer(unsafe.SliceData(packet))), uintptr(len(packet)))
	runtime.KeepAlive(packet)
	return int(n)
}

func nativeUDPRequest(op byte, input []byte, value uint16) ([]byte, int) {
	if !rustCoreAvailable() || rustCore.udpRequest == 0 {
		return nil, -1
	}
	// The maximum DNS question is 12 + 255 + 4 bytes; SOCKS headers fit too.
	output := make([]byte, 271)
	result, _, _ := nativeCall(rustCore.udpRequest, uintptr(op), uintptr(unsafe.Pointer(unsafe.SliceData(input))), uintptr(len(input)), uintptr(value), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)))
	runtime.KeepAlive(input)
	if n := int(result); n > 0 && n <= len(output) {
		return output[:n], n
	}
	return nil, int(result)
}

func encodeRelayHeader(kind byte, address []byte, port uint16) []byte {
	if output, _ := nativeUDPRequest(kind, address, port); output != nil {
		return output
	}
	return encodeRelayHeaderGo(kind, address, port)
}

func encodeSTUNRequest(id [12]byte) []byte {
	if packet, n := nativeUDPRequest(2, id[:], 0); n == stunHeaderLength {
		return packet
	}
	return encodeSTUNRequestGo(id)
}

func matchesSTUNReply(request, reply []byte) bool {
	if len(request) < stunHeaderLength || len(reply) < stunHeaderLength {
		return false
	}
	if rustCoreAvailable() && rustCore.stunMatch != 0 {
		n, _, _ := nativeCall(rustCore.stunMatch, uintptr(unsafe.Pointer(&request[8])), uintptr(unsafe.Pointer(&reply[0])))
		runtime.KeepAlive(request)
		runtime.KeepAlive(reply)
		if n <= 1 {
			return n == 1
		}
	}
	return matchesSTUNReplyGo(request, reply)
}

func relayHandshakeFrame(method []byte) ([]byte, bool) {
	op := byte(6)
	expected := 10
	if method == nil {
		op, expected = 5, 3
	}
	frame, status := nativeUDPRequest(op, method, 0)
	if status == expected {
		return frame, true
	}
	if status == -3 && method != nil {
		return nil, false
	}
	return relayHandshakeFrameGo(method)
}

func readRelayEndpoint(r io.Reader) (string, error) {
	if !rustCoreAvailable() || rustCore.relayEndpoint == 0 {
		return readRelayEndpointGo(r)
	}
	var packet [262]byte   // VER, REP, RSV, ATYP, length, 255 host bytes and port
	var endpoint [263]byte // 255 host bytes, brackets, colon and 5 port digits
	used := 0
	for {
		result, _, _ := nativeCall(rustCore.relayEndpoint, uintptr(unsafe.Pointer(&packet[0])), uintptr(used), uintptr(unsafe.Pointer(&endpoint[0])), uintptr(len(endpoint)))
		n := int(result)
		switch {
		case n > 0 && n <= len(packet)-used:
			if _, err := io.ReadFull(r, packet[used:used+n]); err != nil {
				if used < 4 {
					return "", errors.New("本机代理没有回应 UDP 转发请求")
				}
				return "", errors.New("UDP 转发响应被截断")
			}
			used += n
		case n < -512 && n >= -512-len(endpoint):
			return string(endpoint[:-512-n]), nil
		case n == -2:
			return "", errors.New("UDP 转发响应无效")
		case n == -3:
			return "", errors.New("UDP 转发响应使用了未知的地址类型")
		case n <= -257 && n >= -511:
			return "", fmt.Errorf("本机代理拒绝了 UDP 转发（状态码 %d）", -256-n)
		default:
			// Reuse the buffered prefix; never repeat the SOCKS request or a socket read.
			return readRelayEndpointGo(io.MultiReader(bytes.NewReader(packet[:used]), r))
		}
	}
}

func encodeDNSQuery(name string, id uint16) ([]byte, error) {
	if len(name) > 253 {
		return nil, errors.New("查询名称无效")
	}
	output, result := nativeUDPRequest(0, []byte(name), id)
	if output != nil {
		return output, nil
	}
	if result == -2 {
		return nil, errors.New("查询名称无效")
	}
	return encodeDNSQueryGo(name, id)
}

func socks5UDPPayload(packet []byte) ([]byte, error) {
	n := udpReplyOffset(1, packet)
	switch {
	case n >= 0 && n <= len(packet):
		return packet[n:], nil
	case n == -2:
		return nil, errors.New("UDP 回包无效")
	case n == -3:
		return nil, errors.New("UDP 回包使用了未知的地址类型")
	default:
		return socks5UDPPayloadGo(packet)
	}
}

func selectDNSRelayReply(packet []byte, id uint16) []byte {
	if rustCoreAvailable() && rustCore.dnsRelayReply != 0 {
		r, _, _ := nativeCall(rustCore.dnsRelayReply, uintptr(unsafe.Pointer(unsafe.SliceData(packet))), uintptr(len(packet)), uintptr(id))
		runtime.KeepAlive(packet)
		switch offset := int(r); {
		case offset == -2:
			return nil
		case offset >= 0 && offset <= len(packet)-2:
			return packet[offset:]
		}
	}
	return selectDNSRelayReplyGo(packet, id)
}

func selectSTUNRelayReply(packet, request []byte) []byte {
	if len(request) < stunHeaderLength {
		return nil
	}
	if rustCoreAvailable() && rustCore.stunRelayReply != 0 {
		r, _, _ := nativeCall(rustCore.stunRelayReply, uintptr(unsafe.Pointer(unsafe.SliceData(packet))), uintptr(len(packet)), uintptr(unsafe.Pointer(&request[8])))
		runtime.KeepAlive(packet)
		runtime.KeepAlive(request)
		switch offset := int(r); {
		case offset == -2:
			return nil
		case offset >= 0 && offset <= len(packet)-stunHeaderLength:
			return packet[offset:]
		}
	}
	return selectSTUNRelayReplyGo(packet, request)
}

func parseDNSFirstA(message []byte) (string, error) {
	n := udpReplyOffset(2, message)
	switch {
	case n >= 0 && n <= len(message)-4:
		return net.IP(message[n : n+4]).String(), nil
	case n == -4:
		return "", errors.New("DNS 应答过短")
	case n == -5:
		return "", errors.New("DNS 名称越界")
	case n == -6:
		return "", errors.New("DNS 应答被截断")
	case n == -7:
		return "", errors.New("DNS 应答里没有地址记录")
	default:
		return parseDNSFirstAGo(message)
	}
}
