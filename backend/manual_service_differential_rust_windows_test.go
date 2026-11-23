//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"testing"
)

func manualDiffCompare(t *testing.T, q manualServiceRequest, s manualDiffSpec) (*manualDiffFX, manualServiceResult) {
	t.Helper()
	if !rustManualFlowAvailable() {
		t.Fatal("actual manual DLL required")
	}
	g, r := manualDiffNew(s, q.Borrowed), manualDiffNew(s, q.Borrowed)
	if q.Borrowed {
		defer g.Base.a.mu.Unlock()
		defer r.Base.a.mu.Unlock()
	}
	gr := runGoManualService(q, g)
	before := manualFlowRecoveries.Load()
	rr, used := runRustManualService(q, r)
	if !used {
		t.Fatal("unexpected prestart fallback")
	}
	if manualFlowRecoveries.Load() != before {
		t.Fatalf("unexpected recovery op=%s", q.Op)
	}
	ge, re := "", ""
	if gr.Err != nil {
		ge = gr.Err.Error()
	}
	if rr.Err != nil {
		re = rr.Err.Error()
	}
	if ge != re {
		t.Fatalf("Goerr=%s Rusterr=%s", ge, re)
	}
	for _, err := range s.Fail {
		if err != nil && errors.Is(gr.Err, err) != errors.Is(rr.Err, err) {
			t.Fatal("error identity changed")
		}
	}
	gr.Err = nil
	rr.Err = nil
	if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || !reflect.DeepEqual(g.Base.a.manualNodes, r.Base.a.manualNodes) || g.StoredBody != r.StoredBody || !reflect.DeepEqual(g.Files, r.Files) {
		t.Fatalf("q=%+v\nGo=%+v\nRust=%+v\nGoTrace=%+v\nRustTrace=%+v", q, gr, rr, g.Trace, r.Trace)
	}
	if locked := r.Base.a.mu.TryLock(); locked {
		r.Base.a.mu.Unlock()
		if q.Borrowed {
			t.Fatal("borrowed lock released")
		}
	} else if !q.Borrowed {
		t.Fatal("owned lock leaked")
	}
	return r, rr
}
func TestMigrationManualActualMatrix(t *testing.T) {
	for _, op := range []string{"load", "save", "provider", "list", "preview", "add", "delete"} {
		for _, borrowed := range []bool{false, true} {
			for _, fault := range []string{"", "get", "decode", "parse", "encode", "set", "yaml", "remove", "mkdir", "write", "rename"} {
				t.Run(fmt.Sprintf("%s/%t/%s", op, borrowed, fault), func(t *testing.T) {
					s := manualDiffBase()
					if fault != "" {
						s.Fail[fault] = fmt.Errorf("synthetic %s: %w", fault, os.ErrPermission)
					}
					manualDiffCompare(t, manualServiceRequest{Op: op, URI: "synthetic", Name: "old", Borrowed: borrowed}, s)
				})
			}
		}
	}
}
func TestMigrationManualActualRandom(t *testing.T) {
	for i := 0; i < 1600; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(190000 + i)))
			s := manualDiffBase()
			ops := []string{"load", "save", "provider", "list", "preview", "add", "delete"}
			op := ops[rng.Intn(7)]
			s.Store = rng.Intn(3) != 0
			s.Running = rng.Intn(3) == 0
			s.Found = rng.Intn(4) != 0
			s.Now = []int64{0, -1, 2147483648, 9007199254740993, 9223372036854775807}[rng.Intn(5)]
			if rng.Intn(3) == 0 {
				s.Nodes = nil
			} else if rng.Intn(4) == 0 {
				s.Nodes = []manualNode{}
			}
			if rng.Intn(3) == 0 {
				s.Nodes = append(s.Nodes, cloneManualNode(s.Parsed))
			}
			if rng.Intn(3) == 0 {
				s.Cached = []proxyNode{{Name: "new"}, {Name: "old"}}
			}
			if rng.Intn(2) == 0 {
				faults := []string{"get", "decode", "parse", "encode", "set", "yaml", "remove", "mkdir", "write", "rename"}
				s.Fail[faults[rng.Intn(len(faults))]] = errors.New("synthetic")
			}
			names := []string{"", " ", "new", " old ", "override", "new\x00tail"}
			if rng.Intn(3) == 0 {
				s.Stored = append(s.Stored, manualNode{Name: "invalid", URI: "x"}, cloneManualNode(s.Stored[0]))
			}
			manualDiffCompare(t, manualServiceRequest{Op: op, Name: names[rng.Intn(len(names))], URI: "synthetic", Borrowed: rng.Intn(2) == 0}, s)
		})
	}
}
func TestMigrationManualActualLegacyGoldens(t *testing.T) {
	t.Run("preview-ignores-running-and-duplicates", func(t *testing.T) {
		s := manualDiffBase()
		s.Running = true
		s.Nodes = append(s.Nodes, s.Parsed)
		s.Cached = []proxyNode{{Name: "new"}}
		f, r := manualDiffCompare(t, manualServiceRequest{Op: "preview", URI: "synthetic", Name: " renamed "}, s)
		if r.Status != 200 || r.Node.Name != "renamed" || r.Node.Proxy["name"] != "new" || f.Calls["lock"] != 0 || f.Calls["running"] != 0 {
			t.Fatal("preview semantics changed")
		}
	})
	t.Run("running-before-parse", func(t *testing.T) {
		s := manualDiffBase()
		s.Running = true
		s.Fail["parse"] = errors.New("parse fail")
		f, r := manualDiffCompare(t, manualServiceRequest{Op: "add", URI: "bad"}, s)
		if r.Status != 409 || f.Calls["parse"] != 0 {
			t.Fatal("running must win over parse")
		}
	})
	t.Run("manual-duplicate-first", func(t *testing.T) {
		s := manualDiffBase()
		s.Nodes = append(s.Nodes, s.Parsed)
		s.Cached = []proxyNode{{Name: "new"}}
		f, r := manualDiffCompare(t, manualServiceRequest{Op: "add", URI: "synthetic"}, s)
		if r.Message != manualDuplicateMessage0("new") || f.Calls["now"] != 0 {
			t.Fatal("manual duplicate priority changed")
		}
	})
	t.Run("load-filter-no-dedup-or-parse", func(t *testing.T) {
		s := manualDiffBase()
		good := cloneManualNode(s.Stored[0])
		s.Stored = []manualNode{{Name: "", URI: "x", Proxy: map[string]any{"x": 1}}, {Name: "n", Proxy: map[string]any{"x": 1}}, {Name: "n", URI: "x"}, good, good}
		f, _ := manualDiffCompare(t, manualServiceRequest{Op: "load", Borrowed: true}, s)
		if len(f.Base.a.manualNodes) != 2 || f.Calls["parse"] != 0 || f.Calls["set"] != 0 || f.Base.a.manualNodes[0].Proxy["name"] != "old" {
			t.Fatal("load filtering changed")
		}
	})
	for _, op := range []string{"add", "delete"} {
		for _, fault := range []string{"set", "rename"} {
			t.Run(op+"/rollback/"+fault, func(t *testing.T) {
				s := manualDiffBase()
				if op == "delete" {
					s.Nodes = append(s.Nodes, cloneManualNode(s.Parsed))
				}
				s.Fail[fault] = errors.New("failure")
				f, _ := manualDiffCompare(t, manualServiceRequest{Op: op, URI: "synthetic", Name: map[string]string{"add": "new", "delete": "old"}[op]}, s)
				if fault == "set" && !reflect.DeepEqual(f.Base.a.manualNodes, s.Nodes) {
					t.Fatal("save failure failed to rollback")
				}
				if fault == "rename" && reflect.DeepEqual(f.Base.a.manualNodes, s.Nodes) {
					t.Fatal("provider failure incorrectly rolled back")
				}
				if fault == "rename" && op == "add" {
					if _, ok := f.Files[f.Base.a.manualPath()+".tmp"]; !ok {
						t.Fatal("rename error must keep tmp")
					}
				}
			})
		}
	}
	t.Run("delete-all-duplicates-nonnil", func(t *testing.T) {
		s := manualDiffBase()
		s.Nodes = append(s.Nodes, cloneManualNode(s.Nodes[0]))
		f, r := manualDiffCompare(t, manualServiceRequest{Op: "delete", Name: " old "}, s)
		if r.Status != 200 || f.Base.a.manualNodes == nil || len(f.Base.a.manualNodes) != 0 {
			t.Fatal("delete did not remove all matches")
		}
	})
	for i, err := range []error{os.ErrNotExist, fmt.Errorf("wrapped: %w", os.ErrNotExist), errors.New(os.ErrNotExist.Error()), os.ErrPermission} {
		t.Run(fmt.Sprintf("remove-identity/%d", i), func(t *testing.T) {
			s := manualDiffBase()
			s.Nodes = nil
			s.Fail["remove"] = err
			manualDiffCompare(t, manualServiceRequest{Op: "provider", Borrowed: true}, s)
		})
	}
}

func TestMigrationManualActualLegacyIdentity(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	manualLegacyIdentityContracts(t, func(q manualServiceRequest, fx manualServiceEffects) manualServiceResult {
		before := manualFlowRecoveries.Load()
		r, used := runRustManualService(q, fx)
		if !used || manualFlowRecoveries.Load() != before {
			t.Fatal("identity case fallback/recovery")
		}
		return r
	})
}
