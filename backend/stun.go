package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"time"
)

// A STUN binding request is the smallest way to ask the network what address it
// sees us as, over UDP. Browsers use exactly this for WebRTC, so the answer is
// comparable with the TCP exit address: if the two differ, the datagrams the
// node forwards are leaving through something other than the tunnel.
const (
	stunBindingRequest   = 0x0001
	stunBindingSuccess   = 0x0101
	stunMagicCookie      = 0x2112A442
	stunHeaderLength     = 20
	stunAttributeMapped  = 0x0001
	stunAttributeXor     = 0x0020
	stunAddressIPv4      = 0x01
	stunAddressIPv6      = 0x02
)

// The servers are tried in order and the first answer wins.
var stunTargets = []string{"stun.l.google.com:19302", "stun.cloudflare.com:3478"}

func buildSTUNRequest() ([]byte, error) {
	message := make([]byte, stunHeaderLength)
	if _, err := rand.Read(message[8:20]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint16(message[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(message[2:4], 0)
	binary.BigEndian.PutUint32(message[4:8], stunMagicCookie)
	return message, nil
}

// parseSTUNAddress reads the address the server saw. XOR-MAPPED-ADDRESS is what
// a modern server sends and is preferred; MAPPED-ADDRESS is still accepted.
func parseSTUNAddress(message []byte) (string, error) {
	if len(message) < stunHeaderLength {
		return "", errors.New("STUN 应答过短")
	}
	if binary.BigEndian.Uint16(message[0:2]) != stunBindingSuccess {
		return "", errors.New("不是 STUN 绑定应答")
	}
	if binary.BigEndian.Uint32(message[4:8]) != stunMagicCookie {
		return "", errors.New("STUN 应答缺少魔术字")
	}
	length := int(binary.BigEndian.Uint16(message[2:4]))
	end := min(stunHeaderLength+length, len(message))
	transaction := message[8:20]
	plain := ""
	for offset := stunHeaderLength; offset+4 <= end; {
		kind := binary.BigEndian.Uint16(message[offset : offset+2])
		size := int(binary.BigEndian.Uint16(message[offset+2 : offset+4]))
		value := message[offset+4 : min(offset+4+size, end)]
		switch kind {
		case stunAttributeXor:
			if address, err := readSTUNAddress(value, transaction); err == nil {
				return address, nil
			}
		case stunAttributeMapped:
			if address, err := readSTUNAddress(value, nil); err == nil {
				plain = address
			}
		}
		// Attributes are padded to a four byte boundary.
		offset += 4 + size + (4-size%4)%4
	}
	if plain != "" {
		return plain, nil
	}
	return "", errors.New("STUN 应答里没有地址")
}

// readSTUNAddress decodes one address attribute. A nil transaction means the
// value is not XORed.
func readSTUNAddress(value []byte, transaction []byte) (string, error) {
	if len(value) < 4 {
		return "", errors.New("STUN 地址字段过短")
	}
	family := value[1]
	port := int(binary.BigEndian.Uint16(value[2:4]))
	if transaction != nil {
		port ^= stunMagicCookie >> 16
	}
	var raw net.IP
	switch family {
	case stunAddressIPv4:
		if len(value) < 8 {
			return "", errors.New("STUN IPv4 地址字段过短")
		}
		raw = make(net.IP, 4)
		copy(raw, value[4:8])
	case stunAddressIPv6:
		if len(value) < 20 {
			return "", errors.New("STUN IPv6 地址字段过短")
		}
		raw = make(net.IP, 16)
		copy(raw, value[4:20])
	default:
		return "", errors.New("STUN 地址类型未知")
	}
	if transaction != nil {
		mask := stunMask(len(raw), transaction)
		for i := range raw {
			raw[i] ^= mask[i]
		}
	}
	if port < 1 || port > 65535 {
		return "", errors.New("STUN 端口无效")
	}
	return net.JoinHostPort(raw.String(), strconv.Itoa(port)), nil
}

// stunMask is the byte sequence an XOR-MAPPED-ADDRESS is masked with: the magic
// cookie for IPv4, the cookie followed by the transaction id for IPv6.
func stunMask(size int, transaction []byte) []byte {
	mask := make([]byte, size)
	binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
	if size > 4 && len(transaction) >= 12 {
		copy(mask[4:], transaction[:12])
	}
	return mask
}

// probeUDPEgress reports the address the network sees for the datagrams the node
// forwards, which is what a WebRTC-style leak test asks.
func probeUDPEgress(proxyAddress string, timeout time.Duration) (string, int64, error) {
	request, err := buildSTUNRequest()
	if err != nil {
		return "", 0, errors.New("无法构造 STUN 请求")
	}
	var lastErr error
	for _, target := range stunTargets {
		reply, elapsed, err := socksUDPExchange(proxyAddress, target, request,
			func(payload []byte) bool {
				return len(payload) >= stunHeaderLength &&
					binary.BigEndian.Uint16(payload[0:2]) == stunBindingSuccess &&
					bytes.Equal(payload[8:20], request[8:20])
			}, timeout)
		if err != nil {
			lastErr = err
			continue
		}
		address, err := parseSTUNAddress(reply)
		if err != nil {
			lastErr = err
			continue
		}
		return address, elapsed, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的 STUN 服务器")
	}
	return "", 0, lastErr
}
