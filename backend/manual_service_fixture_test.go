package main

import (
	"os"
	"reflect"
	"testing"
)

type manualDiffSpec struct {
	Nodes, Stored         []manualNode
	Cached                []proxyNode
	Parsed                manualNode
	Store, Running, Found bool
	Now                   int64
	Fail                  map[string]error
}
type manualDiffEvent struct {
	Effect          manualEffect
	Nodes           []manualNode
	Store           string
	Files           map[string]string
	Owned, Borrowed bool
}
type manualDiffFX struct {
	Base       *appManualEffects
	Trace      []manualDiffEvent
	Calls      map[string]int
	StoredBody string
	Files      map[string]string
	Spec       manualDiffSpec
}

func manualDiffBase() manualDiffSpec {
	n := manualNode{Name: "new", URI: "synthetic-uri", Type: "trojan", Server: "test.invalid", Port: 443, Proxy: map[string]any{"name": "new", "type": "trojan", "server": "test.invalid", "port": 443, "nested": map[string]any{"values": []any{"one", int64(9007199254740993)}}}}
	old := cloneManualNode(n)
	old.Name = "old"
	old.URI = "legacy-not-parseable"
	old.Proxy["name"] = "wrong-old-name"
	return manualDiffSpec{Nodes: []manualNode{old}, Stored: []manualNode{old}, Parsed: n, Store: true, Found: true, Now: 9007199254740993, Fail: map[string]error{}}
}
func manualDiffNew(s manualDiffSpec, borrowed bool) *manualDiffFX {
	a := &app{home: "synthetic-manual-home", manualNodes: cloneManualNodes(s.Nodes), cachedNodes: append([]proxyNode(nil), s.Cached...)}
	if s.Store {
		a.store = &store{}
	}
	if borrowed {
		a.mu.Lock()
	}
	f := &manualDiffFX{Base: newAppManualEffects(a, borrowed), Calls: map[string]int{}, Files: map[string]string{}, Spec: s, StoredBody: "old-store"}
	f.Base.now = func() int64 { return s.Now }
	f.Base.running = func() bool { return s.Running }
	f.Base.get = func() (string, bool, error) { return "synthetic-loaded-body", s.Found, s.Fail["get"] }
	f.Base.set = func(v string) error {
		if e := s.Fail["set"]; e != nil {
			return e
		}
		f.StoredBody = v
		return nil
	}
	f.Base.parse = func(string) (manualNode, error) { return cloneManualNode(s.Parsed), s.Fail["parse"] }
	f.Base.decode = func([]byte) ([]manualNode, error) { return cloneManualNodes(s.Stored), s.Fail["decode"] }
	encode := f.Base.encode
	f.Base.encode = func(ns []manualNode) ([]byte, error) {
		if e := s.Fail["encode"]; e != nil {
			return nil, e
		}
		return encode(ns)
	}
	render := f.Base.yaml
	f.Base.yaml = func(ns []manualNode) ([]byte, error) {
		if e := s.Fail["yaml"]; e != nil {
			return nil, e
		}
		return render(ns)
	}
	f.Base.remove = func(p string) error {
		if e := s.Fail["remove"]; e != nil {
			return e
		}
		if _, ok := f.Files[p]; !ok {
			return os.ErrNotExist
		}
		delete(f.Files, p)
		return nil
	}
	f.Base.mkdir = func(_ string, mode os.FileMode) error {
		if mode != 0700 {
			panic("mkdir mode changed")
		}
		return s.Fail["mkdir"]
	}
	f.Base.write = func(p string, b []byte, mode os.FileMode) error {
		if mode != 0600 {
			panic("file mode changed")
		}
		if e := s.Fail["write"]; e != nil {
			return e
		}
		f.Files[p] = string(b)
		return nil
	}
	f.Base.rename = func(from, to string) error {
		if e := s.Fail["rename"]; e != nil {
			return e
		}
		body, ok := f.Files[from]
		if !ok {
			return os.ErrNotExist
		}
		f.Files[to] = body
		delete(f.Files, from)
		return nil
	}
	f.Base.logger = func(string) {}
	return f
}
func (f *manualDiffFX) capture(e manualEffect) {
	e.Node = cloneManualNode(e.Node)
	e.Nodes = cloneManualNodes(e.Nodes)
	e.Body = append([]byte(nil), e.Body...)
	files := map[string]string{}
	for k, v := range f.Files {
		files[k] = v
	}
	f.Trace = append(f.Trace, manualDiffEvent{e, cloneManualNodes(f.Base.a.manualNodes), f.StoredBody, files, f.Base.owned, f.Base.borrowed})
}
func (f *manualDiffFX) Call(e manualEffect) manualReply {
	f.Calls[e.Kind]++
	snapshot := e
	snapshot.Node = cloneManualNode(e.Node)
	snapshot.Nodes = cloneManualNodes(e.Nodes)
	snapshot.Body = append([]byte(nil), e.Body...)
	if e.Indices != nil {
		snapshot.Indices = append([]int{}, e.Indices...)
	}
	r := f.Base.Call(e)
	f.capture(snapshot)
	return r
}
func (f *manualDiffFX) Release() {
	owned := f.Base.owned
	f.Base.Release()
	if owned {
		f.capture(manualEffect{Kind: "release"})
	}
}
func TestMigrationManualSafeSelectedLifecycle(t *testing.T) {
	type out struct {
		Results []manualServiceResult
		Trace   []manualDiffEvent
		Nodes   []manualNode
		Files   map[string]string
		Stored  string
	}
	execute := func(selected bool) out {
		s := manualDiffBase()
		s.Nodes = nil
		s.Stored = nil
		f := manualDiffNew(s, false)
		o := out{}
		run := func(q manualServiceRequest) {
			var r manualServiceResult
			if selected {
				r = runSelectedManualService(q, f)
			} else {
				r = runGoManualService(q, f)
			}
			if r.Err != nil {
				t.Fatalf("successful lifecycle failed op=%s err=%v", q.Op, r.Err)
			}
			o.Results = append(o.Results, r)
		}
		run(manualServiceRequest{Op: "preview", URI: "synthetic", Name: " preview "})
		if len(f.Base.a.manualNodes) != 0 {
			t.Fatal("preview mutated state")
		}
		run(manualServiceRequest{Op: "add", URI: "synthetic", Name: " kept "})
		run(manualServiceRequest{Op: "list"})
		if len(f.Base.a.manualNodes) != 1 || f.Base.a.manualNodes[0].AddedAt != s.Now {
			t.Fatal("add incomplete")
		}
		if len(f.Files) != 1 {
			t.Fatal("provider not installed")
		}
		f.Base.a.mu.Lock()
		f.Base.borrowed = true
		run(manualServiceRequest{Op: "save", Borrowed: true})
		if f.Base.a.mu.TryLock() {
			f.Base.a.mu.Unlock()
			t.Fatal("save released borrowed mutex")
		}
		f.Base.a.mu.Unlock()
		f.Base.borrowed = false
		f.Base.get = func() (string, bool, error) { return f.StoredBody, true, nil }
		f.Base.decode = newAppManualEffects(f.Base.a, false).decode
		run(manualServiceRequest{Op: "load"})
		if len(f.Base.a.manualNodes) != 1 || f.Base.a.manualNodes[0].Name != "kept" {
			t.Fatal("stored lifecycle did not reload")
		}
		run(manualServiceRequest{Op: "delete", Name: " kept "})
		if f.Base.a.manualNodes == nil || len(f.Base.a.manualNodes) != 0 || len(f.Files) != 0 {
			t.Fatal("delete did not leave nonnil empty list/removeprovider")
		}
		o.Trace = f.Trace
		o.Nodes = f.Base.a.manualNodes
		o.Files = f.Files
		o.Stored = f.StoredBody
		if !f.Base.a.mu.TryLock() {
			t.Fatal("selector leaked lock")
		}
		f.Base.a.mu.Unlock()
		return o
	}
	want, got := execute(false), execute(true)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("selected lifecycle mismatch\nGo=%+v\nSelected=%+v", want, got)
	}
}

func manualLegacyIdentityContracts(t *testing.T, run func(manualServiceRequest, manualServiceEffects) manualServiceResult) {
	t.Helper()
	t.Run("add-result-and-shared-nested-map", func(t *testing.T) {
		shared := map[string]any{"value": "initial"}
		parsedProxy := map[string]any{"name": "parsed", "nested": shared}
		oldProxy := map[string]any{"name": "old", "nested": shared}
		a := &app{manualNodes: []manualNode{{Name: "old", URI: "old", Proxy: oldProxy}}}
		f := safeManualFX(a, false)
		f.parse = func(string) (manualNode, error) {
			return manualNode{Name: "parsed", URI: "synthetic", Proxy: parsedProxy}, nil
		}
		r := run(manualServiceRequest{Op: "add", Name: "renamed"}, f)
		if r.Err != nil || len(a.manualNodes) != 2 {
			t.Fatal(r.Err)
		}
		f.added.Proxy["result-write"] = "seen"
		if a.manualNodes[1].Proxy["result-write"] != "seen" || parsedProxy["result-write"] != "seen" {
			t.Fatal("prepared/parser/app Proxy identity changed")
		}
		f.added.Proxy["nested"].(map[string]any)["value"] = "updated"
		if shared["value"] != "updated" || a.manualNodes[0].Proxy["nested"].(map[string]any)["value"] != "updated" {
			t.Fatal("shared nested proxy graph was copied")
		}
		if parsedProxy["name"] != "renamed" {
			t.Fatal("rename failed to mutate parsed Proxy")
		}
	})
	t.Run("delete-kept-map-identity", func(t *testing.T) {
		kept := map[string]any{"name": "keep"}
		a := &app{manualNodes: []manualNode{{Name: "remove", Proxy: map[string]any{"name": "remove"}}, {Name: "keep", Proxy: kept}}}
		f := safeManualFX(a, false)
		r := run(manualServiceRequest{Op: "delete", Name: "remove"}, f)
		if r.Err != nil || len(a.manualNodes) != 1 {
			t.Fatal(r.Err)
		}
		kept["external"] = "visible"
		if a.manualNodes[0].Proxy["external"] != "visible" {
			t.Fatal("delete copied kept Proxy")
		}
	})
	t.Run("delete-save-failure-restores-original-slice", func(t *testing.T) {
		nodes := make([]manualNode, 2, 5)
		nodes[0] = manualNode{Name: "remove", Proxy: map[string]any{"name": "remove"}}
		nodes[1] = manualNode{Name: "keep", Proxy: map[string]any{"name": "keep"}}
		original := &nodes[0]
		a := &app{manualNodes: nodes, store: &store{}}
		f := safeManualFX(a, false)
		f.set = func(string) error { return os.ErrPermission }
		r := run(manualServiceRequest{Op: "delete", Name: "remove"}, f)
		if r.Err == nil || len(a.manualNodes) != 2 || cap(a.manualNodes) != 5 || &a.manualNodes[0] != original {
			t.Fatal("rollback failed to restore original slice backing/capacity")
		}
	})
	t.Run("load-preserves-shared-map-graph", func(t *testing.T) {
		shared := map[string]any{"name": "old"}
		stored := []manualNode{{Name: "a", URI: "a", Proxy: shared}, {Name: "b", URI: "b", Proxy: shared}}
		a := &app{store: &store{}}
		f := safeManualFX(a, false)
		f.get = func() (string, bool, error) { return "synthetic", true, nil }
		f.decode = func([]byte) ([]manualNode, error) { return stored, nil }
		r := run(manualServiceRequest{Op: "load"}, f)
		if r.Err != nil || len(a.manualNodes) != 2 {
			t.Fatal(r.Err)
		}
		if shared["name"] != "b" || a.manualNodes[0].Proxy["name"] != "b" {
			t.Fatal("load copied decoder-owned shared graph")
		}
		shared["after"] = "visible"
		if a.manualNodes[1].Proxy["after"] != "visible" {
			t.Fatal("load map identity changed")
		}
	})
}
func TestMigrationManualSafeLegacyIdentity(t *testing.T) {
	manualLegacyIdentityContracts(t, runGoManualService)
}
