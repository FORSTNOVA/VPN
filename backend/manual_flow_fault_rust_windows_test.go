//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type manualInjectedSession struct {
	manualFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *manualInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.manualFlowSession.next()
	if e == nil {
		f, err := decodeManualFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *manualInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		s.seen++
		if s.seen == s.nth {
			if s.nextFailure {
				s.pending = true
			} else {
				s.failed = true
				return errors.New("synthetic advance failure")
			}
		}
	}
	return s.manualFlowSession.advance(n, b)
}

func manualFaultUnlock(t *testing.T, f *manualDiffFX, borrowed bool) {
	t.Helper()
	if borrowed {
		if f.Base.a.mu.TryLock() {
			f.Base.a.mu.Unlock()
			t.Fatal("released caller lock")
		}
		f.Base.a.mu.Unlock()
	} else {
		if !f.Base.a.mu.TryLock() {
			t.Fatal("owned lock leaked")
		}
		f.Base.a.mu.Unlock()
	}
}
func TestManualFlowActualFaultsNoReplay(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("DLL required")
	}
	for k := byte(1); k <= 21; k++ {
		for _, next := range []bool{false, true} {
			spec := manualDiffBase()
			q := manualServiceRequest{Op: "add", URI: "synthetic", Name: "newer"}
			switch k {
			case 2:
				q.Op = "save"
				q.Borrowed = true
			case 8:
				spec.Fail["set"] = errors.New("save fail")
			case 9, 10:
				q.Op = "delete"
				q.Name = "old"
				if k == 10 {
					spec.Fail["set"] = errors.New("save fail")
				}
			case 11, 12, 13, 14:
				q.Op = "load"
				if k == 14 {
					spec.Fail["decode"] = errors.New("decode fail")
				}
			case 15, 16:
				q.Op = "save"
			case 17, 18, 19, 20, 21:
				q.Op = "provider"
				if k == 19 {
					spec.Nodes = nil
				}
			}
			x, y := manualDiffNew(spec, q.Borrowed), manualDiffNew(spec, q.Borrowed)
			want := runGoManualService(q, x)
			var inj *manualInjectedSession
			got, used := runRustManualServiceWithStart(q, y, func(b []byte) (manualFlowSession, error) {
				ss, e := startRustManualFlow(b)
				if e != nil {
					return nil, e
				}
				inj = &manualInjectedSession{manualFlowSession: ss, target: k, nth: 1, nextFailure: next}
				return inj, nil
			})
			we, ge := "", ""
			if want.Err != nil {
				we = want.Err.Error()
				want.Err = nil
			}
			if got.Err != nil {
				ge = got.Err.Error()
				got.Err = nil
			}
			if !used || inj == nil || !inj.failed || we != ge || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(x.Trace, y.Trace) || !reflect.DeepEqual(x.Base.a.manualNodes, y.Base.a.manualNodes) || !reflect.DeepEqual(x.Calls, y.Calls) || x.StoredBody != y.StoredBody || !reflect.DeepEqual(x.Files, y.Files) {
				t.Fatalf("kind%d next%v mismatch reason%v traces%#v/%#v", k, next, manualFlowRecoveryReason.Load(), x.Trace, y.Trace)
			}
			manualFaultUnlock(t, x, q.Borrowed)
			manualFaultUnlock(t, y, q.Borrowed)
		}
	}
}

type manualPanicFX struct {
	*manualDiffFX
	port string
}

func (f *manualPanicFX) Call(q manualEffect) manualReply {
	r := f.manualDiffFX.Call(q)
	if q.Kind == f.port {
		panic("synthetic manual panic")
	}
	return r
}
func TestManualFlowActualPanicReturnsError(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"lock", "snapshot", "parse", "name", "now", "append", "encode", "set", "yaml", "mkdir", "write", "rename", "remove", "remove-indices", "restore", "truncate", "reset", "get", "decode", "log"} {
		spec := manualDiffBase()
		q := manualServiceRequest{Op: "add", URI: "synthetic", Name: "newer"}
		switch port {
		case "snapshot":
			q.Borrowed = true
		case "remove":
			q.Op = "provider"
			spec.Nodes = nil
		case "remove-indices", "restore":
			q.Op = "delete"
			q.Name = "old"
			if port == "restore" {
				spec.Fail["set"] = errors.New("failed")
			}
		case "truncate":
			spec.Fail["set"] = errors.New("failed")
		case "reset", "get", "decode", "log":
			q.Op = "load"
			if port == "log" {
				spec.Fail["decode"] = errors.New("failed")
			}
		}
		f := &manualPanicFX{manualDiffNew(spec, q.Borrowed), port}
		r, used := runRustManualService(q, f)
		if !used || r.Err == nil || f.Calls[port] != 1 {
			t.Fatalf("panic %s false success/replay", port)
		}
		manualFaultUnlock(t, f.manualDiffFX, q.Borrowed)
	}
}

type manualCountFX struct {
	base  *appManualEffects
	calls map[string]int
}

func (f *manualCountFX) Call(q manualEffect) manualReply { f.calls[q.Kind]++; return f.base.Call(q) }
func (f *manualCountFX) Release()                        { f.base.Release() }
func TestManualFlowActualLargeLoadAndOpaquePayload(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("DLL required")
	}
	spec := manualDiffBase()
	spec.Nodes = nil
	spec.Stored = make([]manualNode, 50001)
	for i := range spec.Stored {
		spec.Stored[i] = manualNode{Name: fmt.Sprintf("node%05d", i), URI: "private://password-never-native", AddedAt: int64(i), Proxy: map[string]any{"name": "old"}}
	}
	fixture := manualDiffNew(spec, false)
	f := &manualCountFX{fixture.Base, map[string]int{}}
	before := manualFlowRecoveries.Load()
	r, used := runRustManualServiceWithStart(manualServiceRequest{Op: "load"}, f, func(b []byte) (manualFlowSession, error) {
		if strings.Contains(string(b), "password-never-native") {
			t.Fatal("URI in start")
		}
		ss, e := startRustManualFlow(b)
		return &manualInspectSession{manualFlowSession: ss, t: t}, e
	})
	if !used || r.Err != nil || manualFlowRecoveries.Load() != before || len(f.base.a.manualNodes) != 50001 || f.calls["decode"] != 1 || f.calls["append"] != 50001 {
		t.Fatalf("large incomplete/recovery %v", manualFlowRecoveryReason.Load())
	}
	for i, n := range f.base.a.manualNodes {
		if n.AddedAt != int64(i) || n.Proxy["name"] != n.Name {
			t.Fatal("load field loss")
		}
	}
}

type manualInspectSession struct {
	manualFlowSession
	t *testing.T
}

func (s *manualInspectSession) advance(n uint64, b []byte) error {
	if strings.Contains(string(b), "password-never-native") {
		s.t.Fatal("URI/serialized body entered native")
	}
	return s.manualFlowSession.advance(n, b)
}
func TestManualFlowActualUnusualGraphs(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, kind := range []string{"nan", "cycle", "function"} {
		spec := manualDiffBase()
		switch kind {
		case "nan":
			spec.Nodes[0].Proxy["value"] = math.NaN()
		case "cycle":
			spec.Nodes[0].Proxy["self"] = spec.Nodes[0].Proxy
		case "function":
			spec.Nodes[0].Proxy["fn"] = func() {}
		}
		f := manualDiffNew(spec, false)
		r, used := runRustManualService(manualServiceRequest{Op: "save"}, f)
		if !used || r.Err == nil || f.Calls["encode"] != 1 || f.Calls["set"] != 0 {
			t.Fatalf("%s lost original serialization error", kind)
		}
		manualFaultUnlock(t, f, false)
	}
	// no-store save must not inspect or serialize even unsupported graph.
	spec := manualDiffBase()
	spec.Store = false
	spec.Nodes[0].Proxy["fn"] = func() {}
	f := manualDiffNew(spec, false)
	r, used := runRustManualService(manualServiceRequest{Op: "save"}, f)
	if !used || r.Err != nil || f.Calls["encode"] != 0 {
		t.Fatal("nil-store save serialized")
	}
}
func TestManualFlowActualCapacityAndCancel(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("DLL required")
	}
	input, _ := manualStartInput(manualServiceRequest{Op: "list"})
	var held []manualFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		ss, e := startRustManualFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, ss)
	}
	f := manualDiffNew(manualDiffBase(), false)
	_, used := runRustManualService(manualServiceRequest{Op: "list"}, f)
	if used || len(f.Calls) != 0 {
		t.Fatal("capacity effects")
	}
	t.Setenv("SMARTVPN_RUST_MANUAL_FLOW", "1")
	if r := runSelectedManualService(manualServiceRequest{Op: "list"}, f); r.Err != nil {
		t.Fatal(r.Err)
	}
	for _, ss := range held {
		ss.close()
	}
	held = nil
	before := runtime.NumGoroutine()
	at := time.Now()
	for i := 0; i < 1200; i++ {
		m := newManualMirror(manualServiceRequest{Op: "list"})
		if _, ok := m.next(); !ok {
			t.Fatal("no mirror effect")
		}
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 || time.Since(at) > 10*time.Second {
		t.Fatal("cancel leak")
	}
}

type manualFinalizePanicFX struct {
	*manualDiffFX
	finalized int
}

func (f *manualFinalizePanicFX) FinalizeManualResult(q manualServiceRequest, r manualServiceResult) manualServiceResult {
	f.finalized++
	panic("synthetic finalize panic")
}
func TestManualFlowActualFinalizerPanic(t *testing.T) {
	if !rustManualFlowAvailable() {
		t.Fatal("DLL required")
	}
	f := &manualFinalizePanicFX{manualDiffFX: manualDiffNew(manualDiffBase(), false)}
	r, used := runRustManualService(manualServiceRequest{Op: "add", URI: "synthetic"}, f)
	if !used || r.Err == nil || f.finalized != 1 || f.Calls["append"] != 1 || f.Calls["set"] != 1 || f.Calls["rename"] != 1 {
		t.Fatal("finalizer panic false success or replay")
	}
	manualFaultUnlock(t, f.manualDiffFX, false)
}

func TestManualFlowOpaqueSlicePrefixDoesNotHideTail(t *testing.T) {
	x := []any{"same", "different-x"}
	y := []any{"same", "different-y"}
	if manualOpaqueEqual([]any{x[:1], x[:2]}, []any{y[:1], y[:2]}) {
		t.Fatal("shared prefix hid unequal suffix")
	}
	if !manualOpaqueEqual([]any{x[:1], x[:2]}, []any{x[:1], x[:2]}) {
		t.Fatal("equal graph rejected")
	}
}
