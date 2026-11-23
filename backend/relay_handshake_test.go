package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

type relayHandshakeTranscript struct {
	io.Reader
	writes    [][]byte
	failWrite int
}

func (s *relayHandshakeTranscript) Write(p []byte) (int, error) {
	s.writes = append(s.writes, bytes.Clone(p))
	if len(s.writes) == s.failWrite {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestRelayHandshakeTranscript(t *testing.T) {
	hello := []byte{5, 1, 0}
	associate := []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, tc := range []struct {
		name              string
		reply             []byte
		failWrite, writes int
		endpoint, message string
	}{
		{"hello write", nil, 1, 1, "", "SOCKS5 握手失败"},
		{"short method", []byte{5}, 0, 1, "", "SOCKS5 握手没有响应"},
		{"version", []byte{4, 0}, 0, 1, "", "本机代理不接受无认证的 SOCKS5 连接"},
		{"password", []byte{5, 2}, 0, 1, "", "本机代理不接受无认证的 SOCKS5 连接"},
		{"no method", []byte{5, 255}, 0, 1, "", "本机代理不接受无认证的 SOCKS5 连接"},
		{"associate write", []byte{5, 0}, 2, 2, "", "UDP 转发请求发送失败"},
		{"short associate reply", []byte{5, 0, 5}, 0, 2, "", "本机代理没有回应 UDP 转发请求"},
		{"accepted", []byte{5, 0, 5, 0, 0, 1, 192, 0, 2, 9, 0, 53}, 0, 2, "192.0.2.9:53", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &relayHandshakeTranscript{Reader: iotest.OneByteReader(bytes.NewReader(tc.reply)), failWrite: tc.failWrite}
			got, err := socks5UDPAssociate(stream)
			if got != tc.endpoint || (err != nil) != (tc.message != "") || err != nil && err.Error() != tc.message {
				t.Fatalf("got %q %v", got, err)
			}
			if len(stream.writes) != tc.writes || !bytes.Equal(stream.writes[0], hello) || tc.writes == 2 && !bytes.Equal(stream.writes[1], associate) {
				t.Fatalf("unexpected writes %x", stream.writes)
			}
		})
	}
}
