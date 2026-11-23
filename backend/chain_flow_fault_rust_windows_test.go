//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type chainInjectedSession struct {
	chainFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *chainInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.chainFlowSession.next()
	if e == nil {
		f, err := decodeChainFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *chainInjectedSession) advance(n uint64, b []byte) error {
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
	return s.chainFlowSession.advance(n, b)
}

func chainFaultUnlock(t *testing.T, f *chainDiffFX) {
	t.Helper()
	if f.Base.a == nil {
		return
	}
	if f.Base.borrowed {
		if f.Base.a.mu.TryLock() {
			f.Base.a.mu.Unlock()
			t.Fatal("borrowed lock released")
		}
		f.Base.a.mu.Unlock()
	} else {
		if !f.Base.a.mu.TryLock() {
			t.Fatal("owned lock leak")
		}
		f.Base.a.mu.Unlock()
	}
}
func chainFaultRequest(op string) chainServiceRequest {
	return chainServiceRequest{Op: op, Next: proxyChain{Enabled: true, Hops: []string{"a", "b"}}, Name: "renamed", Original: "old", Hops: []string{"a", "b"}, Profile: []byte("private-yaml-password-never-native"), Index: map[string][]map[string]any{"a": {{"type": "trojan", "server": "secret-server-never-native"}}, "b": {{"type": "trojan", "server": "b.invalid"}}}}
}
func TestChainFlowActualFaultsNoReplay(t *testing.T) {
	if !rustChainFlowAvailable() {
		t.Fatal("DLL required")
	}
	type scenario struct {
		kind byte
		op   string
		nth  int
		fail bool
	}
	var cases []scenario
	for k := byte(1); k <= 15; k++ {
		op := "update"
		if k == 2 {
			op = "build"
		}
		if k == 7 {
			op = "compile"
		}
		if k == 13 {
			op = "save-profile"
		}
		cases = append(cases, scenario{k, op, 1, k == 15})
	}
	cases = append(cases, scenario{15, "save-profile", 1, true}, scenario{15, "save-profile", 2, true}, scenario{6, "save-profile", 2, false}, scenario{11, "save-profile", 1, false})
	for _, c := range cases {
		for _, next := range []bool{false, true} {
			q := chainFaultRequest(c.op)
			spec := chainDiffBase()
			spec.State.LockedRegion = "US"
			if c.fail {
				spec.Fail["save"] = errors.New("synthetic save")
			}
			x, y := chainDiffNew(q, spec), chainDiffNew(q, spec)
			want := runGoChainService(q, x)
			var inj *chainInjectedSession
			got, used := runRustChainServiceWithStart(q, y, func(b []byte) (chainFlowSession, error) {
				ss, e := startRustChainFlow(b)
				if e != nil {
					return nil, e
				}
				inj = &chainInjectedSession{chainFlowSession: ss, target: c.kind, nth: c.nth, nextFailure: next}
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
			if !used || inj == nil || !inj.failed || we != ge || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(x.Trace, y.Trace) || !reflect.DeepEqual(x.Calls, y.Calls) {
				t.Fatalf("fault %d/%s/%d next%v mismatch reason%v traces%#v/%#v", c.kind, c.op, c.nth, next, chainFlowRecoveryReason.Load(), x.Trace, y.Trace)
			}
			chainFaultUnlock(t, x)
			chainFaultUnlock(t, y)
		}
	}
}

type chainPanicFX struct {
	*chainDiffFX
	port      string
	finalized int
}

func (f *chainPanicFX) Call(q chainEffect) chainReply {
	r := f.chainDiffFX.Call(q)
	if q.Kind == f.port {
		panic("synthetic chain panic")
	}
	return r
}
func (f *chainPanicFX) FinalizeChainResult(q chainServiceRequest, r chainServiceResult) chainServiceResult {
	f.finalized++
	if f.port == "finalize" {
		panic("synthetic finalizer panic")
	}
	return r
}
func TestChainFlowActualPanicReturnsError(t *testing.T) {
	if !rustChainFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"lock", "snapshot", "running", "job", "read", "decode", "index", "clone", "region", "age", "yaml", "commit-chain", "commit-profiles", "save", "restore-profiles", "restore-chain", "finalize"} {
		op := "update"
		if port == "snapshot" {
			op = "build"
		}
		if port == "index" {
			op = "compile"
		}
		if port == "commit-profiles" || port == "restore-profiles" || port == "restore-chain" {
			op = "save-profile"
		}
		q := chainFaultRequest(op)
		spec := chainDiffBase()
		spec.State.LockedRegion = "US"
		if strings.HasPrefix(port, "restore-") {
			spec.Fail["save"] = errors.New("save failed")
		}
		f := &chainPanicFX{chainDiffFX: chainDiffNew(q, spec), port: port}
		r, used := runRustChainService(q, f)
		if !used || r.Err == nil {
			t.Fatalf("%s false success", port)
		}
		if port == "finalize" {
			if f.finalized != 1 || f.Calls["save"] != 1 {
				t.Fatal("replayed finalization")
			}
		} else if f.Calls[port] != 1 {
			t.Fatalf("%s replay", port)
		}
		chainFaultUnlock(t, f.chainDiffFX)
	}
}

type chainInspectSession struct {
	chainFlowSession
	t *testing.T
}

func (s *chainInspectSession) advance(n uint64, b []byte) error {
	for _, secret := range []string{"private-yaml-password-never-native", "secret-server-never-native"} {
		if strings.Contains(string(b), secret) {
			s.t.Fatal("opaque content crossed native")
		}
	}
	return s.chainFlowSession.advance(n, b)
}
func TestChainFlowActualOpaquePayload(t *testing.T) {
	if !rustChainFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, op := range []string{"prefix", "compile"} {
		q := chainFaultRequest(op)
		f := chainDiffNew(q, chainDiffBase())
		before := chainFlowRecoveries.Load()
		r, used := runRustChainServiceWithStart(q, f, func(b []byte) (chainFlowSession, error) {
			if strings.Contains(string(b), "never-native") {
				t.Fatal("request leaked secret")
			}
			ss, e := startRustChainFlow(b)
			return &chainInspectSession{ss, t}, e
		})
		if !used || r.Err != nil || chainFlowRecoveries.Load() != before {
			t.Fatalf("opaque %s failure/recovery %v", op, chainFlowRecoveryReason.Load())
		}
	}
}
func TestChainFlowActualCapacityAndCancel(t *testing.T) {
	if !rustChainFlowAvailable() {
		t.Fatal("DLL required")
	}
	q := chainFaultRequest("compile")
	input, _ := chainStartInput(q)
	var held []chainFlowSession
	defer func() {
		for _, ss := range held {
			ss.close()
		}
	}()
	for i := 0; i < 64; i++ {
		ss, e := startRustChainFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, ss)
	}
	f := chainDiffNew(q, chainDiffBase())
	_, used := runRustChainService(q, f)
	if used || len(f.Calls) != 0 {
		t.Fatal("capacity effects")
	}
	t.Setenv("SMARTVPN_RUST_CHAIN_FLOW", "1")
	if r := runSelectedChainService(q, f); r.Err != nil {
		t.Fatal(r.Err)
	}
	for _, ss := range held {
		ss.close()
	}
	held = nil
	before := runtime.NumGoroutine()
	at := time.Now()
	for i := 0; i < 1200; i++ {
		m := newChainMirror(q)
		if _, ok := m.next(); !ok {
			t.Fatal("missing effect")
		}
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 || time.Since(at) > 10*time.Second {
		t.Fatal("cancel resource leak")
	}
}
