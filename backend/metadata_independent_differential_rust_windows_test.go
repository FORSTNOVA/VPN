//go:build windows && rustcore

package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type metadataIndependentFX struct {
	e     *appMetadataProjectionEffects
	trace []metadataProjectionEffect
}

func (f *metadataIndependentFX) Release() {}
func (f *metadataIndependentFX) Call(q metadataProjectionEffect) metadataProjectionReply {
	f.trace = append(f.trace, q)
	return f.e.Call(q)
}
func metadataIndependentCase(i int) *metadataIndependentFX {
	name := []string{"X", "İ", "\xff", ""}[i%4]
	proxy := map[string]any{"name": name, "tls": i%2 == 0, "network": "ws", "ws-opts": map[string]any{"headers": nil, "path": " "}}
	a := &app{manualNodes: []manualNode{{Name: name, Type: "vmess", Server: "example.invalid", Proxy: proxy}, {Name: name, Type: "last", Server: "192.0.2.1", Proxy: map[string]any{"name": name}}}}
	testEditSettings(a, func(s *settings) {
		s.SubscriptionURL = "synthetic"
		s.ConnectionMode = []string{"tun", "vpn", "system-proxy", " TUN "}[i%4]
	})
	e := &appMetadataProjectionEffects{a: a, manual: a.manualNodes, body: []byte("proxies: [{name: X, type: ss}, {type: ss}]"), readFile: func(string) ([]byte, error) {
		if i%3 == 0 {
			return []byte("proxies: [{name: partial, type: ss}]"), errors.New("synthetic read")
		}
		return []byte("proxies: [{name: X, type: ss}]"), nil
	}, hooks: map[string]func(metadataProjectionEffect) metadataProjectionReply{}}
	if i%5 == 0 {
		e.body = []byte("proxies: [")
	}
	e.hooks["platform"] = func(metadataProjectionEffect) metadataProjectionReply {
		return metadataProjectionReply{Text: []string{"windows", "android"}[i%2]}
	}
	return &metadataIndependentFX{e: e}
}
func TestMetadataActualIndependentMatrix(t *testing.T) {
	if !rustMetadataProjectionAvailable() {
		t.Fatal("metadata native unavailable")
	}
	for _, op := range []string{"details", "domains", "index", "current-index", "mode"} {
		for i := 0; i < 64; i++ {
			t.Run(fmt.Sprintf("%s/%d", op, i), func(t *testing.T) {
				g, r := metadataIndependentCase(i), metadataIndependentCase(i)
				q := metadataProjectionRequest{Op: op}
				gr := runGoMetadataProjection(q, g)
				n := metadataProjectionRecoveries.Load()
				rr, ok := runRustMetadataProjection(q, r)
				if !ok || metadataProjectionRecoveries.Load() != n {
					t.Fatalf("native incomplete %v %v", ok, metadataProjectionRecoveryReason.Load())
				}
				if op == "current-index" && i%3 == 0 && len(r.e.index["partial"]) != 1 {
					t.Fatal("read error discarded partial profile bytes")
				}
				if (op == "index" || op == "current-index") && rr.Err == nil {
					p := r.e.manual[0].Proxy
					name := stringValue(p["name"])
					group := r.e.index[name]
					found := false
					for _, item := range group {
						if reflect.ValueOf(item).Pointer() == reflect.ValueOf(p).Pointer() {
							found = true
						}
					}
					if !found {
						t.Fatal("manual map alias lost")
					}
				}
				if gr.Text != rr.Text || fmt.Sprint(gr.Err) != fmt.Sprint(rr.Err) || !reflect.DeepEqual(g.trace, r.trace) || !reflect.DeepEqual(g.e.details, r.e.details) || !reflect.DeepEqual(g.e.domains, r.e.domains) || !reflect.DeepEqual(g.e.index, r.e.index) {
					t.Fatalf("metadata mismatch %v %v traces%v/%v", gr, rr, g.trace, r.trace)
				}
			})
		}
	}
}

func TestMetadataActualIndependentLegacy(t *testing.T) {
	if !rustMetadataProjectionAvailable() {
		t.Fatal("native metadata unavailable")
	}
	t.Setenv("SMARTVPN_RUST_METADATA_PROJECTION", "1")
	n := metadataProjectionRecoveries.Load()
	TestMetadataIndependentLegacyDetails(t)
	TestMetadataIndependentLegacyIndexAlias(t)
	if metadataProjectionRecoveries.Load() != n {
		t.Fatal("metadata legacy recovered")
	}
}
