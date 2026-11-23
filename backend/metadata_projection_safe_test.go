package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestMetadataProjectionServiceGraphIdentity(t *testing.T) {
	nested := map[string]any{"marker": "old"}
	manual := map[string]any{"name": "dup", "nested": nested}
	idx, err := proxyIndex([]byte("proxies:\n- {name: dup, type: ss}\n- {name: dup, type: vmess}\n"), []manualNode{{Proxy: manual}, {Proxy: nil}})
	if err != nil || len(idx["dup"]) != 3 || len(idx[""]) != 1 || idx[""][0] != nil {
		t.Fatalf("index shape %v", err)
	}
	if reflect.ValueOf(idx["dup"][2]).Pointer() != reflect.ValueOf(manual).Pointer() {
		t.Fatal("manual proxy cloned")
	}
	idx["dup"][2]["new"] = true
	if manual["new"] != true {
		t.Fatal("map identity lost")
	}
	idx["dup"][2]["nested"].(map[string]any)["marker"] = "new"
	if nested["marker"] != "new" {
		t.Fatal("nested alias lost")
	}
	bad, err := proxyIndex([]byte("proxies: ["), []manualNode{{Proxy: manual}})
	if err == nil || bad != nil {
		t.Fatal("decode failure must return nil before manual")
	}
}
func TestMetadataProjectionServiceDetailsExactTypesAndLastWins(t *testing.T) {
	nodes := []manualNode{{Name: "same", Type: "first", Proxy: map[string]any{"tls": true}}, {Name: "same", Type: "second", Proxy: map[string]any{"name": "wrong", "type": "wrong", "network": 12, "tls": "true", "ws-opts": map[string]any{"path": " ", "headers": nil}}}, {Name: string([]byte{255}), Proxy: nil}}
	d := manualNodeDetails(nodes)
	if len(d) != 2 || d["same"].Type != "second" || d["same"].TLS || d["same"].Network != "" || !d["same"].WsHost || !d["same"].WsPath || !d["same"].Manual {
		t.Fatal("metadata changed")
	}
	if _, ok := d[string([]byte{255})]; !ok {
		t.Fatal("raw byte key lost")
	}
	if manualNodeDetails(nil) == nil {
		t.Fatal("nil empty map")
	}
}
func TestMetadataProjectionServiceDomainsOrderedNotDeduplicated(t *testing.T) {
	nodes := []manualNode{{Server: "a.example.com"}, {Server: "127.0.0.1"}, {Server: "b.example.com"}}
	got := manualServerDomains(nodes)
	if !reflect.DeepEqual(got, []string{"+.example.com", "+.example.com"}) {
		t.Fatalf("domains %v", got)
	}
	if manualServerDomains(nil) == nil {
		t.Fatal("nil empty slice")
	}
}
func TestMetadataProjectionServiceCurrentPartialReadThenSnapshot(t *testing.T) {
	f := &appMetadataProjectionEffects{hooks: map[string]func(metadataProjectionEffect) metadataProjectionReply{}}
	trace := []string{}
	f.hooks["subscription"] = func(metadataProjectionEffect) metadataProjectionReply {
		trace = append(trace, "subscription")
		return metadataProjectionReply{Text: " "}
	}
	f.hooks["read-profile"] = func(metadataProjectionEffect) metadataProjectionReply {
		trace = append(trace, "read")
		f.body = []byte("proxies: [{name: profile}]")
		return metadataProjectionReply{Err: errors.New("partial read")}
	}
	f.hooks["manual-snapshot"] = func(metadataProjectionEffect) metadataProjectionReply {
		trace = append(trace, "snapshot")
		f.manual = []manualNode{{Proxy: map[string]any{"name": "manual"}}}
		return metadataProjectionReply{}
	}
	r := runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "current-index"}, f)
	if r.Err != nil || !reflect.DeepEqual(trace, []string{"subscription", "read", "snapshot"}) || len(f.index) != 2 {
		t.Fatalf("changed partial read %v %v", trace, r.Err)
	}
}
func TestMetadataProjectionServiceAuthorityPanicDoesNotRead(t *testing.T) {
	calls := 0
	f := &appMetadataProjectionEffects{hooks: map[string]func(metadataProjectionEffect) metadataProjectionReply{"subscription": func(metadataProjectionEffect) metadataProjectionReply { panic("closed authority") }, "read-profile": func(metadataProjectionEffect) metadataProjectionReply { calls++; return metadataProjectionReply{} }}}
	defer func() {
		if recover() == nil || calls != 0 {
			t.Fatal("authority failure became empty subscription")
		}
	}()
	runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "current-index"}, f)
}
func TestMetadataProjectionServiceModeDoesNotTrim(t *testing.T) {
	for _, v := range []struct{ platform, mode, want string }{{"windows", "tun", "tun"}, {"windows", " tun ", "system-proxy"}, {"android", "system-proxy", "vpn"}, {"windows", string([]byte{255}), "system-proxy"}} {
		reads := 0
		f := &appMetadataProjectionEffects{hooks: map[string]func(metadataProjectionEffect) metadataProjectionReply{"mode-read": func(metadataProjectionEffect) metadataProjectionReply {
			reads++
			return metadataProjectionReply{Text: v.mode}
		}, "platform": func(metadataProjectionEffect) metadataProjectionReply {
			return metadataProjectionReply{Text: v.platform}
		}}}
		r := runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "mode"}, f)
		if r.Err != nil || r.Text != v.want || reads != 1 {
			t.Fatal("platform mode changed")
		}
	}
}
func TestMetadataProjectionServiceLargeGraphNoArtificialLimit(t *testing.T) {
	nodes := make([]manualNode, 60001)
	for i := range nodes {
		nodes[i] = manualNode{Name: "same", Type: "last", Proxy: nil}
	}
	got := manualNodeDetails(nodes)
	if len(got) != 1 || got["same"].Type != "last" {
		t.Fatal("large graph truncated")
	}

}
