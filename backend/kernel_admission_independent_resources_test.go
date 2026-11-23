//go:build windows

package main

import (
	"errors"
	"net"
	"os/exec"
	"testing"
	"time"
)

type admissionIndependentLog struct{ closed int }

func (l *admissionIndependentLog) Write(b []byte) (int, error) { return len(b), nil }
func (l *admissionIndependentLog) Close() error                { l.closed++; return nil }
func admissionIndependentResources(t *testing.T, run func(kernelAdmissionRequest, kernelAdmissionEffects) kernelAdmissionResult) {
	t.Run("watch-dynamic-exited", func(t *testing.T) {
		a := &app{}
		old, current := make(chan struct{}), make(chan struct{})
		k := &execKernel{app: a, exited: old}
		q := kernelAdmissionRequest{Op: "watch"}
		e, w := newWindowsKernelAdmission(a, k, q)
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan kernelAdmissionResult, 1)
		l := &admissionIndependentLog{}
		w.log = l
		w.logOwned = true
		w.wait = func() error { close(entered); <-release; return errors.New("ignored wait error") }
		e.hooks = map[string]func(kernelAdmissionEffect) kernelAdmissionReply{"exit-callback": func(kernelAdmissionEffect) kernelAdmissionReply {
			select {
			case <-current:
			default:
				t.Error("callback precedes dynamic close")
			}
			if l.closed != 1 {
				t.Error("callback precedes log close")
			}
			return kernelAdmissionReply{}
		}}
		go func() { done <- run(q, e) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("watch not waiting")
		}
		k.exited = current
		close(release)
		select {
		case r := <-done:
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("watch stalled")
		}
		select {
		case <-old:
			t.Fatal("captured old channel closed")
		default:
		}
		e.Release()
		if l.closed != 1 {
			t.Fatal("log closed twice")
		}
	})
	t.Run("ready-fresh-port-and-success-after-deadline", func(t *testing.T) {
		a := &app{mixedPort: 111}
		q := kernelAdmissionRequest{Op: "ready", Timeout: time.Second}
		e := newKernelAdmissionEffects(a, q)
		now := time.Unix(10, 0)
		e.clock = func() time.Time { return now }
		attempts, sleeps := 0, 0
		c, peer := net.Pipe()
		defer peer.Close()
		e.dial = func(network, address string, d time.Duration) (net.Conn, error) {
			attempts++
			if network != "tcp" || d != 300*time.Millisecond {
				t.Error("dial parameters")
			}
			if attempts == 1 {
				if address != "127.0.0.1:111" {
					t.Error("first port")
				}
				return nil, errors.New("fake refused")
			}
			if address != "127.0.0.1:222" {
				t.Error("port reused")
			}
			now = now.Add(2 * time.Second)
			return c, nil
		}
		e.sleep = func(d time.Duration) {
			sleeps++
			if d != 50*time.Millisecond {
				t.Error("sleep")
			}
			a.mixedPort = 222
			now = now.Add(d)
		}
		if r := run(q, e); r.Err != nil {
			t.Fatal(r.Err)
		}
		if attempts != 2 || sleeps != 1 || e.conn != nil {
			t.Fatal("ready ledger")
		}
		e.Release()
		if _, err := c.Write([]byte("x")); err == nil {
			t.Fatal("successful connection not closed")
		}
	})
}
func TestKernelAdmissionIndependentResources(t *testing.T) {
	admissionIndependentResources(t, runGoKernelAdmissionService)
}

func admissionIndependentUnpublishedCleanup(t *testing.T, run func(kernelAdmissionRequest, kernelAdmissionEffects) kernelAdmissionResult) {
	a := &app{}
	old := &exec.Cmd{}
	k := &execKernel{app: a, cmd: old}
	q := kernelAdmissionRequest{Op: "start"}
	e, w := newWindowsKernelAdmission(a, k, q)
	l := &admissionIndependentLog{}
	starts, kills, waits := 0, 0, 0
	e.hooks = map[string]func(kernelAdmissionEffect) kernelAdmissionReply{
		"command-new": func(kernelAdmissionEffect) kernelAdmissionReply {
			w.cmd = &exec.Cmd{}
			w.start = func() error { starts++; return nil }
			w.kill = func() error { kills++; return nil }
			w.wait = func() error { waits++; return nil }
			return kernelAdmissionReply{}
		},
		"log-open": func(kernelAdmissionEffect) kernelAdmissionReply {
			w.log = l
			w.logOwned = true
			return kernelAdmissionReply{}
		},
		"publish": func(kernelAdmissionEffect) kernelAdmissionReply { panic("synthetic before publication") },
	}
	var err error
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		err = run(q, e).Err
	}()
	if !panicked && err == nil {
		t.Fatal("interruption reported success")
	}
	e.Release()
	if starts != 1 || kills != 1 || waits != 1 || l.closed != 1 || k.cmd != old {
		t.Fatalf("new-resource cleanup start=%d kill=%d wait=%d close=%d old=%v", starts, kills, waits, l.closed, k.cmd == old)
	}
}
func TestKernelAdmissionIndependentUnpublishedCleanup(t *testing.T) {
	admissionIndependentUnpublishedCleanup(t, runGoKernelAdmissionService)
}
