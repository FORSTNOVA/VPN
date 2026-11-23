package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSubscriptionNodeMetadataParity(t *testing.T) {
	body := []byte("proxies: [{name: n, type: http, tls: true, network: ws, ws-opts: {headers: {Host: h}, path: /}}, {name: n, type: ss}, {name: other, type: ss}]\nsmartvpn-source-labels: {n: first, other: second}\n")
	nodes, ok := nativeQueryNodes(body)
	mode := os.Getenv("SMARTVPN_TEST_NODE_METADATA")
	assertMode := func(ok bool) {
		t.Helper()
		if mode != "" && ok != (mode == "rust") {
			t.Fatalf("native=%t mode=%s", ok, mode)
		}
	}
	assertMode(ok)
	if want := yamlProfileNodesGo(body); !reflect.DeepEqual(parseProfileNodes(body), want) || ok && !reflect.DeepEqual(nodes, want) {
		t.Fatal("duplicate summaries, flags, or labels differ", nodes, want)
	}
	idx, err := proxyIndex(body, []manualNode{{Proxy: map[string]any{"name": "n", "type": "manual"}}})
	if err != nil || len(idx["n"]) != 3 || idx["n"][1]["type"] != "ss" || idx["n"][2]["type"] != "manual" {
		t.Fatal("duplicate index order", err, idx)
	}
	doc, ok := nativeLabelDocument(body)
	assertMode(ok)
	var want map[string]any
	if err := yaml.Unmarshal(body, &want); err != nil || ok && !reflect.DeepEqual(doc, want) {
		t.Fatal("label repair document differs")
	}
	manual := []manualNode{{Name: "n", Type: "first"}, {Name: "n", Type: "last", Proxy: map[string]any{"network": "ws", "ws-opts": map[string]any{"path": " ", "headers": nil}}}}
	details, ok := nativeManualDetails(manual)
	assertMode(ok)
	if m := manualNodeDetails(manual)["n"]; m.Type != "last" || !m.WsHost || !m.WsPath || !m.Manual || ok && details["n"] != m {
		t.Fatal("manual overlay differs", m)
	}
	list := []proxyNode{{Name: "n", Network: "ws"}, {Name: "n", SourceLabel: "original"}, {Name: "m", Manual: true, Selected: true}}
	selected := "n"
	_, ok = nativeNodeAnnotations(list, &selected)
	assertMode(ok)
	annotateProfileNodes(list, "first", &selected)
	if list[0].SourceLabel != "first" || !list[0].Selected || list[1].SourceLabel != "original" || !list[1].Selected || list[2].Selected || list[2].SourceLabel != "" {
		t.Fatal(list)
	}
	for _, s := range []string{"proxies: [{name: 010, type: http}]", "trojan://fixture@127.0.0.1:8443#n", `{"outbounds":[{"tag":"n","type":"trojan"}],"proxies":[{"name":"x","type":"http"}]}`, "proxies: [{name: n, type: http}]\nsmartvpn-source-labels: {n: 001}"} {
		b := []byte(s)
		if !reflect.DeepEqual(parseProfileNodes(b), parseProfileNodesViaConverter(b)) {
			t.Fatalf("converter precedence changed for %q", s)
		}
	}
}

func TestSubscriptionNodeMetadataRefreshRestart(t *testing.T) {
	home := t.TempDir()
	openApp := func() *app {
		a := &app{home: home}
		before := a.settingsLocked()
		next := before
		next.SubscriptionURL = mergedSubscriptionURL
		next.SelectedNode = "same (2)"
		a.applySettingsControlDiffLocked(before, next)
		return a
	}
	query := func(a *app) []proxyNode {
		t.Helper()
		r := httptest.NewRecorder()
		a.nodes(r, httptest.NewRequest("GET", "/nodes", nil))
		var result struct{ Nodes []proxyNode }
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 {
			t.Fatalf("nodes query: %v %d", err, r.Code)
		}
		return result.Nodes
	}
	a := openApp()
	for _, second := range []string{"second", "refreshed"} {
		sources := []SubscriptionSource{{Label: "first", Body: []byte("proxies: [{name: same, type: http, server: 127.0.0.1, port: 9, network: ws, tls: true}, {name: same, type: http, server: 127.0.0.1, port: 11, network: grpc}, {name: tail, type: socks5, server: 127.0.0.1, port: 12}]")}, {Label: second, Body: []byte("proxies: [{name: same, type: socks5, server: 127.0.0.1, port: 10}]")}}
		body, nodes, err := mergeSubscriptionSourcesForCache(sources)
		if err != nil {
			t.Fatal(err)
		}
		want := yamlProfileNodesGo(body)
		if !reflect.DeepEqual(nodes, want) || len(nodes) != 4 || nodes[0].SourceLabel != "first" || nodes[1].Network != "grpc" || nodes[1].Name != "same (2)" || nodes[2].Name != "tail" || nodes[3].SourceLabel != second || nodes[3].Name != "same (3)" {
			t.Fatal("merge metadata", nodes, want)
		}
		a.replaceCachedNodesLocked(nodes, false)
		if err := a.saveCachedNodesLocked(nodes); err != nil {
			t.Fatal(err)
		}
		want[1].Selected = true
		if got := query(a); !reflect.DeepEqual(got, want) {
			t.Fatal("refresh", got, want)
		}
		reopened := openApp()
		reopened.loadCachedNodes()
		if got := query(reopened); !reflect.DeepEqual(got, want) {
			t.Fatal("reopened", got, want)
		}
	}
}
