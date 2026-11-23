package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMetadataIndependentLegacyDetails(t *testing.T) {
	nodes := []manualNode{{Name: "X", Type: "first", Proxy: map[string]any{"tls": true}}, {Name: "X", Type: "last", Proxy: map[string]any{"tls": "true", "network": 3, "ws-opts": map[string]any{"headers": nil, "path": " "}}}, {Name: "", Proxy: nil}}
	d := manualNodeDetails(nodes)
	if len(d) != 2 || d["X"].Type != "last" || d["X"].TLS || d["X"].Network != "" || !d["X"].WsHost || !d["X"].WsPath || !d[""].Manual {
		t.Fatalf("details %#v", d)
	}
	if manualNodeDetails(nil) == nil || manualServerDomains(nil) == nil {
		t.Fatal("empty shape")
	}
	domains := manualServerDomains([]manualNode{{Server: "Example.invalid"}, {Server: "Example.invalid"}, {Server: "192.0.2.1"}})
	if !reflect.DeepEqual(domains, []string{"+.example.invalid", "+.example.invalid"}) {
		t.Fatalf("domains %#v", domains)
	}
}
func TestMetadataIndependentLegacyIndexAlias(t *testing.T) {
	nested := map[string]any{"x": "old"}
	p := map[string]any{"name": "N", "extra": nested}
	out, err := proxyIndex([]byte("proxies: [{name: N, type: ss}, {type: ss}]"), []manualNode{{Proxy: p}, {Proxy: nil}})
	if err != nil || len(out["N"]) != 2 || len(out[""]) != 2 {
		t.Fatalf("index %#v %v", out, err)
	}
	out["N"][1]["name"] = "changed"
	nested["x"] = "new"
	if p["name"] != "changed" || out["N"][1]["extra"].(map[string]any)["x"] != "new" {
		t.Fatal("resource alias lost")
	}
	if got, e := proxyIndex([]byte("proxies: ["), []manualNode{{Proxy: p}}); e == nil || got != nil {
		t.Fatal("malformed must precede manual append")
	}
}

func TestMetadataIndependentLegacyCachedVMess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.txt")
	if got := readCachedVMessDetails(path); got == nil || len(got) != 0 {
		t.Fatal("read error empty map")
	}
	first := base64.StdEncoding.EncodeToString([]byte(`{"ps":"same","net":"ws","tls":"tls"}`))
	last := base64.RawStdEncoding.EncodeToString([]byte(`{"ps":"same","net":"grpc","host":"h"}`))
	skip := base64.StdEncoding.EncodeToString([]byte(`{"ps":"skipped","net":"tcp"}`))
	text := "vmess://" + first + "\n vmess://" + skip + "\nVMESS://" + skip + "\nvmess://" + last + "\nvmess://invalid"
	for _, body := range []string{text, base64.StdEncoding.EncodeToString([]byte(text))} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		d := readCachedVMessDetails(path)
		if len(d) != 1 || d["same"].Net != "grpc" || d["same"].Host != "h" {
			t.Fatalf("cached exact lines/lastwins %#v", d)
		}
	}
	if err := os.WriteFile(path, []byte("proxies: []"), 0600); err != nil {
		t.Fatal(err)
	}
	if d := readCachedVMessDetails(path); d == nil || len(d) != 0 {
		t.Fatal("nonURI shape")
	}
}
