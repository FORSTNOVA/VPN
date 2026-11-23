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

type connectionDiffEvent struct {
	Effect          connectionEffect
	State           connectionServiceState
	Locked, Blocked bool
}
type connectionDiffFX struct {
	State                     connectionServiceState
	Trace                     []connectionDiffEvent
	Locked, Borrowed, Blocked bool
	Running, Conflict, Fresh  bool
	Nodes                     []proxyNode
	Candidates                []string
	Regions                   map[string]regionRecord
	Fail                      map[string]error
	Calls                     map[string]int
	FailAt                    map[string]int
	Nested                    any
	ConfirmWrong              bool
	RawConfirm                bool
	ConfirmValue              any
	Edit                      string
	Edited                    bool
	Problems                  []string
}

func connectionDiffClone(s connectionServiceState) connectionServiceState {
	s.CachedNodes = append([]proxyNode(nil), s.CachedNodes...)
	return s
}
func connectionDiffNew(src connectionDiffFX, op string) *connectionDiffFX {
	f := src
	f.State = connectionDiffClone(src.State)
	f.Trace = nil
	f.Calls = map[string]int{}
	f.Problems = nil
	f.Edited = false
	f.Locked = op == "drop"
	f.Borrowed = f.Locked
	return &f
}
func (f *connectionDiffFX) Release() {
	if f.Locked && !f.Borrowed {
		f.Locked = false
		f.record(connectionEffect{Kind: "release"})
	}
}
func (f *connectionDiffFX) record(e connectionEffect) {
	e.Nodes = append([]proxyNode(nil), e.Nodes...)
	f.Trace = append(f.Trace, connectionDiffEvent{e, connectionDiffClone(f.State), f.Locked, f.Blocked})
}
func (f *connectionDiffFX) Call(e connectionEffect) (r connectionReply) {
	f.Calls[e.Kind]++
	defer func() {
		if f.Locked {
			s := connectionDiffClone(f.State)
			r.State = &s
		}
		f.record(e)
	}()
	if e.Kind == "lock" {
		if f.Locked {
			panic("double lock")
		}
		f.Locked = true
		return
	}
	if e.Kind == "unlock" {
		if !f.Locked || f.Borrowed {
			panic("invalid unlock")
		}
		f.Locked = false
		return
	}
	if !f.Locked && e.Kind != "list" && e.Kind != "put" {
		panic("unlocked effect " + e.Kind)
	}
	if !f.Locked && !f.Edited && f.Edit != "" {
		f.Edited = true
		f.State.CachedNodes = append(f.State.CachedNodes, proxyNode{Name: "concurrent-cache"})
		f.State.Mode = modeTUN
		if f.Edit == "chain" {
			f.State.Chain = true
		}
		if f.Edit == "region" {
			f.State.LockedRegion = "JP"
			f.State.LockedNode = "concurrent-node"
		}
	}
	if err := f.Fail[e.Kind]; err != nil && (f.FailAt[e.Kind] == 0 || f.FailAt[e.Kind] == f.Calls[e.Kind]) {
		r.Err = err
	}
	switch e.Kind {
	case "snapshot":
	case "running":
		r.Bool = f.Running
	case "proxy-conflict":
		r.Bool = f.Conflict
	case "list":
		r.Nodes = append([]proxyNode(nil), f.Nodes...)
	case "get":
		if e.Path == "/proxies/SmartVPN" {
			r.Value = f.lastPut()
			if f.RawConfirm {
				r.Value = f.ConfirmValue
			}
			if f.ConfirmWrong {
				r.Value = "wrong"
			}
		} else {
			r.Value = f.Nested
		}
	case "put", "start-kernel", "stop-kernel", "save-settings", "save-cache", "upsert", "system-proxy", "start-patrol", "start-path", "async-patrol", "stop-patrol", "stop-path", "log":
	case "region":
		r.Region, r.Found = f.Regions[e.Name]
	case "region-fresh":
		r.Bool = f.Fresh
	case "candidates":
		r.Strings = append([]string(nil), f.Candidates...)
	case "latency":
		r.Latency = 37
	case "commit":
		switch e.Mutation {
		case "lock-node":
			f.State.LockedNode = e.Name
		case "selection":
			f.State.SelectionMode = e.Mode
			f.State.SelectedNode = e.Name
			f.State.Choice = e.Name
			if e.Mode == "auto" {
				f.State.Choice = autoGroup
			}
			if e.Mode == "fallback" {
				f.State.Choice = fallbackGroup
			}
		case "cache":
			f.State.CachedNodes = append([]proxyNode(nil), e.Nodes...)
		case "region":
			f.State.LockedRegion = e.Region
			f.State.LockedNode = e.Name
		case "clear-block":
			f.Blocked = false
		case "proxy-on":
			f.State.ProxyOn = e.Bool
		default:
			panic("unknown mutation " + e.Mutation)
		}
	default:
		panic("unknown effect " + e.Kind)
	}
	return
}
func (f *connectionDiffFX) lastPut() string {
	for i := len(f.Trace) - 1; i >= 0; i-- {
		if f.Trace[i].Effect.Kind == "put" {
			return f.Trace[i].Effect.Name
		}
	}
	return ""
}
func connectionDiffBase() connectionDiffFX {
	return connectionDiffFX{State: connectionServiceState{Mode: modeSystemProxy, SelectionMode: "auto", Choice: autoGroup, StoreAvailable: true, CachedNodes: []proxyNode{{Name: "a"}, {Name: " spaced "}}}, Blocked: true, Fresh: true, Nodes: []proxyNode{{Name: "a"}, {Name: "b"}, {Name: " spaced "}}, Nested: "a", Regions: map[string]regionRecord{}, Fail: map[string]error{}, FailAt: map[string]int{}}
}
func connectionDiffCompare(t *testing.T, q connectionServiceRequest, base connectionDiffFX) {
	t.Helper()
	if !rustConnectionFlowAvailable() {
		t.Fatal("actual connection DLL unavailable")
	}
	g, r := connectionDiffNew(base, q.Op), connectionDiffNew(base, q.Op)
	gr := runGoConnectionService(q, g)
	g.Release()
	before := connectionFlowRecoveries.Load()
	rr, handled := runRustConnectionService(q, r)
	r.Release()
	if !handled {
		t.Fatal("unexpected prestart Go fallback")
	}
	if connectionFlowRecoveries.Load() != before {
		t.Fatalf("unexpected continuation recovery op=%s trace=%+v", q.Op, r.Trace)
	}
	ge, re := "", ""
	if gr.Err != nil {
		ge = gr.Err.Error()
	}
	if rr.Err != nil {
		re = rr.Err.Error()
	}
	if ge != re || errors.Is(gr.Err, os.ErrProcessDone) != errors.Is(rr.Err, os.ErrProcessDone) {
		t.Fatalf("error identity Go=%v Rust=%v", gr.Err, rr.Err)
	}
	gr.Err = nil
	rr.Err = nil
	if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || !reflect.DeepEqual(g.State, r.State) || g.Blocked != r.Blocked || g.Locked != r.Locked {
		t.Fatalf("request=%+v\nGo=%+v\nRust=%+v\nGoTrace=%+v\nRustTrace=%+v", q, gr, rr, g.Trace, r.Trace)
	}
	if r.Locked != (q.Op == "drop") {
		t.Fatal("lock ownership violated")
	}
}
func TestMigrationConnectionActualFaultMatrix(t *testing.T) {
	for _, op := range []string{"connect", "disconnect", "drop", "select"} {
		for _, running := range []bool{false, true} {
			for _, proxy := range []bool{false, true} {
				for _, fault := range []string{"", "start-kernel", "list", "put", "get", "save-settings", "save-cache", "upsert", "system-proxy", "stop-kernel"} {
					t.Run(fmt.Sprintf("%s/%t/%t/%s", op, running, proxy, fault), func(t *testing.T) {
						b := connectionDiffBase()
						b.Running = running
						b.State.ProxyOn = proxy
						if fault != "" {
							b.Fail[fault] = errors.New("synthetic " + fault)
						}
						connectionDiffCompare(t, connectionServiceRequest{Op: op, Name: "a"}, b)
					})
				}
			}
		}
	}
}
func TestMigrationConnectionActualRandom(t *testing.T) {
	for i := 0; i < 1800; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(140000 + i)))
			b := connectionDiffBase()
			ops := []string{"connect", "disconnect", "drop", "select"}
			op := ops[rng.Intn(len(ops))]
			names := []string{"a", "b", "missing", " spaced ", autoGroup, fallbackGroup}
			b.Running = rng.Intn(2) == 0
			b.State.ProxyOn = rng.Intn(2) == 0
			b.State.Chain = rng.Intn(5) == 0
			b.Conflict = rng.Intn(7) == 0
			b.State.StoreAvailable = rng.Intn(2) == 0
			b.Fresh = rng.Intn(2) == 0
			b.ConfirmWrong = rng.Intn(8) == 0
			if rng.Intn(2) == 0 {
				b.State.Mode = modeTUN
			}
			if rng.Intn(3) == 0 {
				b.State.SelectionMode = "manual"
				b.State.SelectedNode = names[rng.Intn(len(names))]
				b.State.Choice = b.State.SelectedNode
			}
			if rng.Intn(3) == 0 {
				b.State.LockedRegion = "US"
				if rng.Intn(2) == 0 {
					b.State.LockedNode = "a"
				}
				if rng.Intn(2) == 0 {
					b.Candidates = []string{"b"}
				}
			}
			if rng.Intn(3) == 0 {
				b.Regions["a"] = regionRecord{Country: "US", Status: regionVerified}
			}
			if rng.Intn(8) == 0 {
				b.Nodes = nil
			}
			if rng.Intn(3) == 0 {
				b.Edit = []string{"chain", "region", "cache"}[rng.Intn(3)]
			}
			if rng.Intn(2) == 0 {
				faults := []string{"start-kernel", "list", "put", "get", "save-settings", "save-cache", "upsert", "system-proxy", "stop-kernel"}
				k := faults[rng.Intn(len(faults))]
				b.Fail[k] = errors.New("fault")
				b.FailAt[k] = 1 + rng.Intn(2)
			}
			connectionDiffCompare(t, connectionServiceRequest{Op: op, Name: names[rng.Intn(len(names))]}, b)
		})
	}
}
func TestMigrationConnectionActualDropErrorIdentity(t *testing.T) {
	errs := []error{nil, os.ErrProcessDone, fmt.Errorf("wrapped: %w", os.ErrProcessDone), errors.New(os.ErrProcessDone.Error()), errors.New("stop failure")}
	for _, op := range []string{"drop", "disconnect"} {
		for i, stop := range errs {
			for j, proxy := range errs {
				for _, on := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/%d/%t", op, i, j, on), func(t *testing.T) {
						b := connectionDiffBase()
						b.State.ProxyOn = on
						b.Fail["stop-kernel"] = stop
						b.Fail["system-proxy"] = proxy
						connectionDiffCompare(t, connectionServiceRequest{Op: op}, b)
					})
				}
			}
		}
	}
}

func TestMigrationConnectionActualLegacyBoundaries(t *testing.T) {
	for _, name := range []string{" spaced ", autoGroup, fallbackGroup, "a"} {
		for _, edit := range []string{"chain", "region", "cache"} {
			t.Run("live/"+name+"/"+edit, func(t *testing.T) {
				b := connectionDiffBase()
				b.Running = true
				b.Edit = edit
				b.Regions["a"] = regionRecord{Country: "US", Status: regionVerified}
				q := connectionServiceRequest{Op: "select", Name: name}
				connectionDiffCompare(t, q, b)
				f := connectionDiffNew(b, q.Op)
				out := runGoConnectionService(q, f)
				if out.Status != 200 || f.State.Mode != modeTUN || len(f.State.CachedNodes) != 3 {
					t.Fatalf("latest concurrent state lost: %+v %+v", out, f.State)
				}
				if name == " spaced " && f.State.SelectedNode != name {
					t.Fatal("raw node name normalized")
				}
				if edit == "chain" && !f.State.Chain {
					t.Fatal("latest chain overwritten")
				}
			})
		}
	}
	t.Run("region-exhausted", func(t *testing.T) {
		b := connectionDiffBase()
		b.State.LockedRegion = "US"
		b.State.LockedNode = "absent"
		q := connectionServiceRequest{Op: "connect"}
		connectionDiffCompare(t, q, b)
		f := connectionDiffNew(b, q.Op)
		r := runGoConnectionService(q, f)
		if r.Status != 502 || f.Calls["put"] != 0 || f.Calls["stop-kernel"] != 1 {
			t.Fatal("region exhaustion must never select outside region")
		}
	})
	t.Run("proxy-undo", func(t *testing.T) {
		b := connectionDiffBase()
		b.Fail["system-proxy"] = errors.New("proxy failure")
		q := connectionServiceRequest{Op: "connect"}
		connectionDiffCompare(t, q, b)
		f := connectionDiffNew(b, q.Op)
		r := runGoConnectionService(q, f)
		if r.Status != 500 || f.Calls["system-proxy"] != 2 || f.Calls["stop-kernel"] != 1 || f.Calls["start-patrol"] != 0 {
			t.Fatal("proxy failure compensation changed")
		}
	})
	for _, kind := range []string{"list", "put", "get", "save-settings", "save-cache"} {
		t.Run("stop-once/"+kind, func(t *testing.T) {
			b := connectionDiffBase()
			b.Fail[kind] = errors.New("failure")
			q := connectionServiceRequest{Op: "connect"}
			connectionDiffCompare(t, q, b)
			f := connectionDiffNew(b, q.Op)
			r := runGoConnectionService(q, f)
			if r.Status == 200 || f.Calls["start-kernel"] != 1 || f.Calls["stop-kernel"] != 1 {
				t.Fatal("post-start failure must stop exactly once")
			}
			if kind == "save-cache" && len(f.State.CachedNodes) != len(b.Nodes) {
				t.Fatal("save-cache failure unexpectedly rolled back cache")
			}
		})
	}
	for _, v := range []any{nil, "", 42, "a"} {
		t.Run(fmt.Sprintf("nested/%v", v), func(t *testing.T) {
			b := connectionDiffBase()
			b.Nested = v
			connectionDiffCompare(t, connectionServiceRequest{Op: "connect"}, b)
		})
	}
}

func TestMigrationConnectionActualRawChoiceAndGET(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual-empty/%t", present), func(t *testing.T) {
			b := connectionDiffBase()
			b.State.SelectionMode = "manual"
			b.State.SelectedNode = ""
			b.State.Choice = ""
			if present {
				b.Nodes = append(b.Nodes, proxyNode{Name: ""})
			}
			connectionDiffCompare(t, connectionServiceRequest{Op: "connect"}, b)
		})
	}
	for _, value := range []any{nil, 42, false, ""} {
		t.Run(fmt.Sprintf("group/%v", value), func(t *testing.T) {
			b := connectionDiffBase()
			b.RawConfirm = true
			b.ConfirmValue = value
			connectionDiffCompare(t, connectionServiceRequest{Op: "connect"}, b)
		})
	}
}

// Real app mutex and mutation adapter; connectionFakeEffects intercepts every
// kernel/controller/proxy/persistence/timer port. No actual platform call occurs.
func TestMigrationConnectionActualAdapterConcurrentEdits(t *testing.T) {
	if !rustConnectionFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	for _, name := range []string{"one", autoGroup, fallbackGroup} {
		for _, edit := range []string{"chain", "region", "other"} {
			t.Run(name+"/"+edit, func(t *testing.T) {
				makeFX := func() *connectionFakeEffects {
					f := newConnectionFake(false)
					f.Running = true
					done := false
					f.Hook = func(e connectionEffect) {
						if e.Kind != "list" || done {
							return
						}
						done = true
						a := f.adapter.a
						if !a.mu.TryLock() {
							t.Fatal("connected list kept mutex")
						}
						defer a.mu.Unlock()
						testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://concurrent.invalid/new" })
						a.cachedNodes = append(a.cachedNodes, proxyNode{Name: "concurrent"})
						if edit == "chain" {
							a.settings.Chain.Enabled = true
						}
						if edit == "region" {
							a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
							a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "concurrent"})
						}
					}
					return f
				}
				g, r := makeFX(), makeFX()
				q := connectionServiceRequest{Op: "select", Name: name}
				gr := runGoConnectionService(q, g)
				before := connectionFlowRecoveries.Load()
				rr, handled := runRustConnectionService(q, r)
				if !handled || connectionFlowRecoveries.Load() != before {
					t.Fatal("unexpected fallback/recovery")
				}
				if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || !reflect.DeepEqual(settingsStateFromApp(g.adapter.a), settingsStateFromApp(r.adapter.a)) {
					t.Fatalf("adapter mismatch Go=%+v Rust=%+v", gr, rr)
				}
				a := r.adapter.a
				if !a.mu.TryLock() {
					t.Fatal("select leaked app mutex")
				}
				a.mu.Unlock()
				if a.settingsLocked().SubscriptionURL != "https://concurrent.invalid/new" || len(a.cachedNodes) != 2 {
					t.Fatal("stale snapshot overwrote concurrent edit")
				}
				if edit == "chain" && !a.settings.Chain.Enabled {
					t.Fatal("chain edit lost")
				}
			})
		}
	}
}
