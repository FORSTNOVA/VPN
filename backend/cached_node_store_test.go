package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
)

func TestCachedNodeOwnershipAndQuery(t *testing.T) {
	a := &app{}
	input := make([]proxyNode, 2, 7)
	input[0] = proxyNode{Name: "duplicate", SourceLabel: "原始", Type: "vless", Network: "ws", TLS: true, WsPath: true, Selected: true}
	input[1] = proxyNode{Name: "duplicate", SourceLabel: string([]byte{0xff, 0}), WsHost: true, Alive: true, Manual: true}
	a.replaceCachedNodesLocked(input, true)
	if mode := os.Getenv("SMARTVPN_TEST_NODE_CACHE"); (mode == "rust" && a.nodeCache == nil) || (mode == "go" && a.nodeCache != nil) {
		t.Fatalf("expected %s cache", mode)
	}
	snap := a.cachedNodeSnapshotLocked()
	if !reflect.DeepEqual(snap, input) {
		t.Fatal("node fields or order lost")
	}
	input[1].Name = "edited"
	a.setCachedNodeLabelLocked(0, "changed")
	if input[0].SourceLabel != "changed" || cap(a.borrowCachedNodesLocked()) != 7 {
		t.Fatal("shared backing lost")
	}
	if got := a.cachedNodeSnapshotLocked(); !reflect.DeepEqual(got, input) || snap[1].Name != "duplicate" || snap[0].SourceLabel != "原始" {
		t.Fatal("snapshot or borrowed write-through lost")
	}
	labels := newAppLabelsEffects(a)
	labels.save = func(nodes []proxyNode) error {
		if &nodes[0] != &input[0] || cap(nodes) != cap(input) {
			t.Fatal("cache save lost its borrowed backing")
		}
		return nil
	}
	labels.Call(labelsEffect{Kind: "save-cache"})
	for _, nodes := range [][]proxyNode{nil, {}, input} {
		a.replaceCachedNodesLocked(nodes, false)
		if !reflect.DeepEqual(a.cachedNodeSnapshotLocked(), nodes) {
			t.Fatal("replace changed shape")
		}
	}
}

func TestCachedNodeConcurrentHTTP(t *testing.T) {
	a := &app{}
	batch := func(name string) []proxyNode {
		return []proxyNode{{Name: name, Type: "vless"}, {Name: name, Type: "vmess"}}
	}
	a.replaceCachedNodesLocked(batch("first"), false)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			for i := 0; i < 50; i++ {
				a.mu.Lock()
				a.replaceCachedNodesLocked(batch(name), false)
				a.setCachedNodeLabelLocked(1, name)
				a.mu.Unlock()
			}
		}([]string{"alpha", "beta"}[w])
	}
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 50; i++ {
				out := httptest.NewRecorder()
				a.nodes(out, nil)
				var resp struct{ Nodes []proxyNode }
				if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &resp) != nil || len(resp.Nodes) != 2 {
					t.Errorf("bad response: %s", out.Body.String())
					return
				}
				x, y := resp.Nodes[0], resp.Nodes[1]
				if x.Name != y.Name || x.Type != "vless" || y.Type != "vmess" || (x.Name != "first" && y.SourceLabel != x.Name) {
					t.Errorf("mixed batch: %+v", resp.Nodes)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}
