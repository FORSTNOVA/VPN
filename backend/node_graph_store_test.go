package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestNodeGraphQueriesAndUpdates(t *testing.T) {
	a := &app{home: t.TempDir()}
	send := func(fn http.HandlerFunc, method, path, body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	for _, name := range []string{"first", "second"} {
		send(a.addManualNode, "POST", "/api/nodes/manual", `{"uri":"trojan://fixture@fixture.invalid:443#`+name+`"}`)
	}
	switch os.Getenv("SMARTVPN_TEST_NODE_GRAPH") {
	case "rust":
		if a.nodeGraph == nil {
			t.Fatal("Rust graph not attached")
		}
		if bytes.Contains(graphSnapshot(a.nodeGraph, graphReadManual).b, []byte("trojan://")) {
			t.Fatal("manual snapshot copied a credential-bearing URI")
		}
	case "go":
		if a.nodeGraph != nil {
			t.Fatal("expected compatibility path")
		}
	}
	checkManual := func(want ...string) {
		t.Helper()
		body := send(a.listManualNodes, "GET", "/api/nodes/manual", "")
		if strings.Contains(body, "trojan://") {
			t.Fatal("URI leaked by list")
		}
		var got struct{ Nodes []struct{ Name string } }
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, n := range got.Nodes {
			names = append(names, n.Name)
		}
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("names %v", names)
		}
	}
	checkManual("first", "second")
	send(a.saveChainProfile, "POST", "/api/chains", `{"name":"old","hops":["first","second"]}`)
	send(a.updateChain, "PUT", "/api/chain", `{"name":"old","enabled":true,"hops":["first","second"]}`)
	send(a.saveChainProfile, "POST", "/api/chains", `{"original":"old","name":"new","hops":["second","first"]}`)
	var state struct {
		Chain  proxyChain
		Chains map[string][]string
	}
	if err := json.Unmarshal([]byte(send(a.state, "GET", "/api/state", "")), &state); err != nil {
		t.Fatal(err)
	}
	want := []string{"second", "first"}
	if state.Chain.Name != "new" || !state.Chain.Enabled || !reflect.DeepEqual(state.Chain.Hops, want) || !reflect.DeepEqual(state.Chains, map[string][]string{"new": want}) {
		t.Fatalf("rename %+v", state)
	}
	// A rename of the active profile changes two resources before saving.
	// Force that save to fail, then read both back via the normal query path.
	home := a.home
	a.home = filepath.Join(home, "missing")
	oldHops := a.settings.Chain.Hops
	failed := a.runChainService(chainServiceRequest{Op: "save-profile", Original: "new", Name: "failed", Hops: []string{"first", "second"}})
	a.home = home
	after := a.chainSnapshotLocked()
	if failed.Status != 500 || &a.settings.Chain.Hops[0] != &oldHops[0] ||
		!reflect.DeepEqual(after.Chain, state.Chain) || !reflect.DeepEqual(after.Chains, state.Chains) {
		t.Fatal("failed save lost the original active or named chain")
	}
	a.settings.Chain.Hops[0] = "borrowed"
	before := a.chainSnapshotLocked()
	if before.Chain.Hops[0] != "borrowed" || before.Chains["new"][0] != "borrowed" {
		t.Fatal("hop write-through lost")
	}
	before.Chain.Hops[0] = "detached"
	if a.settings.Chain.Hops[0] != "borrowed" {
		t.Fatal("state snapshot shares backing")
	}
	send(a.deleteManualNode, "DELETE", "/api/nodes/manual?name=first", "")
	checkManual("second")
	body := send(a.nodes, "GET", "/api/nodes", "")
	if !strings.Contains(body, `"second"`) || strings.Contains(body, `"first"`) {
		t.Fatal("combined node query did not see delete")
	}
}

func TestNodeGraphConcurrentSnapshots(t *testing.T) {
	a := &app{}
	var wg sync.WaitGroup
	for _, name := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				a.mu.Lock()
				a.replaceManualLocked([]manualNode{{Name: name, Proxy: map[string]any{"name": name}}})
				a.setActiveChainLocked(proxyChain{Name: name, Enabled: true, Hops: []string{name, name}})
				a.mu.Unlock()
			}
		}(name)
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.mu.Lock()
				ns := a.manualSnapshotLocked()
				chain := a.chainSnapshotLocked().Chain
				a.mu.Unlock()
				if len(ns) != 0 && (len(chain.Hops) != 2 || ns[0].Name != chain.Name || ns[0].Proxy["name"] != chain.Hops[1]) {
					t.Error("mixed graph generations")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestNodeGraphRejectsTruncatedField(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("accepted an incomplete length prefix")
		}
	}()
	decodeGraphHops([]byte{1, 3})
}
