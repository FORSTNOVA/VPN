package main

import (
	"net/http/httptest"
	"testing"
)

func migrationOriginalLiveNodeMerge(selected string, details map[string]proxyNode, providers map[string]map[string]any) []proxyNode {
	nodes := make([]proxyNode, 0, 64)
	seen := map[string]bool{}
	for _, providerName := range []string{"SmartVPNSubscription", manualProviderName} {
		provider, ok := providers[providerName]
		if !ok {
			continue
		}
		rawNodes, _ := provider["proxies"].([]any)
		for _, raw := range rawNodes {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := item["name"].(string)
			if name == "" || seen[name] || isInformationalNodeGo(name) {
				continue
			}
			seen[name] = true
			kind, _ := item["type"].(string)
			alive, _ := item["alive"].(bool)
			extra := details[name]
			network := extra.Network
			if network == "" {
				network, _ = item["network"].(string)
			}
			nodes = append(nodes, proxyNode{Name: name, Type: kind, Network: network, TLS: extra.TLS, SourceLabel: extra.SourceLabel, WsHost: extra.WsHost, WsPath: extra.WsPath, Alive: alive, Selected: name == selected, Manual: extra.Manual})
		}
	}
	return nodes
}
func TestMigrationNodeQuerySafeStoppedHTTP(t *testing.T) {
	for _, op := range []string{"nodes", "latency", "node-latency"} {
		a := &app{}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/synthetic", nil)
		switch op {
		case "nodes":
			a.nodes(w, r)
		case "latency":
			a.latency(w, r)
		case "node-latency":
			a.nodeLatency(w, r)
		}
		want := 409
		if op == "nodes" {
			want = 200
		}
		if w.Code != want {
			t.Fatal(op, w.Code, want)
		}
		if !a.mu.TryLock() {
			t.Fatal("HTTP lock leak")
		}
		a.mu.Unlock()
	}
}
