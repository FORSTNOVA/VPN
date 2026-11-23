package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// Synthetic payloads only. Never include URI or proxy values in assertion messages.
func TestManualRecordOwnership(t *testing.T) {
	a := &app{home: t.TempDir()}
	g := a.graphLocked()
	mode := os.Getenv("SMARTVPN_TEST_MANUAL_RECORD")
	if mode != "" && supportsFullManual(g) != (mode == "rust") {
		t.Fatalf("full record capability differs from %s", mode)
	}
	uri := "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443?type=ws&path=%2Ffixture&host=node.invalid#one"
	node, err := a.addManualNodeLocked(uri, "")
	if err != nil {
		t.Fatal("add failed", err)
	}
	ws := node.Proxy["ws-opts"].(map[string]any)
	ws["path"] = "/edited"
	node.Proxy["lists"] = []any{float64(1), []string{"a"}, nil, map[string]string{"test": "b"}}
	before := cloneManualNode(node)
	snapshot := a.manualSnapshotLocked()
	if len(snapshot) != 1 || !reflect.DeepEqual(snapshot[0], before) {
		t.Fatal("record lost types or borrowed edit")
	}
	if supportsFullManual(g) {
		full := graphSnapshot(g, graphReadFull).b
		if !bytes.Contains(full, []byte(uri)) || !bytes.Contains(full, []byte("/edited")) || bytes.Contains(graphSnapshot(g, graphReadManual).b, []byte(uri)) {
			t.Fatal("unexpected native record or metadata view")
		}
		snapshot[0].Proxy["ws-opts"].(map[string]any)["path"] = "snapshot-only"
		if !reflect.DeepEqual(a.manualSnapshotLocked()[0], before) {
			t.Fatal("snapshot mutated stored resource")
		}
	}
	if supportsFullManual(g) {
		// Change only the authority. Both the read path and the real provider
		// writer must pick up its URI/proxy pair without a Go mirror update.
		owned := cloneManualNode(before)
		owned.URI += "-native"
		owned.Proxy["ws-opts"].(map[string]any)["path"] = "/native"
		g.call(graphReplaceFull, 0, graphField(registryBool(nil, true), manualRow(owned, true)))
		got := a.manualSnapshotLocked()
		if len(got) != 1 || !reflect.DeepEqual(got[0], owned) {
			t.Fatal("query reread the Go mirror")
		}
		if err := a.writeManualProviderLocked(); err != nil {
			t.Fatal(err)
		}
		y, err := os.ReadFile(a.manualPath())
		want, _ := manualProviderBody(got)
		if err != nil || !bytes.Equal(y, want) {
			t.Fatal("provider reread the Go mirror")
		}
		before = owned
	}

	if err := a.writeManualProviderLocked(); err != nil {
		t.Fatal(err)
	}
	provider, err := os.ReadFile(a.manualPath())
	want, err2 := manualProviderBody([]manualNode{before})
	if err != nil || err2 != nil || !bytes.Equal(want, provider) {
		t.Fatal("provider diverged from query snapshot")
	}
	a.store = &store{} // The failing persistence port is injected below.
	for _, q := range []manualServiceRequest{{Op: "add", URI: uri, Name: "two"}, {Op: "delete", Name: "one"}} {
		e := newAppManualEffects(a, false)
		e.set = func(string) error { return errors.New("fixture save failure") }
		result := runSelectedManualService(q, e)
		after, err := os.ReadFile(a.manualPath())
		if result.Err == nil || err != nil || !bytes.Equal(after, provider) || !reflect.DeepEqual(a.manualSnapshotLocked(), []manualNode{before}) {
			t.Fatal("failed save changed published record")
		}
	}
	a.store = nil
	if err := a.removeManualNodeLocked("one"); err != nil {
		t.Fatal(err)
	}
	if len(a.manualSnapshotLocked()) != 0 {
		t.Fatal("deleted record remained")
	}
	if _, err := os.Stat(filepath.Join(a.home, manualProviderPath)); !os.IsNotExist(err) {
		t.Fatal("empty provider remained")
	}
}

func TestManualRecordConcurrentRead(t *testing.T) {
	a := &app{}
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				a.mu.Lock()
				a.replaceManualLocked([]manualNode{{Name: name, URI: name, Proxy: map[string]any{"name": name, "ws-opts": map[string]any{"path": name}}}})
				a.mu.Unlock()
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 60; j++ {
				a.mu.Lock()
				rows := a.manualSnapshotLocked()
				a.mu.Unlock()
				if len(rows) > 0 {
					n := rows[0]
					if n.Name != n.URI || n.Proxy["name"] != n.Name || n.Proxy["ws-opts"].(map[string]any)["path"] != n.URI {
						t.Error("torn resource snapshot")
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func TestManualRecordReload(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "smartvpn.db")
	db, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{home: home, store: db}
	uri := "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443?type=ws&path=%2Ffixture&host=node.invalid#reload"
	_, err = a.addManualNodeLocked(uri, "")
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatal("save before reopen failed")
	}
	reopened, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b := &app{home: home, store: reopened}
	b.loadManualNodesLocked()
	rows := b.manualSnapshotLocked()
	if len(rows) != 1 || rows[0].URI != uri || rows[0].Proxy["port"] != float64(443) {
		t.Fatal("reopened record differs from Go persistence format")
	}
	details := manualNodeDetails(rows)["reload"]
	if !details.WsPath || !details.WsHost || details.Network != "ws" {
		t.Fatal("reopened query lost transport options")
	}
	if err := b.writeManualProviderLocked(); err != nil {
		t.Fatal(err)
	}
	want, err := manualProviderBody(rows)
	got, readErr := os.ReadFile(b.manualPath())
	if err != nil || readErr != nil || !bytes.Equal(want, got) {
		t.Fatal("reopened provider mismatch")
	}
}
