package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNodeFrontServiceTransportPreservesOriginalGraph(t *testing.T) {
	for _, kind := range []string{"tcp", "ws", "websocket", "grpc", "h2", "http", "unsupported"} {
		old := map[string]any{"keep": "original"}
		p := map[string]any{"ws-opts": old, "grpc-opts": old, "h2-opts": old}
		err := transportOptions(p, kind, "", "", "")
		if kind == "unsupported" {
			if err == nil {
				t.Fatal("missing transport error")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"ws-opts", "grpc-opts", "h2-opts"} {
			if reflect.ValueOf(p[key]).Pointer() != reflect.ValueOf(old).Pointer() {
				t.Fatal("empty options replaced original graph")
			}
		}
	}
	p := map[string]any{}
	if err := transportOptions(p, " ws ", " /raw ", "host", "service"); err != nil {
		t.Fatal(err)
	}
	opts := p["ws-opts"].(map[string]any)
	if opts["path"] != " /raw " || opts["headers"].(map[string]any)["Host"] != "host" {
		t.Fatal("transport values normalized unexpectedly")
	}
}
func TestNodeFrontServiceFinishKeepsProxyAlias(t *testing.T) {
	p := map[string]any{"name": "old", "unknown": []any{"retained"}}
	n, err := finishManualNode("synthetic", "  ", "test", "host", 123, p)
	if err != nil || n.Name != "host:123" || p["name"] != n.Name || reflect.ValueOf(n.Proxy).Pointer() != reflect.ValueOf(p).Pointer() {
		t.Fatal("finish alias/name changed")
	}
}
func TestNodeFrontServiceManualVmessDialectAndPrivateWire(t *testing.T) {
	raw := `{"add":" node.example ","port":443,"id":" synthetic-id ","ps":" ","net":"ws","path":"/x","host":"sni.example","tls":"tls","fp":" chrome ","alpn":["h2"," http/1.1 "]}`
	uri := "vmess://" + base64.StdEncoding.EncodeToString([]byte(raw)) + "#ignored?ignored"
	n, err := parseVmessLinkWithFields(uri, false)
	if err != nil || n.Name != "node.example:443" || n.Proxy["client-fingerprint"] != "chrome" || n.Proxy["network"] != "ws" || !reflect.DeepEqual(n.Proxy["alpn"], []string{"h2", "http/1.1"}) {
		t.Fatalf("manual dialect changed: %v", err)
	}
	wire := nodeFrontInput(nodeFrontRequest{Op: "dispatch", Path: uri})
	if bytes.Contains(wire, []byte(uri)) || bytes.Contains(wire, []byte("synthetic-id")) {
		t.Fatal("raw credential input escaped ledger")
	}
	bad := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"x","port":443}`))
	_, err = parseVmessLinkWithFields(bad, false)
	if !errors.Is(err, errLinkFields) {
		t.Fatal("manual missing UUID accepted")
	}
	_, err = parseNodeURI("VMESS://" + strings.TrimPrefix(uri, "vmess://"))
	if err == nil {
		t.Fatal("case-sensitive manual base64 prefix quirk changed")
	}
}
func TestNodeFrontServiceYamlProjectionNilAndFirstWins(t *testing.T) {
	if yamlProfileNodes([]byte("proxies: []")) != nil {
		t.Fatal("empty source must nil")
	}
	body := []byte("proxies:\n - {name: n, type: ss, network: ws, tls: 'False', ws-opts: {path: ' ', headers: {hOsT: ' x '}}}\n - {name: n, type: vmess}\n - {name: invalid}\nsmartvpn-source-labels: {n: label}\n")
	got := yamlProfileNodes(body)
	if len(got) != 1 || got[0].Type != "ss" || got[0].SourceLabel != "label" || !got[0].TLS || !got[0].WsHost || !got[0].WsPath {
		t.Fatal("projection/dedup changed")
	}
	if got := yamlProfileNodes([]byte("proxies: [{name: no-type}]")); got == nil || len(got) != 0 {
		t.Fatal("all-filtered output must nonnil empty")
	}
	if uniqueNodes(nil) == nil {
		t.Fatal("unique nil input becomes empty nonnil")
	}
}

func TestNodeFrontServiceCodecErrorsStayOpaqueOnWire(t *testing.T) {
	secret := "https://synthetic:credential@invalid/%zz"
	original := errors.New(secret)
	wire := nodeFrontReplyBytes(nodeFrontWireReply("url-parse", nodeFrontReply{Err: original}))
	if bytes.Contains(wire, []byte(secret)) || bytes.Contains(wire, []byte("credential")) {
		t.Fatal("codec error leaked raw URI into native input")
	}
}
