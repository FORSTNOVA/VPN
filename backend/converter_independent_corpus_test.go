package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func converterIndependentCorpus() [][]byte {
	out := [][]byte{nil, []byte("bad"), []byte("proxies: []"), []byte("proxies: [{name: X}, {name: X, type: ss}, {name: Y, type: trojan}]"), []byte("anytls://synthetic@example.invalid#orphan\ntrojan://synthetic@example.invalid#real")}
	for _, kind := range []string{"shadowsocks", "vmess", "vless", "trojan", "hysteria2", "tuic"} {
		for i := 0; i < 16; i++ {
			m := map[string]any{"type": kind, "tag": " N ", "server": " example.invalid ", "server_port": 443, "method": "aes-128-gcm", "password": "synthetic", "uuid": "synthetic-id", "security": " auto ", "alter_id": 2, "flow": "xtls-rprx-vision", "congestion_control": "bbr", "congestion_controller": "cubic", "udp_relay_mode": "quic", "zero_rtt_handshake": true, "heartbeat": "10s", "up_mbps": 20, "down_mbps": 30}
			if i&1 != 0 {
				m["tls"] = map[string]any{"enabled": true, "server_name": "sni.invalid", "insecure": true, "alpn": []string{"h2", "http/1.1"}, "reality": map[string]any{"enabled": true, "public_key": "public-synthetic", "short_id": "abcd"}, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}}
			}
			if i&2 != 0 {
				m["transport"] = map[string]any{"type": "ws", "path": "/raw", "headers": map[string]string{"Host": "ws.invalid"}}
			}
			if i&4 != 0 {
				m["password"] = ""
				m["uuid"] = ""
			}
			if i&8 != 0 {
				m["transport"] = map[string]any{"type": "unsupported"}
				m["server_port"] = 0
			}
			b, _ := json.Marshal(map[string]any{"outbounds": []any{m}})
			out = append(out, b, []byte(base64.StdEncoding.EncodeToString(b)))
		}
	}
	for _, net := range []string{"", "tcp", "ws", "grpc", "h2", "unsupported"} {
		for _, id := range []string{"", "synthetic"} {
			b, _ := json.Marshal(map[string]any{"ps": " V ", "add": " example.invalid ", "port": "443", "id": id, "net": net, "tls": "tls", "host": "host.invalid", "path": "/raw", "fp": "chrome", "alpn": []string{"h2"}})
			out = append(out, []byte("vmess://"+base64.StdEncoding.EncodeToString(b)))
		}
	}
	return out
}

func TestConverterIndependentLegacyCorpusFingerprint(t *testing.T) {
	h := sha256.New()
	for _, b := range converterIndependentCorpus() {
		p, n, e := parseSubscriptionBodyToProxies(b)
		err := ""
		if e != nil {
			err = e.Error()
		}
		record, _ := json.Marshal([]any{p, n, err})
		h.Write(record)
		out, nn, ee := ensureClashYAML(b)
		err = ""
		if ee != nil {
			err = ee.Error()
		}
		record, _ = json.Marshal([]any{out, nn, err})
		h.Write(record)
	}
	got := fmt.Sprintf("%x", h.Sum(nil))
	const want = "5c1ba94744246f6c92efdafd393d527dd5f1ed3b00cdc3da47d2827642f5c08f"
	if got != want {
		t.Fatalf("legacy corpus fingerprint %s (%d inputs)", got, len(converterIndependentCorpus()))
	}
}
