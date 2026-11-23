//go:build windows

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

type admissionFakeLog struct {
	events *[]string
	closed int
}

func (l *admissionFakeLog) Write(p []byte) (int, error) { return len(p), nil }
func (l *admissionFakeLog) Close() error {
	l.closed++
	*l.events = append(*l.events, "close")
	return nil
}
func admissionSafeStart(t *testing.T) (*appKernelAdmissionEffects, *kernelAdmissionWindowsEffects, *execKernel, *admissionFakeLog, *[]string, *func()) {
	t.Helper()
	a := &app{}
	k := &execKernel{app: a, cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}}
	e, w := newWindowsKernelAdmission(a, k, kernelAdmissionRequest{Op: "start"})
	events := []string{}
	l := &admissionFakeLog{events: &events}
	var worker func()
	w.spawn = func(f func()) { events = append(events, "spawn"); worker = f }
	e.hooks = map[string]func(kernelAdmissionEffect) kernelAdmissionReply{
		"command-new": func(kernelAdmissionEffect) kernelAdmissionReply {
			events = append(events, "new")
			w.cmd = &exec.Cmd{}
			w.start = func() error { events = append(events, "start"); return nil }
			w.kill = func() error { events = append(events, "kill"); return nil }
			w.wait = func() error { events = append(events, "wait"); return errors.New("ignored wait") }
			return kernelAdmissionReply{}
		},
		"log-open": func(kernelAdmissionEffect) kernelAdmissionReply {
			events = append(events, "open")
			w.log = l
			w.logOwned = true
			return kernelAdmissionReply{}
		},
		"command-log": func(kernelAdmissionEffect) kernelAdmissionReply {
			events = append(events, "attach")
			return kernelAdmissionReply{}
		},
		"exit-callback": func(kernelAdmissionEffect) kernelAdmissionReply {
			select {
			case <-k.exited:
			default:
				t.Fatal("callback before exited close")
			}
			events = append(events, "callback")
			return kernelAdmissionReply{}
		},
	}
	return e, w, k, l, &events, &worker
}
func TestKernelAdmissionServiceSafeWatcher(t *testing.T) {
	admissionSafeWatcher(t, runGoKernelAdmissionService)
}
func admissionSafeWatcher(t *testing.T, run func(kernelAdmissionRequest, kernelAdmissionEffects) kernelAdmissionResult) {
	e, _, k, l, events, worker := admissionSafeStart(t)
	k.app.mu.Lock()
	r := run(e.q, e)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if *worker == nil || l.closed != 0 {
		t.Fatal("watch ownership not transferred")
	}
	old := k.exited
	k.exited = make(chan struct{})
	(*worker)()
	if l.closed != 1 {
		t.Fatal(l.closed)
	}
	select {
	case <-old:
		t.Fatal("captured old channel")
	default:
	}
	if !reflect.DeepEqual((*events)[len(*events)-3:], []string{"wait", "close", "callback"}) {
		t.Fatal(*events)
	}
	k.app.mu.Unlock()
}
func TestKernelAdmissionServiceSafePartialStart(t *testing.T) {
	for _, point := range []string{"process-start", "exited-new", "watch-spawn"} {
		t.Run(point, func(t *testing.T) {
			e, w, k, l, events, worker := admissionSafeStart(t)
			old := k.cmd
			if point == "process-start" {
				e.hooks[point] = func(kernelAdmissionEffect) kernelAdmissionReply {
					return kernelAdmissionReply{Err: errors.New("start failure")}
				}
			} else if point == "watch-spawn" {
				w.spawn = func(f func()) { *worker = f; panic("unknown spawn acknowledgement") }
			} else {
				e.hooks[point] = func(kernelAdmissionEffect) kernelAdmissionReply { panic("after publication") }
			}
			r := runKernelAdmissionWithEffects(e.q, e)
			if r.Err == nil {
				t.Fatal("fault lost")
			}
			e.Release()
			count := func(s string) int {
				n := 0
				for _, v := range *events {
					if v == s {
						n++
					}
				}
				return n
			}
			switch point {
			case "process-start":
				if k.cmd != old || count("kill") != 0 || l.closed != 1 {
					t.Fatal(*events)
				}
			case "exited-new":
				if k.cmd == old || count("kill") != 1 || count("wait") != 1 || l.closed != 1 {
					t.Fatal(*events)
				}
			case "watch-spawn":
				if count("kill") != 0 || count("wait") != 0 || l.closed != 0 {
					t.Fatal("reclaimed transferred ownership", *events)
				}
				(*worker)()
				if l.closed != 1 {
					t.Fatal("worker cleanup missing")
				}
			}
		})
	}
}
func TestKernelAdmissionServiceSafeKillErrorNoJoin(t *testing.T) {
	e, _ := newWindowsKernelAdmission(&app{}, &execKernel{}, kernelAdmissionRequest{Op: "stop"})
	boom := errors.New("kill failed")
	e.hooks = map[string]func(kernelAdmissionEffect) kernelAdmissionReply{"running": func(kernelAdmissionEffect) kernelAdmissionReply { return kernelAdmissionReply{Bool: true} }, "process-kill": func(kernelAdmissionEffect) kernelAdmissionReply { return kernelAdmissionReply{Err: boom} }, "exited-join": func(kernelAdmissionEffect) kernelAdmissionReply {
		t.Fatal("joined failed kill")
		return kernelAdmissionReply{}
	}}
	r := runGoKernelAdmissionService(e.q, e)
	if r.Err != boom {
		t.Fatal("error identity lost")
	}
}

func TestKernelAdmissionServiceSafeIPv6ErrorClasses(t *testing.T) {
	for _, sentinel := range []error{windows.ERROR_NETWORK_UNREACHABLE, windows.ERROR_HOST_UNREACHABLE, windows.ERROR_NOT_FOUND, windows.ERROR_NOT_SUPPORTED} {
		for _, err := range []error{sentinel, fmt.Errorf("outer: %w", sentinel), errors.Join(errors.New("other"), fmt.Errorf("nested: %w", sentinel))} {
			if !kernelAdmissionIPv6Unavailable(err) {
				t.Fatalf("allowed wrapped class rejected: %v", err)
			}
		}
		if kernelAdmissionIPv6Unavailable(errors.New(sentinel.Error())) {
			t.Fatalf("text-only error accepted: %v", sentinel)
		}
	}
	for _, err := range []error{nil, windows.ERROR_ACCESS_DENIED, fmt.Errorf("wrapped: %w", windows.ERROR_ACCESS_DENIED), errors.Join(errors.New("unrelated"), windows.ERROR_INVALID_PARAMETER)} {
		if kernelAdmissionIPv6Unavailable(err) {
			t.Fatalf("unrelated error accepted: %v", err)
		}
	}
	seen := []error{}
	e := admissionClassOrderError{seen: &seen}
	if kernelAdmissionIPv6Unavailable(e) {
		t.Fatal("nonmatching custom error accepted")
	}
	want := []error{windows.ERROR_NETWORK_UNREACHABLE, windows.ERROR_HOST_UNREACHABLE, windows.ERROR_NOT_FOUND, windows.ERROR_NOT_SUPPORTED}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("errors.Is order changed: %v", seen)
	}
}

type admissionClassOrderError struct{ seen *[]error }

func (e admissionClassOrderError) Error() string { return "classification trace" }
func (e admissionClassOrderError) Is(target error) bool {
	*e.seen = append(*e.seen, target)
	return false
}
