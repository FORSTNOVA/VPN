package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

var bootIndependentErr = errors.New("synthetic bootstrap failure")

type bootIndependentFX struct {
	Seed                                                   int
	Fail                                                   string
	Trace                                                  []bootstrapServiceEffect
	Reads, Attempts, Before, Requests, Releases, Finalized int
	Held, Transferred                                      bool
}

func (f *bootIndependentFX) Release() {
	f.Releases++
	if !f.Transferred {
		f.Held = false
	}
}
func (f *bootIndependentFX) FinalizeBootstrapResult(q bootstrapServiceRequest, r bootstrapServiceResult) bootstrapServiceResult {
	f.Finalized++
	if r.Lock && r.Err == nil {
		f.Transferred = true
	}
	return r
}
func (f *bootIndependentFX) Call(e bootstrapServiceEffect) (r bootstrapServiceReply) {
	f.Trace = append(f.Trace, e)
	if e.Kind == f.Fail {
		r.Err = bootIndependentErr
	}
	switch e.Kind {
	case "record-read":
		f.Reads++
		if f.Seed%7 == 0 || f.Reads > 3 {
			r.Err = bootIndependentErr
		}
	case "record-decode", "executable", "executable-stat", "service-decode", "record-encode", "record-temp", "record-rename", "lock-open", "home-mkdir", "config-home", "new-service", "prepare", "token", "serve", "owner-key", "owner-write", "proxy-execute", "identify-request", "identify-decode", "portable-stat", "portable-mkdir", "portable-write", "tunnel-decode":
		if e.Kind == "executable-stat" {
			r.Value = int64(f.Seed % 4)
			r.OtherValue = 3
		}
		if e.Kind == "identify-decode" {
			r.Flag = f.Seed%2 == 0
		}
	case "record-valid":
		r.Flag = f.Seed%5 != 0
	case "service-request":
		f.Requests++
		if f.Requests > 5 {
			r.Err = bootIndependentErr
		}
	case "service-read", "response-status":
		r.Count = 200
		if f.Seed%3 == 0 {
			r.Count = 503
		}
	case "service-facts":
		r.Value = int64(f.Seed % 3)
		r.OtherValue = 3
		r.Connected = f.Seed%2 == 0
		r.Elevated = f.Seed%4 == 0
	case "lock-try":
		f.Attempts++
		if f.Attempts <= f.Seed%3 {
			r.Err = bootIndependentErr
			r.Flag = f.Seed%5 != 1
		}
	case "lock-acquired":
		f.Held = true
	case "lock-release", "lock-close":
		f.Held = false
	case "before":
		f.Before++
		r.Flag = f.Before <= f.Seed%3
	case "proxy-state":
		r.Flag = f.Seed%2 == 0
		r.Other = f.Seed%3 != 0
	case "owner-read":
		r.Flag = f.Seed%4 != 0
		r.Other = f.Seed%3 == 0
	case "owner-address", "bundle-present", "record-own", "bridge-init", "android-current", "kernel-running":
		r.Flag = f.Seed%2 == 0
	case "proxy-port":
		r.Count = []int{0, 7891, -1}[f.Seed%3]
	case "android-detach":
		r.Flag = f.Seed%2 == 0
		r.Other = f.Seed%3 == 0
	case "failure":
		r.Err = fmt.Errorf("synthetic failure %d", e.Count)
	case "drop":
	case "body-close", "deadline", "sleep", "identify-client", "idle-close", "owner-key-close", "owner-delete", "proxy-script", "proxy-script-fallback", "portable-remove", "portable-select", "record-remove", "android-home", "bundle-dir", "default-home", "open-log", "elevated", "log", "announce", "android-publish", "record-build", "signal-wait", "server-close", "cleanup", "service-lock", "service-unlock", "app-lock", "app-unlock", "socket-hook", "android-reuse", "tunnel-grant", "tunnel-release", "permission":
	default:
		panic("unsafe bootstrap port " + e.Kind)
	}
	return
}

type bootIndependentRunner func(bootstrapServiceRequest, bootstrapServiceEffects) bootstrapServiceResult

func bootIndependentMatrix(t *testing.T, run bootIndependentRunner) {
	ops := []string{"join", "lock", "running", "probe", "handover", "identity", "owner", "identify", "proxy", "clear-owner", "portable", "write-record", "remove-record", "windows", "android-start", "android-stop", "jni-start", "tunnel"}
	fails := []string{"", "record-read", "record-decode", "service-request", "service-read", "service-decode", "lock-open", "executable-stat", "proxy-state", "proxy-execute", "owner-write", "owner-key", "identify-request", "identify-decode", "config-home", "home-mkdir", "new-service", "prepare", "token", "serve", "record-temp", "record-rename", "tunnel-decode", "drop", "proxy-script"}
	for i := 0; i < 2160; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			q := bootstrapServiceRequest{Op: ops[i%18], Elevated: i%2 == 0, Enable: i%3 == 0, Authorized: i%4 == 0, Wait: time.Duration(i%3) * time.Second, ShutdownWait: time.Second}
			a := &bootIndependentFX{Seed: i, Fail: fails[(i/18)%len(fails)]}
			b := *a
			ga := runGoBootstrapService(q, a)
			gb := run(q, &b)
			if fmt.Sprint(ga.Err) != fmt.Sprint(gb.Err) || ga.Bool != gb.Bool || ga.Reuse != gb.Reuse || ga.Lock != gb.Lock || !reflect.DeepEqual(a, &b) {
				t.Fatalf("%s seed%d fail%s\ngo=%+v actual=%+v\nGO%+v\nACT%+v", q.Op, i, a.Fail, ga, gb, a, &b)
			}
		})
	}
}
func TestBootstrapIndependentSelectedMatrix(t *testing.T) {
	bootIndependentMatrix(t, runSelectedBootstrapService)
}

type bootGoldenFX struct {
	Reply               map[string][]bootstrapServiceReply
	Trace               []bootstrapServiceEffect
	Released, Finalized int
}

func (f *bootGoldenFX) Release() { f.Released++ }
func (f *bootGoldenFX) FinalizeBootstrapResult(_ bootstrapServiceRequest, r bootstrapServiceResult) bootstrapServiceResult {
	f.Finalized++
	return r
}
func (f *bootGoldenFX) Call(e bootstrapServiceEffect) bootstrapServiceReply {
	f.Trace = append(f.Trace, e)
	v, ok := f.Reply[e.Kind]
	if !ok {
		panic("unapproved golden port " + e.Kind)
	}
	if len(v) == 0 {
		return bootstrapServiceReply{}
	}
	r := v[0]
	if len(v) > 1 {
		f.Reply[e.Kind] = v[1:]
	}
	return r
}
func bootIndependentGoldens(t *testing.T, run bootIndependentRunner) {
	cases := []struct {
		name, op             string
		auth                 bool
		reply                map[string][]bootstrapServiceReply
		want                 []string
		reuse, lock, boolean bool
	}{
		{name: "probe-malformed-alive", op: "probe", reply: map[string][]bootstrapServiceReply{"record-valid": {{Flag: true}}, "service-request": {}, "service-read": {{Count: 200, Err: bootIndependentErr}}, "service-decode": {{Err: bootIndependentErr}}, "body-close": {}}, want: []string{"record-valid", "service-request", "service-read", "service-decode", "body-close"}, boolean: true},
		{name: "handover-zero-wait-probes", op: "handover", reply: map[string][]bootstrapServiceReply{"service-request": {{Err: bootIndependentErr}}, "deadline": {}, "record-valid": {{Flag: false}}}, want: []string{"service-request", "deadline", "record-valid"}, boolean: true},
		{name: "join-double-check-new", op: "join", reply: map[string][]bootstrapServiceReply{"record-read": {{Err: bootIndependentErr}}, "lock-open": {}, "deadline": {}, "lock-try": {}, "lock-acquired": {}}, want: []string{"record-read", "lock-open", "deadline", "lock-try", "lock-acquired", "record-read"}, lock: true},
		{name: "join-second-check-reuse", op: "join", reply: map[string][]bootstrapServiceReply{"record-read": {{Err: bootIndependentErr}, {}}, "record-decode": {}, "record-valid": {{Flag: true}}, "service-request": {}, "service-read": {{Count: 200}}, "service-decode": {}, "body-close": {}, "service-facts": {{Connected: true}}, "lock-open": {}, "deadline": {}, "lock-try": {}, "lock-acquired": {}, "lock-release": {}}, want: []string{"record-read", "lock-open", "deadline", "lock-try", "lock-acquired", "record-read", "record-decode", "record-valid", "service-request", "service-read", "service-decode", "body-close", "service-facts", "lock-release"}, reuse: true},
		{name: "clear-invalid-deletes", op: "clear-owner", reply: map[string][]bootstrapServiceReply{"owner-read": {{Flag: false}}, "owner-key": {}, "owner-delete": {{Err: bootIndependentErr}}, "owner-key-close": {}}, want: []string{"owner-read", "owner-key", "owner-delete", "owner-key-close"}},
		{name: "clear-other-preserved", op: "clear-owner", reply: map[string][]bootstrapServiceReply{"owner-read": {{Flag: true, Other: false}}}, want: []string{"owner-read"}},
		{name: "identify-status-before-decode", op: "identify", reply: map[string][]bootstrapServiceReply{"identify-client": {}, "identify-request": {}, "response-status": {{Count: 503}}, "body-close": {}, "idle-close": {}}, want: []string{"identify-client", "identify-request", "response-status", "body-close", "idle-close"}},
		{name: "android-stop-order", op: "android-stop", reply: map[string][]bootstrapServiceReply{"service-lock": {}, "android-detach": {{Flag: true, Other: true}}, "service-unlock": {}, "app-lock": {}, "drop": {{Err: bootIndependentErr}}, "app-unlock": {}, "cleanup": {}, "server-close": {}, "record-read": {{Err: bootIndependentErr}}}, want: []string{"service-lock", "android-detach", "service-unlock", "app-lock", "drop", "app-unlock", "cleanup", "server-close", "record-read"}},
		{name: "tunnel-loss-holds-lock", op: "tunnel", reply: map[string][]bootstrapServiceReply{"tunnel-decode": {{Err: bootIndependentErr}}, "log": {}, "tunnel-release": {}, "service-lock": {}, "android-current": {{Flag: true}}, "service-unlock": {}, "app-lock": {}, "permission": {}, "kernel-running": {{Flag: true}}, "app-unlock": {}, "drop": {{Err: bootIndependentErr}}}, want: []string{"tunnel-decode", "log", "service-lock", "android-current", "app-lock", "service-unlock", "tunnel-release", "permission", "kernel-running", "drop", "app-unlock", "log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &bootGoldenFX{Reply: c.reply}
			r := run(bootstrapServiceRequest{Op: c.op, Authorized: c.auth}, f)
			var kinds []string
			for _, e := range f.Trace {
				kinds = append(kinds, e.Kind)
			}
			if !reflect.DeepEqual(kinds, c.want) || r.Err != nil || r.Reuse != c.reuse || r.Lock != c.lock || r.Bool != c.boolean || f.Released != 1 || f.Finalized != 1 {
				t.Fatalf("golden %+v trace%v final%d release%d", r, kinds, f.Finalized, f.Released)
			}
		})
	}
}
func TestBootstrapIndependentSelectedGoldens(t *testing.T) {
	bootIndependentGoldens(t, runSelectedBootstrapService)
}

func bootIndependentScriptFallback(t *testing.T, run bootIndependentRunner) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			f := &bootGoldenFX{Reply: map[string][]bootstrapServiceReply{"proxy-script": {}, "proxy-script-fallback": {}, "proxy-port": {{Count: 0}}, "proxy-execute": {}, "owner-write": {{Err: bootIndependentErr}}, "log": {}}}
			if missing {
				f.Reply["proxy-script"] = []bootstrapServiceReply{{Err: bootIndependentErr}}
			}
			r := run(bootstrapServiceRequest{Op: "proxy", Enable: true}, f)
			want := []string{"proxy-script"}
			if missing {
				want = append(want, "proxy-script-fallback")
			}
			want = append(want, "proxy-port", "proxy-execute", "owner-write", "log")
			var kinds []string
			execute := 0
			for _, e := range f.Trace {
				kinds = append(kinds, e.Kind)
				if e.Kind == "proxy-execute" {
					execute++
					if e.Count != 7890 || !e.Flag {
						t.Fatal("default port/enable", e)
					}
				}
			}
			if r.Err != nil || !reflect.DeepEqual(kinds, want) || execute != 1 || f.Released != 1 {
				t.Fatalf("script fallback result%+v trace%v executions%d", r, kinds, execute)
			}
		})
	}
}
func TestBootstrapIndependentSelectedScriptFallback(t *testing.T) {
	bootIndependentScriptFallback(t, runSelectedBootstrapService)
}
