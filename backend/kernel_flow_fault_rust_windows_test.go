//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type kernelInjectedSession struct {
	kernelFlowSession
	kind, target                 byte
	nth, seen                    int
	nextFailure, pending, failed bool
}

func (s *kernelInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.kernelFlowSession.next()
	if e == nil {
		f, err := decodeKernelFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *kernelInjectedSession) advance(n uint64, b []byte) error {
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
	return s.kernelFlowSession.advance(n, b)
}

type kernelFaultFX struct {
	state                 kernelServiceState
	trace                 []kernelEffect
	failure, panicAt      string
	local, transferred    bool
	closed, starts, stops int
	options               kernelOptionFacts
}

func newKernelFaultFX() *kernelFaultFX {
	return &kernelFaultFX{state: kernelServiceState{Mode: "tun", SubscriptionURL: "https://synthetic.invalid", SocksPort: 1080}, options: kernelOptionFacts{TUN: true, DNSProtection: true, Active: true}}
}
func (f *kernelFaultFX) Release() {
	if f.local {
		f.local = false
		f.closed++
	}
}
func (f *kernelFaultFX) Call(q kernelEffect) (r kernelReply) {
	f.trace = append(f.trace, q)
	defer func() { s := f.state; r.State = &s }()
	if q.Kind == f.failure {
		r.Err = errors.New("synthetic " + q.Kind)
		return
	}
	switch q.Kind {
	case "profile-matches":
		r.Bool = true
	case "mixed-port":
		r.Port = 1080
		f.state.MixedPort = r.Port
	case "control-port":
		r.Port = 9090
		f.state.ControlPort = r.Port
	case "read-profile":
		r.Body = []byte("synthetic")
	case "options":
		r.Token = 1
		r.Options = f.options
	case "china-active":
		f.state.ChinaActive = q.Bool
	case "relay-new":
		r.Token = 2
		f.local = true
	case "generate":
		r.Text = "synthetic config"
	case "runner-new":
		r.Token = 3
		r.Bool = true
	case "runner-start":
		f.starts++
		f.state.KernelPresent = true
	case "history-start":
		f.state.HistoryPresent = true
	case "relay-transfer":
		f.local = false
		f.transferred = q.Token != 0
		f.state.RelayPresent = q.Token != 0
	case "close-local":
		f.Release()
	case "close-app-relay":
		f.closed++
		f.state.RelayPresent = false
		f.transferred = false
	case "history-stop":
		f.state.HistoryPresent = false
	case "runner-stop":
		f.stops++
		f.state.KernelPresent = false
		f.state.ChinaActive = false
	}
	if q.Kind == f.panicAt {
		panic("synthetic port panic")
	}
	return
}
func kernelFaultSame(t *testing.T, x, y *kernelFaultFX, w, g kernelServiceResult) {
	t.Helper()
	we, ge := "", ""
	if w.Err != nil {
		we = w.Err.Error()
	}
	if g.Err != nil {
		ge = g.Err.Error()
	}
	if we != ge || !reflect.DeepEqual(x.trace, y.trace) || x.state != y.state || x.closed != y.closed || x.local != y.local || x.transferred != y.transferred || x.starts != y.starts || x.stops != y.stops {
		t.Fatalf("mismatch error %q/%q trace %#v/%#v state %#v/%#v resources %d/%d", we, ge, x.trace, y.trace, x.state, y.state, x.closed, y.closed)
	}
}
func TestKernelFlowActualFaultsNoReplay(t *testing.T) {
	if !rustKernelFlowAvailable() {
		t.Fatal("DLL required")
	}
	type scenario struct {
		op      string
		failure string
		target  byte
	}
	var cases []scenario
	for _, k := range []byte{1, 2, 3, 4, 6, 7, 9, 11, 12, 13, 14, 34, 35, 15, 16, 17, 19, 20, 21, 22, 23, 24, 25} {
		cases = append(cases, scenario{"start", "", k})
	}
	for _, k := range []byte{19, 29, 30, 31, 33, 26} {
		cases = append(cases, scenario{"start", "ready", k})
	}
	for _, k := range []byte{1, 27, 29, 30, 31, 33} {
		cases = append(cases, scenario{"stop", "", k})
	}
	for _, c := range cases {
		for _, next := range []bool{false, true} {
			x, y := newKernelFaultFX(), newKernelFaultFX()
			x.failure = c.failure
			y.failure = c.failure
			if c.failure == "ready" {
				x.state.HistoryPresent = true
				y.state.HistoryPresent = true
			}
			if c.op == "stop" {
				x.state.KernelPresent = true
				x.state.RelayPresent = true
				x.state.HistoryPresent = true
				y.state = x.state
			}
			q := kernelServiceRequest{Op: c.op}
			want := runGoKernelService(q, x)
			var inj *kernelInjectedSession
			got, ok := runRustKernelServiceWithStart(q, y, func(b []byte) (kernelFlowSession, error) {
				s, e := startRustKernelFlow(b)
				if e != nil {
					return nil, e
				}
				inj = &kernelInjectedSession{kernelFlowSession: s, target: c.target, nth: 1, nextFailure: next}
				return inj, nil
			})
			if !ok || inj == nil || !inj.failed {
				t.Fatalf("target %d/%s not injected next=%v reason=%v", c.target, c.failure, next, kernelFlowRecoveryReason.Load())
			}
			kernelFaultSame(t, x, y, want, got)
		}
	}
}
func TestKernelFlowActualPanicOwnership(t *testing.T) {
	if !rustKernelFlowAvailable() {
		t.Fatal("DLL required")
	}
	for _, port := range []string{"relay-new", "options-relay", "generate", "write-config", "runner-new", "runner-start", "relay-transfer", "relay-monitor"} {
		f := newKernelFaultFX()
		f.panicAt = port
		r, ok := runRustKernelService(kernelServiceRequest{Op: "start"}, f)
		if !ok || r.Err == nil {
			t.Fatalf("%s false success", port)
		}
		if f.trace[len(f.trace)-1].Kind != port {
			t.Fatalf("%s replayed effects", port)
		}
		transferred := port == "relay-transfer" || port == "relay-monitor"
		if f.local || f.transferred != transferred || f.closed != map[bool]int{true: 0, false: 1}[transferred] {
			t.Fatalf("%s bad cleanup local=%v transferred=%v closed=%d", port, f.local, f.transferred, f.closed)
		}
	}
}

func TestKernelFlowActualCapacityAndCancellation(t *testing.T) {
	if !rustKernelFlowAvailable() {
		t.Fatal("DLL required")
	}
	var held []kernelFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustKernelFlow([]byte{0})
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, s)
	}
	f := newKernelFaultFX()
	_, used := runRustKernelService(kernelServiceRequest{Op: "start"}, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity must fail before effects")
	}
	t.Setenv("SMARTVPN_RUST_KERNEL_FLOW", "1")
	r := runSelectedKernelService(kernelServiceRequest{Op: "start"}, f)
	if r.Err != nil || f.starts != 1 {
		t.Fatal("capacity Go fallback failed")
	}
	for _, s := range held {
		s.close()
	}
	held = nil
	before := runtime.NumGoroutine()
	start := time.Now()
	for i := 0; i < 1200; i++ {
		m := newKernelMirror(kernelServiceRequest{Op: "start"})
		if _, ok := m.next(); !ok {
			t.Fatal("missing first event")
		}
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror goroutines leaked")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation too slow")
	}
	f = newKernelFaultFX()
	r, used = runRustKernelService(kernelServiceRequest{Op: "start"}, f)
	if !used || r.Err != nil {
		t.Fatal("released capacity not reusable")
	}
}
