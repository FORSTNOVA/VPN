package main

import (
	"fmt"
	"reflect"
	"testing"
)

type embeddedIndependentFX struct {
	Trace   []embeddedKernelEffect
	Replies map[string]embeddedKernelReply
	Events  int
	ErrText string
	Running bool
}

func (f *embeddedIndependentFX) Release() {}
func (f *embeddedIndependentFX) Call(e embeddedKernelEffect) embeddedKernelReply {
	f.Trace = append(f.Trace, e)
	switch e.Kind {
	case "next-event":
		if f.Events > 0 {
			f.Events--
			return embeddedKernelReply{Flag: true}
		}
		return embeddedKernelReply{}
	case "clear-tun-error":
		f.ErrText = ""
	case "capture-tun":
		f.ErrText = "first synthetic TUN listening error"
	case "tun-error":
		return embeddedKernelReply{Text: f.ErrText}
	case "tun-empty":
		return embeddedKernelReply{Flag: f.ErrText == ""}
	case "set-running":
		f.Running = e.Number != 0
	case "running":
		return embeddedKernelReply{Flag: f.Running}
	}
	r, ok := f.Replies[e.Kind]
	if !ok {
		panic("unexpected embedded effect " + e.Kind)
	}
	return r
}
func embeddedIndependentCase(i int) *embeddedIndependentFX {
	f := &embeddedIndependentFX{Replies: map[string]embeddedKernelReply{}, Events: i % 4, Running: i&1 != 0}
	for _, k := range []string{"chdir", "home", "mkdir", "read-config", "start-logging", "hub-parse", "stop-logging", "refusal", "shutdown", "handover", "lock", "unlock", "set-running", "sleep", "open-log", "redactor", "subscribe", "publish-log", "clear-tun-error", "spawn-logs", "handle-event", "event-facts", "capture-tun", "write-log", "snapshot-log", "clear-log", "unsubscribe", "close-log", "parse-config", "parse-live", "apply-config"} {
		f.Replies[k] = embeddedKernelReply{}
	}
	if i&2 != 0 {
		f.ErrText = "old synthetic refusal"
	}
	for _, kind := range []string{"start-monitor", "stop-monitor", "check-generation"} {
		f.Replies[kind] = embeddedKernelReply{}
	}
	if i&16 != 0 {
		f.Replies["check-generation"] = embeddedKernelReply{Err: fmt.Errorf("retired tunnel")}
	}
	f.Replies["event-facts"] = embeddedKernelReply{Number: int64(i%3 - 1)}
	f.Replies["snapshot-log"] = embeddedKernelReply{Number: int64(i % 4)}
	ks := []string{"", "chdir", "mkdir", "read-config", "hub-parse", "refusal", "open-log", "parse-config", "parse-live"}
	if k := ks[i%len(ks)]; k != "" {
		f.Replies[k] = embeddedKernelReply{Err: fmt.Errorf("synthetic %s", k)}
	}
	return f
}
func embeddedIndependentGoldens(t *testing.T, run func(embeddedKernelRequest, embeddedKernelEffects) embeddedKernelResult) {
	cases := []struct {
		op   string
		seed int
		want []string
		old  bool
	}{
		{"start-logging", 6, []string{"open-log"}, true},
		{"start-logging", 0, []string{"open-log", "redactor", "subscribe", "lock", "publish-log", "clear-tun-error", "unlock", "spawn-logs"}, false},
		{"start", 4, []string{"chdir", "home", "mkdir", "read-config", "start-logging", "hub-parse", "stop-logging"}, false},
		{"start", 5, []string{"chdir", "home", "mkdir", "read-config", "start-logging", "hub-parse", "refusal", "shutdown", "stop-logging"}, false},
		{"start", 0, []string{"chdir", "home", "mkdir", "read-config", "start-logging", "hub-parse", "refusal", "handover", "lock", "set-running", "unlock"}, false},
		{"stop-logging", 3, []string{"lock", "snapshot-log", "clear-log", "unlock", "unsubscribe", "close-log"}, false},
		{"event", 2, []string{"event-facts", "lock", "tun-empty", "unlock", "write-log"}, true},
		{"apply", 8, []string{"parse-live"}, false},
		{"session-start", 0, []string{"chdir", "home", "mkdir", "read-config", "start-logging", "hub-parse", "refusal", "handover", "lock", "set-running", "unlock", "start-monitor"}, false},
		{"session-stop", 0, []string{"stop-monitor", "shutdown", "stop-logging", "lock", "set-running", "unlock"}, false},
		{"session-apply", 16, []string{"check-generation"}, false},
		{"session-apply", 8, []string{"check-generation", "parse-live"}, false},
		{"session-apply", 0, []string{"check-generation", "parse-live", "apply-config"}, false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%d", c.op, c.seed), func(t *testing.T) {
			f := embeddedIndependentCase(c.seed)
			old := f.ErrText
			run(embeddedKernelRequest{Op: c.op}, f)
			ks := []string{}
			for _, e := range f.Trace {
				ks = append(ks, e.Kind)
				if e.Kind == "mkdir" && e.Number != 0700 {
					t.Fatal("mkdir mode")
				}
				if e.Kind == "open-log" && e.Number != 0600 {
					t.Fatal("log mode")
				}
			}
			if !reflect.DeepEqual(ks, c.want) {
				t.Fatalf("expected %v got %v", c.want, ks)
			}
			if c.old && f.ErrText != old {
				t.Fatal("old TUN error replaced")
			}
		})
	}
}
func TestEmbeddedKernelIndependentGoldens(t *testing.T) {
	embeddedIndependentGoldens(t, runGoEmbeddedKernel)
}

func TestEmbeddedKernelIndependentSelectedLifecycle(t *testing.T) {
	embeddedIndependentGoldens(t, runSelectedEmbeddedKernel)
}
