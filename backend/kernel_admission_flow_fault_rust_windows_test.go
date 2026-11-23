//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type admissionFaultFX struct {
	op, fail                  string
	trace                     []kernelAdmissionEffect
	release, finalize, before int
	panicKind                 string
	panicked                  bool
	unsaved                   bool
}

func (f *admissionFaultFX) Call(e kernelAdmissionEffect) (r kernelAdmissionReply) {
	f.trace = append(f.trace, e)
	switch e.Kind {
	case "saved-path":
		if !f.unsaved {
			r.Text = "fake-kernel"
		}
	case "bundle-present":
		r.Bool = true
	case "bundle-path":
		r.Text = "fake-bundle"
	case "home-path":
		r.Text = "fake-home"
	case "stat", "runner-new", "command-new", "log-open", "command-log", "process-start", "log-close", "publish", "exited-new", "watch-spawn", "process-wait", "exited-close", "exit-callback", "process-kill", "exited-join", "deadline", "conn-close", "sleep", "context", "context-cancel", "netstat", "config-command":
	case "running":
		r.Bool = true
	case "before":
		f.before++
		r.Bool = f.before <= 2
	case "dial":
		if f.fail == "dial" {
			r.Err = errors.New("synthetic dial error")
		}
	case "row-count":
		r.Count = 2
	case "owner-address":
		r.Text = "127.0.0.1:1234"
	case "row":
		if e.Index == 0 {
			r.Names = []string{"skip"}
		} else {
			r.Names = []string{"ANY", "127.0.0.1:1234", "0.0.0.0:0", "IGNORED", "91"}
		}
	case "pid":
		r.Text = "91"
	case "interface":
		r.Bool = true
		r.Count = 7
	case "route4", "route6":
		r.Count = 7
	case "output-split":
		r.Names = []string{"first", strings.Repeat("x", 301)}
	case "error":
		r.Err = fmt.Errorf("synthetic admission error %d/%s", e.Index, e.Text)
	default:
		panic("unsafe unknown admission test port " + e.Kind)
	}
	if e.Kind == f.fail {
		r.Err = errors.New("synthetic " + e.Kind + " error")
	}
	if e.Kind == f.panicKind && !f.panicked {
		f.panicked = true
		panic("after admission effect")
	}
	return
}
func (f *admissionFaultFX) Release() { f.release++ }
func (f *admissionFaultFX) FinalizeKernelAdmissionResult(q kernelAdmissionRequest, r kernelAdmissionResult) kernelAdmissionResult {
	f.finalize++
	return r
}

type admissionFaultCase struct {
	op, fail string
	unsaved  bool
}

func admissionFaultCases() []admissionFaultCase {
	var out []admissionFaultCase
	for _, op := range []string{"path", "look-for", "new", "running", "start", "watch", "stop", "ready", "owner", "privacy", "check-config"} {
		out = append(out, admissionFaultCase{op: op})
	}
	for _, c := range []admissionFaultCase{{"path", "stat", true}, {"new", "stat", false}, {"start", "log-open", false}, {"start", "process-start", false}, {"stop", "process-kill", false}, {"ready", "dial", false}, {"owner", "netstat", false}, {"privacy", "interface", false}, {"privacy", "route4", false}, {"privacy", "route6", false}, {"check-config", "config-command", false}} {
		out = append(out, c)
	}
	return out
}
func admissionMake(c admissionFaultCase) (kernelAdmissionRequest, *admissionFaultFX) {
	return kernelAdmissionRequest{Op: c.op, Port: 1234, Name: "synthetic", Timeout: time.Second}, &admissionFaultFX{op: c.op, fail: c.fail, unsaved: c.unsaved}
}
func admissionResultEqual(a, b kernelAdmissionResult) bool {
	if a.Bool != b.Bool || a.Text != b.Text || (a.Err == nil) != (b.Err == nil) {
		return false
	}
	return a.Err == nil || a.Err.Error() == b.Err.Error()
}

type admissionFaultSession struct {
	kernelAdmissionFlowSession
	kind                  string
	nextFault, fired, arm bool
	last                  kernelAdmissionFrame
}

func (s *admissionFaultSession) next() ([]byte, uint64, error) {
	if s.arm {
		s.arm = false
		return nil, 0, errors.New("next fault")
	}
	b, n, e := s.kernelAdmissionFlowSession.next()
	if e == nil {
		s.last, _ = decodeKernelAdmissionFrame(b)
	}
	return b, n, e
}
func (s *admissionFaultSession) advance(n uint64, b []byte) error {
	e := s.kernelAdmissionFlowSession.advance(n, b)
	if e == nil && !s.fired && s.last.kind == s.kind {
		s.fired = true
		if s.nextFault {
			s.arm = true
		} else {
			return errors.New("advance fault")
		}
	}
	return e
}
func TestKernelAdmissionFlowActualBoundaries(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual kernel admission unavailable")
	}
	cases := admissionFaultCases()
	seen := map[string]int{}
	for i, c := range cases {
		q, f := admissionMake(c)
		runGoKernelAdmissionService(q, f)
		for _, e := range f.trace {
			if _, ok := seen[e.Kind]; !ok {
				seen[e.Kind] = i
			}
		}
	}
	for k, i := range seen {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", k, next), func(t *testing.T) {
				q, w := admissionMake(cases[i])
				want := runGoKernelAdmissionService(q, w)
				q, g := admissionMake(cases[i])
				var fs *admissionFaultSession
				got, used := runRustKernelAdmissionServiceWithStart(q, g, func(b []byte) (kernelAdmissionFlowSession, error) {
					s, e := startRustKernelAdmissionFlow(b)
					fs = &admissionFaultSession{kernelAdmissionFlowSession: s, kind: k, nextFault: next}
					return fs, e
				})
				if !used || fs == nil || !fs.fired || !admissionResultEqual(want, got) || !reflect.DeepEqual(w.trace, g.trace) || g.release != 1 || g.finalize != 1 {
					t.Fatalf("used=%v fired=%v want=%#v got=%#v traces=%v/%v release=%d finalize=%d", used, fs != nil && fs.fired, want, got, w.trace, g.trace, g.release, g.finalize)
				}
			})
		}
	}
	t.Logf("%d boundaries / %d ABI faults", len(seen), 2*len(seen))
}
func TestKernelAdmissionFlowActualAfterPanic(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	seen := map[string]admissionFaultCase{}
	for _, c := range admissionFaultCases() {
		q, f := admissionMake(c)
		runGoKernelAdmissionService(q, f)
		for _, e := range f.trace {
			seen[e.Kind] = c
		}
	}
	for k, c := range seen {
		t.Run(k, func(t *testing.T) {
			q, f := admissionMake(c)
			f.panicKind = k
			r, used := runRustKernelAdmissionService(q, f)
			if !used || r.Err == nil || !f.panicked || f.release != 1 || f.finalize != 0 || f.trace[len(f.trace)-1].Kind != k {
				t.Fatalf("panic replay/success used=%v r=%#v release=%d finalize=%d", used, r, f.release, f.finalize)
			}
		})
	}
}
func TestKernelAdmissionFlowActualCapacityCancel(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	q, f := admissionMake(admissionFaultCase{op: "running"})
	b, e := kernelAdmissionStartInput(q)
	if e != nil {
		t.Fatal(e)
	}
	var ss []kernelAdmissionFlowSession
	defer func() {
		for _, s := range ss {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustKernelAdmissionFlow(b)
		if e != nil {
			t.Fatal(e)
		}
		ss = append(ss, s)
	}
	_, used := runRustKernelAdmissionService(q, f)
	if used || len(f.trace) > 0 || f.release != 0 {
		t.Fatal("capacity touched borrowed resources")
	}
	for _, s := range ss {
		s.close()
	}
	ss = nil
	n := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newKernelAdmissionMirror(q)
		m.close()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if runtime.NumGoroutine() > n+4 {
		t.Fatal("mirror leak")
	}
}

type admissionBridgeFakeConn struct{ closed int }

func (c *admissionBridgeFakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *admissionBridgeFakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *admissionBridgeFakeConn) Close() error                     { c.closed++; return nil }
func (c *admissionBridgeFakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *admissionBridgeFakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *admissionBridgeFakeConn) SetDeadline(time.Time) error      { return nil }
func (c *admissionBridgeFakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *admissionBridgeFakeConn) SetWriteDeadline(time.Time) error { return nil }

type admissionAfterActual struct {
	kernelAdmissionEffects
	kind  string
	fired bool
}

func (f *admissionAfterActual) Call(e kernelAdmissionEffect) kernelAdmissionReply {
	r := f.kernelAdmissionEffects.Call(e)
	if e.Kind == f.kind && !f.fired {
		f.fired = true
		panic("after actual admission effect")
	}
	return r
}
func TestKernelAdmissionFlowActualConnectionCleanup(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	for _, kind := range []string{"dial", "conn-close"} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprint(kind, panics), func(t *testing.T) {
				q := kernelAdmissionRequest{Op: "ready", Timeout: time.Second}
				a := &app{mixedPort: 1234}
				e := newKernelAdmissionEffects(a, q)
				e.clock = func() time.Time { return time.Unix(123, 0) }
				e.sleep = func(time.Duration) { t.Fatal("unexpected sleep") }
				c := &admissionBridgeFakeConn{}
				dials := 0
				e.dial = func(n, addr string, d time.Duration) (net.Conn, error) {
					dials++
					if n != "tcp" || addr != "127.0.0.1:1234" || d != 300*time.Millisecond {
						t.Fatal("dial facts")
					}
					return c, nil
				}
				var r kernelAdmissionResult
				var used bool
				if panics {
					r, used = runRustKernelAdmissionService(q, &admissionAfterActual{kernelAdmissionEffects: e, kind: kind})
				} else {
					r, used = runRustKernelAdmissionServiceWithStart(q, e, func(b []byte) (kernelAdmissionFlowSession, error) {
						s, err := startRustKernelAdmissionFlow(b)
						return &admissionFaultSession{kernelAdmissionFlowSession: s, kind: kind}, err
					})
				}
				if !used || dials != 1 || c.closed != 1 || (r.Err != nil) != panics {
					t.Fatalf("used=%v dial=%d close=%d err=%v", used, dials, c.closed, r.Err)
				}
			})
		}
	}
}

type admissionBridgeFakeLog struct{ closed int }

func (f *admissionBridgeFakeLog) Write(b []byte) (int, error) { return len(b), nil }
func (f *admissionBridgeFakeLog) Close() error                { f.closed++; return nil }
func TestKernelAdmissionFlowActualStartOwnership(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	for _, kind := range []string{"log-open", "process-start", "publish", "exited-new", "watch-spawn"} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprint(kind, panics), func(t *testing.T) {
				a := &app{}
				k := &execKernel{app: a}
				q := kernelAdmissionRequest{Op: "start"}
				e, w := newWindowsKernelAdmission(a, k, q)
				log := &admissionBridgeFakeLog{}
				starts, kills, waits, spawns := 0, 0, 0, 0
				e.hooks = map[string]func(kernelAdmissionEffect) kernelAdmissionReply{
					"command-new": func(kernelAdmissionEffect) kernelAdmissionReply {
						w.cmd = &exec.Cmd{}
						w.start = func() error { starts++; return nil }
						w.kill = func() error { kills++; return nil }
						w.wait = func() error { waits++; return nil }
						return kernelAdmissionReply{}
					},
					"log-open": func(kernelAdmissionEffect) kernelAdmissionReply {
						w.log = log
						w.logOwned = true
						return kernelAdmissionReply{}
					},
				}
				w.spawn = func(func()) { spawns++ }
				var r kernelAdmissionResult
				var used bool
				if panics {
					r, used = runRustKernelAdmissionService(q, &admissionAfterActual{kernelAdmissionEffects: e, kind: kind})
				} else {
					r, used = runRustKernelAdmissionServiceWithStart(q, e, func(b []byte) (kernelAdmissionFlowSession, error) {
						s, err := startRustKernelAdmissionFlow(b)
						return &admissionFaultSession{kernelAdmissionFlowSession: s, kind: kind}, err
					})
				}
				if !used || (r.Err != nil) != panics {
					t.Fatal("result", used, r.Err)
				}
				if !panics || kind == "watch-spawn" {
					if starts != 1 || spawns != 1 || kills != 0 || waits != 0 || log.closed != 0 {
						t.Fatal("transferred watcher resources reclaimed", starts, spawns, kills, waits, log.closed)
					}
				} else {
					expectedStart := 1
					if kind == "log-open" {
						expectedStart = 0
					}
					if starts != expectedStart || kills != expectedStart || waits != expectedStart || log.closed != 1 || spawns != 0 {
						t.Fatal("local cleanup repeated/lost", starts, kills, waits, log.closed, spawns)
					}
				}
			})
		}
	}
}

func TestKernelAdmissionFlowActualNestedWatcher(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	t.Setenv("SMARTVPN_RUST_KERNEL_ADMISSION_FLOW", "1")
	before := kernelAdmissionFlowRecoveries.Load()
	admissionSafeWatcher(t, func(q kernelAdmissionRequest, e kernelAdmissionEffects) kernelAdmissionResult {
		r, used := runRustKernelAdmissionService(q, e)
		if !used {
			t.Fatal("parent did not use native")
		}
		return r
	})
	if kernelAdmissionFlowRecoveries.Load() != before {
		t.Fatal("nested watcher recovered", kernelAdmissionFlowRecoveryReason.Load())
	}
}

type admissionLongFX struct {
	before, dials, sleeps, released int
	err                             error
}

func (f *admissionLongFX) Call(e kernelAdmissionEffect) kernelAdmissionReply {
	switch e.Kind {
	case "deadline":
		return kernelAdmissionReply{}
	case "before":
		f.before++
		return kernelAdmissionReply{Bool: f.before <= 60000}
	case "dial":
		f.dials++
		return kernelAdmissionReply{Err: f.err}
	case "sleep":
		f.sleeps++
		return kernelAdmissionReply{}
	default:
		panic("unexpected long ready effect")
	}
}
func (f *admissionLongFX) Release() { f.released++ }
func TestKernelAdmissionFlowActualLongReady(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	f := &admissionLongFX{err: errors.New("fake dial")}
	before := kernelAdmissionFlowRecoveries.Load()
	r, used := runRustKernelAdmissionService(kernelAdmissionRequest{Op: "ready", Timeout: time.Hour}, f)
	if !used || r.Err == nil || r.Err.Error() != "startup timeout" || f.before != 60001 || f.dials != 60000 || f.sleeps != 60000 || f.released != 1 || kernelAdmissionFlowRecoveries.Load() != before {
		t.Fatal("long ready changed cadence", used, r, f)
	}
}

type admissionMalformedSession struct {
	kernelAdmissionFlowSession
	target     string
	mode       int
	arm, fired bool
	last       kernelAdmissionFrame
}

func (s *admissionMalformedSession) next() ([]byte, uint64, error) {
	b, n, e := s.kernelAdmissionFlowSession.next()
	if e != nil {
		return b, n, e
	}
	s.last, _ = decodeKernelAdmissionFrame(b)
	if s.arm && !s.fired {
		s.fired = true
		switch s.mode {
		case 0:
			b = b[:len(b)-1]
		case 1:
			b = append(b, 0)
		case 2:
			n++
		case 3:
			b = append([]byte{}, b...)
			b[0] = 255
		}
	}
	return b, n, nil
}
func (s *admissionMalformedSession) advance(n uint64, b []byte) error {
	e := s.kernelAdmissionFlowSession.advance(n, b)
	if e == nil && s.last.kind == s.target {
		s.arm = true
	}
	return e
}
func TestKernelAdmissionFlowActualMalformedAfterEffect(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	for _, tc := range []struct{ op, target string }{{"start", "process-start"}, {"start", "publish"}, {"start", "watch-spawn"}, {"stop", "process-kill"}, {"watch", "process-wait"}, {"watch", "exited-close"}, {"watch", "exit-callback"}, {"ready", "dial"}, {"owner", "netstat"}} {
		for mode := 0; mode < 4; mode++ {
			t.Run(fmt.Sprint(tc.target, mode), func(t *testing.T) {
				q, w := admissionMake(admissionFaultCase{op: tc.op})
				want := runGoKernelAdmissionService(q, w)
				q, g := admissionMake(admissionFaultCase{op: tc.op})
				var s *admissionMalformedSession
				r, used := runRustKernelAdmissionServiceWithStart(q, g, func(b []byte) (kernelAdmissionFlowSession, error) {
					x, e := startRustKernelAdmissionFlow(b)
					s = &admissionMalformedSession{kernelAdmissionFlowSession: x, target: tc.target, mode: mode}
					return s, e
				})
				if !used || s == nil || !s.fired || !reflect.DeepEqual(w.trace, g.trace) || !admissionResultEqual(want, r) || g.release != 1 || g.finalize != 1 {
					t.Fatal("malformed frame replay", used, r, s != nil && s.fired)
				}
			})
		}
	}
}

type admissionCloseFaultSession struct {
	kernelAdmissionFlowSession
	closed int
}

func (s *admissionCloseFaultSession) close() {
	s.closed++
	s.kernelAdmissionFlowSession.close()
	panic("session close acknowledgement")
}

type admissionFinalizePanic struct{ *admissionFaultFX }

func (f *admissionFinalizePanic) FinalizeKernelAdmissionResult(kernelAdmissionRequest, kernelAdmissionResult) kernelAdmissionResult {
	f.finalize++
	panic("finalizer")
}
func TestKernelAdmissionFlowActualTerminalFaults(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("actual unavailable")
	}
	q, f := admissionMake(admissionFaultCase{op: "start"})
	var s *admissionCloseFaultSession
	r, used := runRustKernelAdmissionServiceWithStart(q, f, func(b []byte) (kernelAdmissionFlowSession, error) {
		x, e := startRustKernelAdmissionFlow(b)
		s = &admissionCloseFaultSession{kernelAdmissionFlowSession: x}
		return s, e
	})
	if !used || r.Err == nil || s == nil || s.closed != 1 || f.release != 1 || f.finalize != 0 {
		t.Fatal("close failure repeated transfer/release", used, r, f)
	}
	q, g := admissionMake(admissionFaultCase{op: "start"})
	r, used = runRustKernelAdmissionService(q, &admissionFinalizePanic{g})
	if !used || r.Err == nil || g.finalize != 1 || g.release != 1 {
		t.Fatal("finalizer fault false success", used, r, g)
	}
}
