package main

import (
	"errors"
	"math"
	"os"
	"reflect"
	"testing"
)

func safeManualFX(a *app, borrowed bool) *appManualEffects {
	e := newAppManualEffects(a, borrowed)
	e.now = func() int64 { return 1 << 40 }
	e.running = func() bool { return false }
	e.get = func() (string, bool, error) { return "", false, nil }
	e.set = func(string) error { return nil }
	e.parse = func(string) (manualNode, error) {
		return manualNode{Name: "parsed", URI: "synthetic", Proxy: map[string]any{"name": "parsed"}}, nil
	}
	e.remove = func(string) error { return nil }
	e.mkdir = func(string, os.FileMode) error { return nil }
	e.write = func(string, []byte, os.FileMode) error { return nil }
	e.rename = func(string, string) error { return nil }
	e.logger = func(string) {}
	return e
}
func TestManualServicePreview(t *testing.T) {
	a := &app{}
	e := safeManualFX(a, false)
	e.running = func() bool { t.Fatal("preview checked running"); return true }
	a.mu.Lock()
	r := runGoManualService(manualServiceRequest{Op: "preview", URI: "synthetic", Name: " renamed "}, e)
	a.mu.Unlock()
	if r.Err != nil || r.Node.Name != "renamed" || r.Node.Proxy["name"] != "parsed" || len(a.manualNodes) != 0 {
		t.Fatal("preview changed state")
	}
}
func TestManualServiceAddRollback(t *testing.T) {
	for _, failure := range []string{"save", "provider", "none"} {
		t.Run(failure, func(t *testing.T) {
			a := &app{store: &store{}}
			e := safeManualFX(a, false)
			saved := false
			e.set = func(string) error {
				if failure == "save" {
					return errors.New("fake")
				}
				saved = true
				return nil
			}
			e.rename = func(string, string) error {
				if failure == "provider" {
					return errors.New("fake")
				}
				return nil
			}
			r := runGoManualService(manualServiceRequest{Op: "add", Name: " override "}, e)
			if failure == "save" {
				if a.manualNodes == nil || len(a.manualNodes) != 0 || saved {
					t.Fatal("append rollback shape")
				}
			} else {
				if len(a.manualNodes) != 1 || !saved || a.manualNodes[0].Proxy["name"] != "override" || a.manualNodes[0].AddedAt != 1<<40 {
					t.Fatal("committed state")
				}
			}
			if (r.Err != nil) != (failure != "none") {
				t.Fatal(r.Err)
			}
			if !a.mu.TryLock() {
				t.Fatal("lock leak")
			}
			a.mu.Unlock()
		})
	}
}
func TestManualServiceDeleteRollback(t *testing.T) {
	for _, failure := range []string{"save", "provider", "none"} {
		t.Run(failure, func(t *testing.T) {
			a := &app{store: &store{}, manualNodes: []manualNode{{Name: "same"}, {Name: "same"}}}
			e := safeManualFX(a, false)
			var body string
			e.set = func(s string) error {
				body = s
				if failure == "save" {
					return errors.New("fake")
				}
				return nil
			}
			e.remove = func(string) error {
				if failure == "provider" {
					return errors.New("fake")
				}
				return nil
			}
			r := runGoManualService(manualServiceRequest{Op: "delete", Name: " same "}, e)
			if failure == "save" {
				if len(a.manualNodes) != 2 {
					t.Fatal("not restored")
				}
			} else if a.manualNodes == nil || len(a.manualNodes) != 0 {
				t.Fatal("deletion not retained")
			}
			if body != "{\"nodes\":[]}" {
				t.Fatal(body)
			}
			if (r.Err != nil) != (failure != "none") {
				t.Fatal(r.Err)
			}
		})
	}
}
func TestManualServiceLoad(t *testing.T) {
	a := &app{store: &store{}, manualNodes: []manualNode{{Name: "old"}}}
	e := safeManualFX(a, true)
	e.get = func() (string, bool, error) {
		return `{"nodes":[{"name":"good","uri":"synthetic","proxy":{"name":"wrong"}},{"name":"good","uri":"synthetic","proxy":{"tls":true}},{"name":"bad","uri":"","proxy":{"tls":true}}]}`, true, nil
	}
	e.parse = func(string) (manualNode, error) { t.Fatal("load reparsed"); return manualNode{}, nil }
	a.mu.Lock()
	runGoManualService(manualServiceRequest{Op: "load", Borrowed: true}, e)
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	a.mu.Unlock()
	if len(a.manualNodes) != 2 || a.manualNodes[0].Proxy["name"] != "good" || a.manualNodes[1].Proxy["name"] != "good" {
		t.Fatal("load filtering")
	}
}
func TestManualServiceGraphAndNilStore(t *testing.T) {
	m := map[string]any{"nan": math.NaN(), "fn": func() {}}
	m["self"] = m
	a := &app{manualNodes: []manualNode{{Name: "synthetic", Proxy: m}}}
	e := safeManualFX(a, true)
	e.encode = func([]manualNode) ([]byte, error) { t.Fatal("nil store encoded"); return nil, nil }
	r := runGoManualService(manualServiceRequest{Op: "save", Borrowed: true}, e)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	copy := cloneManualNode(a.manualNodes[0])
	copy.Proxy["self"].(map[string]any)["new"] = true
	if _, ok := m["new"]; ok {
		t.Fatal("graph alias escaped")
	}
	if copy.Proxy["new"] != true || reflect.TypeOf(copy.Proxy["fn"]).Kind() != reflect.Func {
		t.Fatal("graph changed")
	}
}
func TestManualServiceProviderErrorIdentity(t *testing.T) {
	a := &app{}
	e := safeManualFX(a, true)
	for _, tc := range []struct {
		err error
		ok  bool
	}{{os.ErrNotExist, true}, {errors.Join(errors.New("wrap"), os.ErrNotExist), true}, {errors.New(os.ErrNotExist.Error()), false}} {
		e.remove = func(string) error { return tc.err }
		r := runGoManualService(manualServiceRequest{Op: "provider", Borrowed: true}, e)
		if (r.Err == nil) != tc.ok {
			t.Fatal("error identity")
		}
	}
}
func TestManualServiceOriginalGraphIdentity(t *testing.T) {
	a := &app{store: &store{}}
	e := safeManualFX(a, false)
	r := runGoManualService(manualServiceRequest{Op: "add", Name: "new"}, e)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	r.Node.Proxy["shared"] = true
	if a.manualNodes[0].Proxy["shared"] != true {
		t.Fatal("add return no longer aliases stored proxy")
	}
	for _, rollback := range []bool{false, true} {
		nested := map[string]any{"x": 1}
		p := map[string]any{"nested": nested}
		original := make([]manualNode, 2, 5)
		original[0] = manualNode{Name: "keep", Proxy: p}
		original[1] = manualNode{Name: "delete", Proxy: p}
		a := &app{store: &store{}, manualNodes: original}
		e := safeManualFX(a, false)
		e.encode = func(ns []manualNode) ([]byte, error) {
			if reflect.ValueOf(ns[0].Proxy).UnsafePointer() != reflect.ValueOf(p).UnsafePointer() {
				t.Fatal("encode lost original graph")
			}
			return []byte("synthetic"), nil
		}
		e.yaml = func(ns []manualNode) ([]byte, error) {
			if reflect.ValueOf(ns[0].Proxy).UnsafePointer() != reflect.ValueOf(p).UnsafePointer() {
				t.Fatal("yaml lost original graph")
			}
			return []byte("synthetic"), nil
		}
		if rollback {
			e.set = func(string) error { return errors.New("fake") }
		}
		runGoManualService(manualServiceRequest{Op: "delete", Name: "delete"}, e)
		if reflect.ValueOf(a.manualNodes[0].Proxy).UnsafePointer() != reflect.ValueOf(p).UnsafePointer() {
			t.Fatal("kept map identity changed")
		}
		if rollback && (&a.manualNodes[0] != &original[0] || cap(a.manualNodes) != cap(original) || len(a.manualNodes) != 2) {
			t.Fatal("rollback original slice/backing lost")
		}
		nested["later"] = true
		if a.manualNodes[0].Proxy["nested"].(map[string]any)["later"] != true {
			t.Fatal("nested alias lost")
		}
	}
}
