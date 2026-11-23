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

type kernelDiffEvent struct {
	Effect               kernelEffect
	State                kernelServiceState
	Config               bool
	Started              int64
	AppRelay, LocalRelay uint64
	LocalClosed          bool
}
type kernelDiffFX struct {
	State                      kernelServiceState
	Options                    kernelOptionFacts
	Running, Matches, Checker  bool
	Fail                       map[string]error
	FailAt                     map[string]int
	Calls                      map[string]int
	Ports                      []int
	Next, AppRelay, LocalRelay uint64
	LocalClosed                bool
	Closes                     map[uint64]int
	Config                     bool
	Started                    int64
	Trace                      []kernelDiffEvent
}

func kernelDiffBase() kernelDiffFX {
	return kernelDiffFX{State: kernelServiceState{Mode: modeTUN, SubscriptionURL: "https://synthetic.invalid/sub", SocksPort: 1080, MixedPort: 9, ControlPort: 10}, Options: kernelOptionFacts{TUN: true, DNSProtection: true, Active: true, ChinaDirect: true}, Matches: true, Checker: true, Fail: map[string]error{}, FailAt: map[string]int{}, Ports: []int{1080, 1080, 1081}, Config: true, Started: 7}
}
func kernelDiffNew(b kernelDiffFX) *kernelDiffFX {
	b.Calls = map[string]int{}
	b.Closes = map[uint64]int{}
	b.Trace = nil
	b.Next = 100
	b.LocalRelay = 0
	b.LocalClosed = false
	if b.State.RelayPresent {
		b.AppRelay = 1
	}
	return &b
}
func (f *kernelDiffFX) record(e kernelEffect) {
	e.Body = append([]byte(nil), e.Body...)
	f.Trace = append(f.Trace, kernelDiffEvent{e, f.State, f.Config, f.Started, f.AppRelay, f.LocalRelay, f.LocalClosed})
}
func (f *kernelDiffFX) closeLocal() {
	if f.LocalRelay != 0 && !f.LocalClosed && f.AppRelay != f.LocalRelay {
		f.Closes[f.LocalRelay]++
		f.LocalClosed = true
	}
}
func (f *kernelDiffFX) Release() {
	if f.LocalRelay != 0 && !f.LocalClosed && f.AppRelay != f.LocalRelay {
		f.closeLocal()
		f.record(kernelEffect{Kind: "release-local"})
	}
}
func (f *kernelDiffFX) Call(e kernelEffect) (r kernelReply) {
	f.Calls[e.Kind]++
	defer func() { s := f.State; r.State = &s; f.record(e) }()
	if err := f.Fail[e.Kind]; err != nil && (f.FailAt[e.Kind] == 0 || f.FailAt[e.Kind] == f.Calls[e.Kind]) {
		r.Err = err
	}
	token := func() uint64 { f.Next++; return f.Next }
	switch e.Kind {
	case "snapshot":
	case "running":
		r.Bool = f.Running
	case "profile-matches":
		r.Bool = f.Matches
	case "tun", "fetch", "write-manual", "ready", "owner-check", "nodes-ready", "privacy", "history-collect", "relay-monitor", "options-relay":
	case "mixed-port":
		r.Port = 1080
		if r.Err != nil {
			r.Port = 0
		}
		f.State.MixedPort = r.Port
	case "control-port":
		i := f.Calls[e.Kind] - 1
		r.Port = 1081
		if i < len(f.Ports) {
			r.Port = f.Ports[i]
		}
		if r.Err != nil {
			r.Port = 0
		}
		f.State.ControlPort = r.Port
	case "read-profile":
		r.Body = []byte("synthetic profile bytes")
	case "options":
		if r.Err == nil {
			r.Token = token()
			r.Options = f.Options
		}
	case "china-active":
		f.State.ChinaActive = e.Bool
	case "relay-new":
		if r.Err == nil {
			r.Token = token()
			f.LocalRelay = r.Token
		}
	case "generate":
		r.Text = "synthetic generated configuration"
	case "write-config":
		if r.Err == nil {
			f.Config = true
		}
	case "runner-new":
		if r.Err == nil {
			r.Token = token()
			r.Bool = f.Checker
		}
	case "runner-start":
		if r.Err == nil {
			f.State.KernelPresent = true
			f.Started = 9007199254740993
		}
	case "history-start":
		f.State.HistoryPresent = true
	case "relay-transfer":
		f.AppRelay = e.Token
		f.State.RelayPresent = e.Token != 0
	case "close-local":
		if e.Token != f.LocalRelay {
			panic("wrong relay token")
		}
		f.closeLocal()
	case "close-app-relay":
		if f.AppRelay == 0 {
			panic("closing nil app relay")
		}
		f.Closes[f.AppRelay]++
		f.AppRelay = 0
		f.State.RelayPresent = false
	case "history-stop":
		f.State.HistoryPresent = false
	case "runner-stop":
		if !f.State.KernelPresent {
			panic("stop nil kernel")
		}
		f.State.KernelPresent = false
		f.State.ChinaActive = false
	case "remove-config":
		f.Config = false
	default:
		panic("unknown kernel effect " + e.Kind)
	}
	return
}
func kernelDiffCompare(t *testing.T, q kernelServiceRequest, b kernelDiffFX) {
	t.Helper()
	if !rustKernelFlowAvailable() {
		t.Fatal("actual kernel DLL unavailable")
	}
	g, r := kernelDiffNew(b), kernelDiffNew(b)
	gr := runGoKernelService(q, g)
	g.Release()
	before := kernelFlowRecoveries.Load()
	rr, handled := runRustKernelService(q, r)
	r.Release()
	if !handled {
		t.Fatal("unexpected prestart fallback")
	}
	if kernelFlowRecoveries.Load() != before {
		t.Fatalf("unexpected continuation op=%s trace=%+v", q.Op, r.Trace)
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
	// Propagated errors retain their original identity, not just their text.
	for _, err := range b.Fail {
		if err != nil && errors.Is(gr.Err, err) != errors.Is(rr.Err, err) {
			t.Fatalf("error chain identity lost: Go=%v Rust=%v", gr.Err, rr.Err)
		}
	}
	if !reflect.DeepEqual(g.Trace, r.Trace) || g.State != r.State || g.Config != r.Config || g.Started != r.Started || !reflect.DeepEqual(g.Closes, r.Closes) {
		t.Fatalf("op=%s GoErr=%v RustErr=%v\nGo=%+v\nRust=%+v", q.Op, gr.Err, rr.Err, g.Trace, r.Trace)
	}
	for id, n := range r.Closes {
		if n > 1 {
			t.Fatalf("relay %d double closed %d", id, n)
		}
	}
}
func TestMigrationKernelActualFaultMatrix(t *testing.T) {
	for _, op := range []string{"start", "stop"} {
		for _, oldRelay := range []bool{false, true} {
			for _, history := range []bool{false, true} {
				for _, fault := range []string{"", "tun", "fetch", "write-manual", "mixed-port", "control-port", "read-profile", "options", "relay-new", "write-config", "runner-new", "runner-start", "ready", "owner-check", "nodes-ready", "privacy", "runner-stop"} {
					t.Run(fmt.Sprintf("%s/%t/%t/%s", op, oldRelay, history, fault), func(t *testing.T) {
						b := kernelDiffBase()
						b.Matches = false
						b.State.RelayPresent = oldRelay
						b.State.HistoryPresent = history
						b.State.KernelPresent = op == "stop"
						if fault != "" {
							b.Fail[fault] = fmt.Errorf("synthetic %s: %w", fault, os.ErrPermission)
						}
						kernelDiffCompare(t, kernelServiceRequest{Op: op}, b)
					})
				}
			}
		}
	}
}
func TestMigrationKernelActualRandom(t *testing.T) {
	for i := 0; i < 1800; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(150000 + i)))
			b := kernelDiffBase()
			op := "start"
			if rng.Intn(3) == 0 {
				op = "stop"
			}
			b.State.KernelPresent = rng.Intn(2) == 0
			b.State.RelayPresent = rng.Intn(2) == 0
			b.State.HistoryPresent = rng.Intn(2) == 0
			b.State.ChinaActive = rng.Intn(2) == 0
			b.Running = b.State.KernelPresent && rng.Intn(3) == 0
			b.Matches = rng.Intn(2) == 0
			b.Checker = rng.Intn(2) == 0
			b.Options = kernelOptionFacts{TUN: rng.Intn(2) == 0, DNSProtection: rng.Intn(2) == 0, Active: rng.Intn(2) == 0, ChinaDirect: rng.Intn(2) == 0, DomesticWeb: rng.Intn(2) == 0}
			if rng.Intn(2) == 0 {
				b.State.Mode = modeSystemProxy
			}
			if rng.Intn(3) == 0 {
				b.State.SubscriptionURL = ""
			}
			if rng.Intn(5) == 0 {
				b.State.SubscriptionURL = mergedSubscriptionURL
			}
			b.State.ManualCount = rng.Intn(3)
			if rng.Intn(2) == 0 {
				b.State.SocksPort = 0
			}
			b.Ports = make([]int, rng.Intn(8))
			for j := range b.Ports {
				b.Ports[j] = 1080
			}
			if rng.Intn(2) == 0 {
				faults := []string{"tun", "fetch", "write-manual", "mixed-port", "control-port", "read-profile", "options", "relay-new", "write-config", "runner-new", "runner-start", "ready", "owner-check", "nodes-ready", "privacy", "runner-stop"}
				k := faults[rng.Intn(len(faults))]
				b.Fail[k] = fmt.Errorf("fault: %w", os.ErrProcessDone)
				b.FailAt[k] = 1 + rng.Intn(2)
			}
			kernelDiffCompare(t, kernelServiceRequest{Op: op}, b)
		})
	}
}

func TestMigrationKernelActualOwnershipAndCleanup(t *testing.T) {
	for _, fault := range []string{"", "write-config", "runner-new", "runner-start", "ready", "owner-check", "nodes-ready", "privacy"} {
		for _, oldRelay := range []bool{false, true} {
			t.Run(fmt.Sprintf("local/%s/%t", fault, oldRelay), func(t *testing.T) {
				b := kernelDiffBase()
				b.State.RelayPresent = oldRelay
				if fault != "" {
					b.Fail[fault] = errors.New("primary")
				}
				b.Fail["runner-stop"] = errors.New("cleanup secondary")
				q := kernelServiceRequest{Op: "start"}
				kernelDiffCompare(t, q, b)
				f := kernelDiffNew(b)
				r := runGoKernelService(q, f)
				if fault == "" {
					if r.Err != nil || f.AppRelay != f.LocalRelay || f.LocalClosed || f.Closes[1] != 0 {
						t.Fatal("relay ownership transfer changed")
					}
				} else {
					if f.LocalRelay == 0 || f.Closes[f.LocalRelay] != 1 {
						t.Fatal("untransferred relay must close exactly once")
					}
					if fault == "runner-start" || fault == "runner-new" || fault == "write-config" {
						if f.Calls["runner-stop"] != 0 || f.Calls["remove-config"] != 0 || f.Closes[1] != 0 {
							t.Fatal("prepublish failure incorrectly stopped/removed old state")
						}
					} else {
						if f.Calls["runner-stop"] != 1 || f.Calls["remove-config"] != 1 || f.Config || f.State.KernelPresent {
							t.Fatal("postpublish failure cleanup changed")
						}
						if oldRelay && f.Closes[1] != 1 {
							t.Fatal("existing app relay not closed")
						}
						if errors.Is(r.Err, b.Fail["runner-stop"]) {
							t.Fatal("cleanup error replaced original")
						}
					}
				}
			})
		}
	}
	for _, relay := range []bool{false, true} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("nil-stop/%t/%t", relay, history), func(t *testing.T) {
				b := kernelDiffBase()
				b.State.KernelPresent = false
				b.State.ChinaActive = true
				b.State.RelayPresent = relay
				b.State.HistoryPresent = history
				q := kernelServiceRequest{Op: "stop"}
				kernelDiffCompare(t, q, b)
				f := kernelDiffNew(b)
				runGoKernelService(q, f)
				if !f.Config || !f.State.ChinaActive || f.Started != 7 || f.Calls["runner-stop"] != 0 || f.Calls["remove-config"] != 0 || f.State.RelayPresent || f.State.HistoryPresent {
					t.Fatal("nil kernel stop changed legacy partial state")
				}
			})
		}
	}
	for _, n := range []int{0, 7, 128} {
		t.Run(fmt.Sprintf("duplicate-control/%d", n), func(t *testing.T) {
			b := kernelDiffBase()
			b.Ports = make([]int, n)
			for i := range b.Ports {
				b.Ports[i] = 1080
			}
			q := kernelServiceRequest{Op: "start"}
			kernelDiffCompare(t, q, b)
			f := kernelDiffNew(b)
			r := runGoKernelService(q, f)
			if r.Err != nil || f.Calls["control-port"] != n+1 {
				t.Fatal("control port collision retry changed")
			}
		})
	}
	for _, sub := range []string{"", mergedSubscriptionURL, "https://synthetic.invalid/sub"} {
		t.Run("profile-read/"+sub, func(t *testing.T) {
			b := kernelDiffBase()
			b.State.SubscriptionURL = sub
			b.State.ManualCount = 1
			b.Fail["read-profile"] = os.ErrNotExist
			kernelDiffCompare(t, kernelServiceRequest{Op: "start"}, b)
		})
	}
	for _, err := range []error{os.ErrProcessDone, fmt.Errorf("wrapped %w", os.ErrProcessDone), errors.New(os.ErrProcessDone.Error()), os.ErrPermission} {
		t.Run("stop-error/"+err.Error(), func(t *testing.T) {
			b := kernelDiffBase()
			b.State.KernelPresent = true
			b.Fail["runner-stop"] = err
			kernelDiffCompare(t, kernelServiceRequest{Op: "stop"}, b)
		})
	}
}

type kernelComposedFX struct {
	Connection  *connectionDiffFX
	Kernel      kernelDiffFX
	Rust        bool
	Selector    bool
	KernelTrace []kernelDiffEvent
}

func (f *kernelComposedFX) Release() { f.Connection.Release() }
func (f *kernelComposedFX) Call(e connectionEffect) connectionReply {
	if e.Kind != "start-kernel" && e.Kind != "stop-kernel" {
		return f.Connection.Call(e)
	}
	op := "start"
	if e.Kind == "stop-kernel" {
		op = "stop"
	}
	inner := kernelDiffNew(f.Kernel)
	inner.Next = f.Kernel.Next
	inner.AppRelay = f.Kernel.AppRelay
	if inner.State.RelayPresent && inner.AppRelay == 0 {
		inner.AppRelay = 1
	}
	var result kernelServiceResult
	if f.Rust {
		if f.Selector {
			result = runSelectedKernelService(kernelServiceRequest{Op: op}, inner)
		} else {
			var handled bool
			result, handled = runRustKernelService(kernelServiceRequest{Op: op}, inner)
			if !handled {
				panic("nested kernel prestart fallback")
			}
		}
	} else {
		result = runGoKernelService(kernelServiceRequest{Op: op}, inner)
	}
	inner.Release()
	f.KernelTrace = append(f.KernelTrace, inner.Trace...)
	f.Kernel = *inner
	f.Connection.Running = inner.State.KernelPresent
	r := f.Connection.Call(e)
	r.Err = result.Err
	return r
}
func TestMigrationKernelActualComposedConnection(t *testing.T) {
	if !rustKernelFlowAvailable() || !rustConnectionFlowAvailable() {
		t.Fatal("both actual DLL workflows required")
	}
	t.Setenv("SMARTVPN_RUST_KERNEL_FLOW", "1")
	t.Setenv("SMARTVPN_RUST_CONNECTION_FLOW", "1")
	for _, selector := range []bool{false, true} {
		for _, scenario := range []string{"success", "ready-failure", "proxy-failure", "stop-error", "existing-relay"} {
			t.Run(fmt.Sprintf("selector-%t/%s", selector, scenario), func(t *testing.T) {
				cb, kb := connectionDiffBase(), kernelDiffBase()
				op := "connect"
				switch scenario {
				case "ready-failure":
					kb.Fail["ready"] = errors.New("not ready")
				case "proxy-failure":
					cb.Fail["system-proxy"] = errors.New("proxy fail")
				case "stop-error":
					op = "disconnect"
					kb.State.KernelPresent = true
					kb.Fail["runner-stop"] = errors.New("stop fail")
					cb.State.ProxyOn = true
				case "existing-relay":
					kb.State.RelayPresent = true
				}
				makeFX := func(rust bool) *kernelComposedFX {
					return &kernelComposedFX{Connection: connectionDiffNew(cb, op), Kernel: kb, Rust: rust, Selector: selector}
				}
				g, r := makeFX(false), makeFX(true)
				q := connectionServiceRequest{Op: op}
				gr := runGoConnectionService(q, g)
				kc, cc := kernelFlowRecoveries.Load(), connectionFlowRecoveries.Load()
				var rr connectionServiceResult
				if selector {
					rr = runSelectedConnectionService(q, r)
				} else {
					var handled bool
					rr, handled = runRustConnectionService(q, r)
					if !handled {
						t.Fatal("connection prestart fallback")
					}
				}
				if kernelFlowRecoveries.Load() != kc || connectionFlowRecoveries.Load() != cc {
					t.Fatal("nested recovery occurred")
				}
				if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Connection.Trace, r.Connection.Trace) || !reflect.DeepEqual(g.KernelTrace, r.KernelTrace) || g.Kernel.State != r.Kernel.State || g.Kernel.Config != r.Kernel.Config {
					t.Fatalf("nested mismatch Go=%+v Rust=%+v", gr, rr)
				}
				starts, stops := 0, 0
				for _, event := range r.KernelTrace {
					if event.Effect.Kind == "runner-start" {
						starts++
					}
					if event.Effect.Kind == "runner-stop" {
						stops++
					}
				}
				wantStart, wantStop := 1, 0
				if scenario == "ready-failure" || scenario == "proxy-failure" {
					wantStop = 1
				}
				if scenario == "stop-error" {
					wantStart = 0
					wantStop = 1
				}
				if starts != wantStart || stops != wantStop {
					t.Fatalf("nested Start/Stop duplicated %d/%d", starts, stops)
				}
			})
		}
	}
}
