package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestChainTransactionSettings(t *testing.T) {
	a := &app{home: t.TempDir()}
	for _, name := range []string{"one", "two", "three"} {
		a.appendManualLocked(manualNode{Name: name, Proxy: map[string]any{"name": name, "type": "http", "server": "127.0.0.1", "port": 8000}})
	}
	native := supportsChainTransactions(a.graphLocked())
	if mode := os.Getenv("SMARTVPN_TEST_CHAIN_TRANSACTION"); mode != "" && native != (mode == "rust") {
		t.Fatalf("transaction capability %v for %s", native, mode)
	}
	edit := func(q chainServiceRequest) {
		t.Helper()
		if r := a.runChainService(q); r.Status != 200 {
			t.Fatalf("%s: %+v", q.Op, r)
		}
	}
	first := []string{"one", "two"}
	second := []string{"two", "three"}
	profile := func(old, name string, h []string) chainServiceRequest {
		return chainServiceRequest{Op: "save-profile", Name: name, Original: old, Hops: h}
	}
	active := func(name string, h []string) chainServiceRequest {
		return chainServiceRequest{Op: "update", Next: proxyChain{Name: name, Enabled: true, Hops: h}}
	}
	edit(profile("", "A", first))
	edit(profile("", "B", second))
	edit(active("A", first))
	edit(profile("A", "renamed", second))
	path := filepath.Join(a.home, "settings.json")
	check := func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		s := chainStateFromApp(a)
		if !s.Chain.Enabled || !reflect.DeepEqual(s.Chains[s.Chain.Name], s.Chain.Hops) || a.selectedChoice() != chainExit {
			return errors.New("activity and profile differ")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		reloaded := &app{}
		if err := reloaded.decodeSettingsDocumentLocked(b); err != nil {
			return err
		}
		if !reflect.DeepEqual(reloaded.chainSnapshotLocked(), chainGraphSettings{s.Chain, s.Chains}) {
			return errors.New("disk and query differ")
		}
		return nil
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	q := profile("renamed", "failed", first)
	e := newAppChainEffects(a, q)
	e.save = func() error { return errors.New("injected write failure") }
	result := func() chainServiceResult { defer e.Release(); return runSelectedChainService(q, e) }()
	after, err := os.ReadFile(path)
	if result.Status != 500 || err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed rename changed settings", result.Status, err)
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	edit(active("B", second))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				name := fmt.Sprintf("extra-%d", i)
				old := name
				if j == 0 {
					old = ""
				}
				req := profile(old, name, first)
				if i%2 == 0 {
					req = active("B", second)
					if j%2 == 0 {
						req = active("renamed", second)
					}
				}
				if r := a.runChainService(req); r.Status != 200 {
					t.Error(r)
					return
				}
				if err := check(); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if native {
		owned := chainGraphSettings{Chain: proxyChain{Name: "owned", Enabled: true, Hops: first}, Chains: map[string][]string{"owned": first}}
		a.nodeGraph.call(graphReplaceChains, 0, chainRecord(owned))
		if err := a.saveSettings(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.chainSnapshotLocked(), owned) {
			t.Fatal("stale compatibility view overwrote native settings")
		}
		if err := check(); err != nil {
			t.Fatal(err)
		}
	}
}
