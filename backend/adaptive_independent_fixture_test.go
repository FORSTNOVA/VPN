package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

var adaptiveIndependentErr = errors.New("synthetic adaptive failure")

type adaptiveIndependentFX struct {
	Seed                                         int
	Fail                                         string
	Trace                                        []adaptiveServiceEffect
	Waits, Checks, Monitors, Released, Finalized int
	Blocked                                      bool
}

func (f *adaptiveIndependentFX) Release() { f.Released++ }
func (f *adaptiveIndependentFX) FinalizeAdaptiveResult(_ adaptiveServiceRequest, r adaptiveServiceResult) adaptiveServiceResult {
	f.Finalized++
	return r
}
func (f *adaptiveIndependentFX) Call(e adaptiveServiceEffect) (r adaptiveServiceReply) {
	f.Trace = append(f.Trace, e)
	if e.Kind == f.Fail {
		r.Err = adaptiveIndependentErr
	}
	switch e.Kind {
	case "pack", "request", "body-read", "listen", "exchange-child", "reject":
	case "do":
		r.Code = 200
		if f.Seed%7 == 0 {
			r.Code = 503
		}
	case "unpack":
		r.Code = []int{0, 2, 3, 5}[f.Seed%4]
		r.Bits = uint64(f.Seed % 9)
		r.QCount = 1
		r.ACount = 1
		r.Value = int64([]int{40, 65535, 65536}[f.Seed%3])
	case "preferred":
		r.Index = f.Seed % 2
	case "head-wait":
		r.Text = []string{"done", "timer", "cancel"}[f.Seed%3]
		r.Flag = f.Seed%4 == 0
		r.Index = f.Seed % 2
	case "resolve-wait":
		f.Waits++
		r.Text = "done"
		if f.Seed%7 == 0 {
			r.Text = "cancel"
		}
		r.Flag = f.Waits == 1 && f.Seed%3 != 0
		r.Index = (f.Seed + f.Waits) % 2
	case "check-wait":
		r.Index = (f.Seed + f.Checks) % 2
		f.Checks++
		r.Flag = (f.Seed+r.Index)%3 != 0
		r.Value = int64(time.Duration((f.Seed+r.Index*17)%1000) * time.Millisecond)
	case "cas":
		r.Flag = f.Seed%2 == 0
	case "monitor-wait":
		f.Monitors++
		r.Flag = f.Monitors <= f.Seed%3
	case "custom-present":
		r.Flag = f.Seed%5 == 0
	case "custom-confirm":
		r.Flag = f.Seed%2 == 0
	case "blocked":
		r.Flag = f.Seed%7 == 0
	case "running":
		r.Flag = f.Seed%3 != 0
	case "probe-config":
		r.Index = f.Seed % 3
		r.Flag = f.Seed%2 == 0
		r.Count = 3
	case "site":
		r.Text = []string{"error", "skipped", "", "passed", "blocked"}[(f.Seed+e.Index)%5]
	case "note-present":
		r.Flag = f.Seed%2 == 0
	case "set-blocked":
		f.Blocked = true
	case "elapsed":
		r.Value = int64(f.Seed)
	case "failure":
		r.Err = fmt.Errorf("synthetic DNS error %d", e.Index)
	case "answer-accept", "body-close", "context", "context-cancel", "completion-new", "resolve-start", "timer-new", "timer-stop", "switch-log", "probe-name", "check-start", "answer-failure", "answer-write", "ticker-new", "ticker-stop", "check-child", "clock-start", "probe-question", "anomaly-log", "note-anomaly", "reject-log", "set-reason", "set-switch-time", "clear-note", "event", "relay-context", "client", "server", "server-start", "server-ready", "relay-cancel", "server-shutdown", "idle-close":
	default:
		panic("unsafe adaptive port " + e.Kind)
	}
	return
}

type adaptiveIndependentRunner func(adaptiveServiceRequest, adaptiveServiceEffects) adaptiveServiceResult

func adaptiveIndependentMatrix(t *testing.T, run adaptiveIndependentRunner) {
	ops := []string{"exchange", "resolve", "switch", "serve", "monitor", "check", "check-worker", "confirm", "note", "block", "create", "close"}
	fails := []string{"", "pack", "request", "do", "body-read", "unpack", "exchange-child", "reject", "listen"}
	for i := 0; i < 2160; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			q := adaptiveServiceRequest{Op: ops[i%len(ops)], Index: i % 2, From: i % 2, To: (i / 2) % 2}
			a := &adaptiveIndependentFX{Seed: i, Fail: fails[(i/12)%len(fails)]}
			b := *a
			ga := runGoAdaptiveService(q, a)
			gb := run(q, &b)
			if fmt.Sprint(ga.Err) != fmt.Sprint(gb.Err) || ga.Bool != gb.Bool || ga.Value != gb.Value || !reflect.DeepEqual(a, &b) {
				t.Fatalf("%s seed%d\ngo=%+v actual=%+v\nGO=%+v\nACT=%+v", q.Op, i, ga, gb, a, &b)
			}
		})
	}
}
func TestAdaptiveIndependentSelectedMatrix(t *testing.T) {
	adaptiveIndependentMatrix(t, runSelectedAdaptiveService)
}

type adaptiveGoldenFX struct {
	Replies  map[string][]adaptiveServiceReply
	Trace    []adaptiveServiceEffect
	Released int
}

func (f *adaptiveGoldenFX) Release() { f.Released++ }
func (f *adaptiveGoldenFX) Call(e adaptiveServiceEffect) adaptiveServiceReply {
	f.Trace = append(f.Trace, e)
	v, ok := f.Replies[e.Kind]
	if !ok {
		panic("unexpected golden effect " + e.Kind)
	}
	if len(v) == 0 {
		return adaptiveServiceReply{}
	}
	r := v[0]
	if len(v) > 1 {
		f.Replies[e.Kind] = v[1:]
	}
	return r
}
func adaptiveIndependentGoldens(t *testing.T, run adaptiveIndependentRunner) {
	cases := []struct {
		name, op string
		replies  map[string][]adaptiveServiceReply
		want     []string
		boolean  bool
	}{
		{"preferred-fast", "resolve", map[string][]adaptiveServiceReply{"context": {}, "completion-new": {}, "preferred": {{Index: 1}}, "resolve-start": {}, "timer-new": {}, "head-wait": {{Text: "done", Flag: true, Index: 1}}, "timer-stop": {}, "context-cancel": {}}, []string{"context", "completion-new", "preferred", "resolve-start", "timer-new", "head-wait", "timer-stop", "context-cancel"}, true},
		{"custom-precedes-blocked", "confirm", map[string][]adaptiveServiceReply{"custom-present": {{Flag: true}}, "custom-confirm": {{Flag: true}}}, []string{"custom-present", "custom-confirm"}, true},
		{"blocked-precedes-running", "confirm", map[string][]adaptiveServiceReply{"custom-present": {}, "blocked": {{Flag: true}}}, []string{"custom-present", "blocked"}, false},
		{"android-second-target", "confirm", map[string][]adaptiveServiceReply{"custom-present": {}, "blocked": {}, "running": {{Flag: true}}, "probe-config": {{Index: 7890, Count: 3, Flag: true}}, "site": {{Text: "error"}, {Text: "blocked"}}}, []string{"custom-present", "blocked", "running", "probe-config", "site", "site"}, true},
		{"passed-keeps-pin", "block", map[string][]adaptiveServiceReply{"custom-present": {{Flag: true}}, "custom-confirm": {{Flag: true}}, "note-present": {}, "anomaly-log": {}, "note-anomaly": {}}, []string{"custom-present", "custom-confirm", "note-present", "anomaly-log", "note-anomaly"}, false},
		{"reject-error-no-state", "block", map[string][]adaptiveServiceReply{"custom-present": {{Flag: true}}, "custom-confirm": {}, "reject": {{Err: adaptiveIndependentErr}}, "reject-log": {}}, []string{"custom-present", "custom-confirm", "reject", "reject-log"}, false},
		{"reject-success-state-order", "block", map[string][]adaptiveServiceReply{"custom-present": {{Flag: true}}, "custom-confirm": {}, "reject": {}, "set-blocked": {}, "set-reason": {}, "set-switch-time": {}, "clear-note": {}, "event": {}}, []string{"custom-present", "custom-confirm", "reject", "set-blocked", "set-reason", "set-switch-time", "clear-note", "event"}, false},
		{"note-existing-no-log", "note", map[string][]adaptiveServiceReply{"note-present": {{Flag: true}}, "note-anomaly": {}}, []string{"note-present", "note-anomaly"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &adaptiveGoldenFX{Replies: c.replies}
			r := run(adaptiveServiceRequest{Op: c.op}, f)
			var kinds []string
			for _, e := range f.Trace {
				kinds = append(kinds, e.Kind)
				if e.Kind == "site" && e.Duration != 6*time.Second {
					t.Fatal("Android timeout")
				}
			}
			if r.Err != nil || r.Bool != c.boolean || !reflect.DeepEqual(kinds, c.want) || f.Released != 1 {
				t.Fatalf("golden result%+v trace%v", r, kinds)
			}
		})
	}
	for _, d := range []time.Duration{500 * time.Millisecond, 500*time.Millisecond + 1} {
		f := &adaptiveGoldenFX{Replies: map[string][]adaptiveServiceReply{"context": {}, "preferred": {}, "probe-name": {}, "completion-new": {}, "check-start": {}, "check-wait": {{Index: 1, Flag: true, Value: 100}, {Index: 0, Flag: true, Value: int64(d)}}, "cas": {{Flag: true}}, "switch-log": {}, "context-cancel": {}}}
		run(adaptiveServiceRequest{Op: "check"}, f)
		cas := 0
		wait := 0
		for _, e := range f.Trace {
			if e.Kind == "check-wait" {
				wait++
			}
			if e.Kind == "cas" {
				cas++
				if wait != 2 {
					t.Fatal("check switched before2completions")
				}
			}
		}
		want := 0
		if d > 500*time.Millisecond {
			want = 1
		}
		if cas != want {
			t.Fatal("strict slow boundary", d, cas)
		}
	}
}
func TestAdaptiveIndependentSelectedGoldens(t *testing.T) {
	adaptiveIndependentGoldens(t, runSelectedAdaptiveService)
}
