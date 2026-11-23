package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type admissionIndependentFX struct {
	Trace    []kernelAdmissionEffect
	Replies  map[string]kernelAdmissionReply
	Before   []bool
	Rows     [][]string
	Released int
	Calls    map[string]int
}

func (f *admissionIndependentFX) Release() { f.Released++ }
func (f *admissionIndependentFX) Call(e kernelAdmissionEffect) kernelAdmissionReply {
	f.Trace = append(f.Trace, e)
	if f.Calls == nil {
		f.Calls = map[string]int{}
	}
	f.Calls[e.Kind]++
	if e.Kind == "before" {
		i := f.Calls[e.Kind] - 1
		if i < len(f.Before) {
			return kernelAdmissionReply{Bool: f.Before[i]}
		}
		return kernelAdmissionReply{}
	}
	if e.Kind == "row-count" {
		return kernelAdmissionReply{Count: len(f.Rows)}
	}
	if e.Kind == "row" {
		return kernelAdmissionReply{Names: f.Rows[e.Index]}
	}
	if e.Kind == "error" {
		return kernelAdmissionReply{Err: fmt.Errorf("synthetic admission error %d: %s", e.Index, e.Text)}
	}
	if r, ok := f.Replies[e.Kind]; ok {
		return r
	}
	panic("unexpected admission effect " + e.Kind)
}
func admissionIndependentGolden(t *testing.T, run func(kernelAdmissionRequest, kernelAdmissionEffects) kernelAdmissionResult) {
	t.Helper()
	boom := errors.New("synthetic effect failure")
	cases := []struct {
		name, op string
		replies  map[string]kernelAdmissionReply
		before   []bool
		rows     [][]string
		trace    []string
		err      bool
	}{
		{"start-publish-before-spawn", "start", map[string]kernelAdmissionReply{"command-new": {}, "log-open": {}, "command-log": {}, "process-start": {}, "publish": {}, "exited-new": {}, "watch-spawn": {}}, nil, nil, []string{"command-new", "log-open", "command-log", "process-start", "publish", "exited-new", "watch-spawn"}, false},
		{"start-log-failure", "start", map[string]kernelAdmissionReply{"command-new": {}, "log-open": {Err: boom}}, nil, nil, []string{"command-new", "log-open", "error"}, true},
		{"start-failure-close-only", "start", map[string]kernelAdmissionReply{"command-new": {}, "log-open": {}, "command-log": {}, "process-start": {Err: boom}, "log-close": {}}, nil, nil, []string{"command-new", "log-open", "command-log", "process-start", "log-close", "error"}, true},
		{"watch-order-ignores-wait-error", "watch", map[string]kernelAdmissionReply{"process-wait": {Err: boom}, "log-close": {}, "exited-close": {}, "exit-callback": {}}, nil, nil, []string{"process-wait", "log-close", "exited-close", "exit-callback"}, false},
		{"kill-error-nojoin", "stop", map[string]kernelAdmissionReply{"running": {Bool: true}, "process-kill": {Err: boom}}, nil, nil, []string{"running", "process-kill"}, true},
		{"kill-success-join", "stop", map[string]kernelAdmissionReply{"running": {Bool: true}, "process-kill": {}, "exited-join": {}}, nil, nil, []string{"running", "process-kill", "exited-join"}, false},
		{"ready-expired-noattempt", "ready", map[string]kernelAdmissionReply{"deadline": {}}, nil, nil, []string{"deadline", "before"}, true},
		{"ready-success-no-final-timecheck", "ready", map[string]kernelAdmissionReply{"deadline": {}, "dial": {}, "conn-close": {}}, []bool{true, false}, nil, []string{"deadline", "before", "dial", "conn-close"}, false},
		{"ready-failure-still-sleeps", "ready", map[string]kernelAdmissionReply{"deadline": {}, "dial": {Err: boom}, "sleep": {}}, []bool{true, false}, nil, []string{"deadline", "before", "dial", "sleep", "before"}, true},
		{"owner-ignores-protocol-state", "owner", map[string]kernelAdmissionReply{"context": {}, "context-cancel": {}, "netstat": {}, "owner-address": {Text: "127.0.0.1:123"}, "pid": {Text: "42"}}, nil, [][]string{{"UDP", "127.0.0.1:123", "0.0.0.0:0", "ANY", "42"}}, []string{"context", "netstat", "row-count", "owner-address", "row", "pid", "context-cancel"}, false},
		{"owner-invalid-row-no-pid", "owner", map[string]kernelAdmissionReply{"context": {}, "context-cancel": {}, "netstat": {}, "owner-address": {Text: "127.0.0.1:123"}}, nil, [][]string{{"TCP", "0.0.0.0:123", "0.0.0.0:0", "LISTENING", "42"}}, []string{"context", "netstat", "row-count", "owner-address", "row", "error", "context-cancel"}, true},
		{"privacy-v4-before-v6", "privacy", map[string]kernelAdmissionReply{"interface": {Bool: true, Count: 7}, "route4": {Count: 8}}, nil, nil, []string{"interface", "route4", "error"}, true},
		{"privacy-unreachable-v6", "privacy", map[string]kernelAdmissionReply{"interface": {Bool: true, Count: 7}, "route4": {Count: 7}, "route6": {Bool: true, Err: boom}}, nil, nil, []string{"interface", "route4", "route6"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &admissionIndependentFX{Replies: c.replies, Before: c.before, Rows: c.rows}
			r := run(kernelAdmissionRequest{Op: c.op, Port: 123, Timeout: time.Second}, f)
			k := []string{}
			for _, e := range f.Trace {
				k = append(k, e.Kind)
				if e.Kind == "dial" && e.Duration != 300*time.Millisecond {
					t.Fatal("dial timeout")
				}
				if e.Kind == "sleep" && e.Duration != 50*time.Millisecond {
					t.Fatal("poll delay")
				}
			}
			if (r.Err != nil) != c.err || !reflect.DeepEqual(k, c.trace) || f.Released != 1 {
				t.Fatalf("result=%v trace=%v release=%d", r.Err, k, f.Released)
			}
			if c.name == "kill-error-nojoin" && r.Err != boom {
				t.Fatal("Kill error identity lost")
			}
		})
	}
}
func TestKernelAdmissionIndependentGoldens(t *testing.T) {
	admissionIndependentGolden(t, runGoKernelAdmissionService)
}

func admissionIndependentCase(seed int) *admissionIndependentFX {
	f := &admissionIndependentFX{Replies: map[string]kernelAdmissionReply{}, Before: []bool{true, true, false}}
	for _, k := range []string{"bundle-present", "bundle-path", "home-path", "saved-path", "stat", "runner-new", "running", "command-new", "log-open", "command-log", "process-start", "publish", "exited-new", "watch-spawn", "process-wait", "log-close", "exited-close", "exit-callback", "process-kill", "exited-join", "deadline", "dial", "sleep", "conn-close", "context", "context-cancel", "netstat", "owner-address", "pid", "interface", "route4", "route6", "config-command", "output-split"} {
		f.Replies[k] = kernelAdmissionReply{}
	}
	f.Replies["bundle-present"] = kernelAdmissionReply{Bool: seed&1 != 0}
	f.Replies["bundle-path"] = kernelAdmissionReply{Text: "bundle/mihomo.exe"}
	f.Replies["home-path"] = kernelAdmissionReply{Text: "home/mihomo.exe"}
	if seed&2 != 0 {
		f.Replies["saved-path"] = kernelAdmissionReply{Text: "saved"}
	}
	f.Replies["stat"] = kernelAdmissionReply{Bool: seed&4 != 0}
	f.Replies["running"] = kernelAdmissionReply{Bool: seed&1 != 0}
	f.Replies["interface"] = kernelAdmissionReply{Bool: seed&1 != 0, Count: 7}
	f.Replies["route4"] = kernelAdmissionReply{Count: 7 + seed%2}
	f.Replies["route6"] = kernelAdmissionReply{Count: 7 + seed%3, Bool: seed&4 != 0}
	f.Replies["owner-address"] = kernelAdmissionReply{Text: "127.0.0.1:123"}
	f.Replies["pid"] = kernelAdmissionReply{Text: "42"}
	f.Rows = [][]string{{"bad"}, {"UDP", "127.0.0.1:123", "0.0.0.0:0", "ANY", "42"}}
	if seed&8 != 0 {
		f.Rows[1][4] = "0042"
	}
	if seed&16 != 0 {
		f.Before = nil
	}
	f.Replies["output-split"] = kernelAdmissionReply{Names: []string{"first", "last"}}
	fails := []string{"", "log-open", "process-start", "process-wait", "process-kill", "netstat", "stat", "interface", "route4", "route6", "config-command", "dial"}
	k := fails[seed%len(fails)]
	if k != "" {
		r := f.Replies[k]
		r.Err = fmt.Errorf("synthetic-%s", k)
		f.Replies[k] = r
	}
	return f
}

func TestKernelAdmissionIndependentSelectedLifecycle(t *testing.T) {
	admissionIndependentGolden(t, runSelectedKernelAdmissionService)
}
