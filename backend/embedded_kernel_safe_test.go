package main

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type embeddedKernelSafeState struct {
	mu        sync.Mutex
	running   bool
	tun       string
	file, sub *int
	events    []string
	writes    []string
	trace     []string
	failure   string
	err       error
	handover  int
	spawned   int
}
type embeddedKernelSafeFX struct {
	s         *embeddedKernelSafeState
	op        string
	owned     bool
	file, sub *int
	event     string
	at        int
}

func (f *embeddedKernelSafeFX) Release() {
	if f.op == "running" && f.owned {
		f.owned = false
		f.s.mu.Unlock()
	}
}
func (f *embeddedKernelSafeFX) child(op string) embeddedKernelResult {
	return runEmbeddedKernelOperation(embeddedKernelRequest{Op: op}, &embeddedKernelSafeFX{s: f.s, op: op})
}
func (f *embeddedKernelSafeFX) Call(e embeddedKernelEffect) embeddedKernelReply {
	s := f.s
	s.trace = append(s.trace, e.Kind)
	if e.Kind == s.failure {
		return embeddedKernelReply{Err: s.err}
	}
	switch e.Kind {
	case "chdir", "home", "mkdir", "read-config", "hub-parse", "parse-config", "parse-live", "apply-config", "shutdown", "redactor":
	case "start-logging":
		return embeddedKernelReply{Err: f.child("start-logging").Err}
	case "stop-logging":
		return embeddedKernelReply{Err: f.child("stop-logging").Err}
	case "refusal":
		return embeddedKernelReply{Err: f.child("refusal").Err}
	case "handover":
		s.handover++
	case "lock":
		s.mu.Lock()
		f.owned = true
	case "unlock":
		f.owned = false
		s.mu.Unlock()
	case "set-running":
		s.running = e.Number != 0
	case "running":
		return embeddedKernelReply{Flag: s.running}
	case "sleep":
		if e.Number != 200 {
			panic("wrong refusal wait")
		}
	case "tun-error":
		return embeddedKernelReply{Text: s.tun}
	case "open-log":
		if e.Number != 0600 {
			panic("wrong log mode")
		}
		f.file = new(int)
	case "subscribe":
		f.sub = new(int)
	case "publish-log":
		s.file, s.sub = f.file, f.sub
	case "clear-tun-error":
		s.tun = ""
	case "spawn-logs":
		s.spawned++
	case "next-event":
		if f.at == len(s.events) {
			return embeddedKernelReply{}
		}
		f.event = s.events[f.at]
		f.at++
		return embeddedKernelReply{Flag: true}
	case "handle-event":
		return embeddedKernelReply{Err: runEmbeddedKernelOperation(embeddedKernelRequest{Op: "event"}, &embeddedKernelSafeFX{s: s, op: "event", event: f.event}).Err}
	case "event-facts":
		return embeddedKernelReply{Number: int64(strings.Index(f.event, "TUN listening error"))}
	case "tun-empty":
		return embeddedKernelReply{Flag: s.tun == ""}
	case "capture-tun":
		s.tun = f.event
	case "write-log":
		s.writes = append(s.writes, f.event)
	case "snapshot-log":
		f.file, f.sub = s.file, s.sub
		mask := int64(0)
		if f.sub != nil {
			mask |= 1
		}
		if f.file != nil {
			mask |= 2
		}
		return embeddedKernelReply{Number: mask}
	case "clear-log":
		s.file, s.sub = nil, nil
	case "unsubscribe":
		if s.file != nil || s.sub != nil || f.sub == nil {
			panic("snapshot/nil/order lost")
		}
	case "close-log":
		if s.file != nil || s.sub != nil || f.file == nil {
			panic("snapshot/nil/order lost")
		}
	default:
		panic("unsafe embedded effect: " + e.Kind)
	}
	return embeddedKernelReply{}
}
func TestEmbeddedKernelServiceStartFailureOrder(t *testing.T) {
	for _, failure := range []string{"chdir", "mkdir", "read-config", "hub-parse"} {
		s := &embeddedKernelSafeState{failure: failure, err: errors.New("original sentinel")}
		r := runEmbeddedKernelOperation(embeddedKernelRequest{Op: "start"}, &embeddedKernelSafeFX{s: s, op: "start"})
		if r.Err == nil || s.running || s.handover != 0 {
			t.Fatal("failed start admitted")
		}
		if failure != "read-config" && !errors.Is(r.Err, s.err) {
			t.Fatal("wrapped error identity changed")
		}
		if failure == "hub-parse" {
			last := s.trace[len(s.trace)-2:]
			if !reflect.DeepEqual(last, []string{"unsubscribe", "close-log"}) {
				t.Fatal("parse cleanup order")
			}
			for _, v := range s.trace {
				if v == "shutdown" {
					t.Fatal("parse error invented shutdown")
				}
			}
		}
	}
}
func TestEmbeddedKernelServiceLoggingAndRefusalQuirks(t *testing.T) {
	s := &embeddedKernelSafeState{tun: "old TUN listening error", failure: "open-log", err: errors.New("open")}
	r := runEmbeddedKernelOperation(embeddedKernelRequest{Op: "start"}, &embeddedKernelSafeFX{s: s, op: "start"})
	if r.Err == nil || s.tun != "old TUN listening error" || s.running || s.handover != 0 {
		t.Fatal("failed logging cleared old refusal")
	}
	if !strings.Contains(strings.Join(s.trace, ","), "sleep,tun-error,shutdown,stop-logging") {
		t.Fatal("refusal cleanup sequence")
	}
	s = &embeddedKernelSafeState{tun: "old"}
	r = runEmbeddedKernelOperation(embeddedKernelRequest{Op: "start"}, &embeddedKernelSafeFX{s: s, op: "start"})
	if r.Err != nil || s.tun != "" || !s.running || s.handover != 1 || s.spawned != 1 {
		t.Fatal("successful start state")
	}
	tail := s.trace[len(s.trace)-4:]
	if !reflect.DeepEqual(tail, []string{"handover", "lock", "set-running", "unlock"}) {
		t.Fatal("FD before running order")
	}
	runEmbeddedKernelOperation(embeddedKernelRequest{Op: "stop"}, &embeddedKernelSafeFX{s: s, op: "stop"})
	if s.running || s.file != nil || s.sub != nil {
		t.Fatal("stop publication")
	}
}
func TestEmbeddedKernelServiceFirstTunEventAndStream(t *testing.T) {
	s := &embeddedKernelSafeState{events: []string{"normal", "first TUN listening error", "second TUN listening error", "tail"}}
	r := runEmbeddedKernelOperation(embeddedKernelRequest{Op: "logs"}, &embeddedKernelSafeFX{s: s, op: "logs"})
	if r.Err != nil || s.tun != s.events[1] || !reflect.DeepEqual(s.writes, s.events) {
		t.Fatal("first error capture/log delivery changed")
	}
}
func TestEmbeddedKernelServiceConfigAdmission(t *testing.T) {
	for _, op := range []string{"check", "apply"} {
		for _, fail := range []string{"read-config", "parse-config", "parse-live"} {
			s := &embeddedKernelSafeState{failure: fail, err: errors.New("original")}
			r := runEmbeddedKernelOperation(embeddedKernelRequest{Op: op}, &embeddedKernelSafeFX{s: s, op: op})
			if op == "apply" && fail == "parse-live" || op == "check" && (fail == "read-config" || fail == "parse-config") {
				if r.Err != s.err {
					t.Fatal("config error identity")
				}
				if s.trace[len(s.trace)-1] != fail {
					t.Fatal("operation continued after parse failure")
				}
			}
		}
	}
}
func TestEmbeddedKernelServiceSnapshotMasksAndRunning(t *testing.T) {
	for mask := 0; mask < 4; mask++ {
		s := &embeddedKernelSafeState{running: true}
		if mask&1 != 0 {
			s.sub = new(int)
		}
		if mask&2 != 0 {
			s.file = new(int)
		}
		runEmbeddedKernelOperation(embeddedKernelRequest{Op: "stop-logging"}, &embeddedKernelSafeFX{s: s, op: "stop-logging"})
		want := []string{"lock", "snapshot-log", "clear-log", "unlock"}
		if mask&1 != 0 {
			want = append(want, "unsubscribe")
		}
		if mask&2 != 0 {
			want = append(want, "close-log")
		}
		if !reflect.DeepEqual(s.trace, want) {
			t.Fatal("snapshot mask cleanup changed")
		}
		r := runEmbeddedKernelOperation(embeddedKernelRequest{Op: "running"}, &embeddedKernelSafeFX{s: s, op: "running"})
		if r.Status != 1 || !s.mu.TryLock() {
			t.Fatal("running lost value or unlock")
		}
		s.mu.Unlock()
	}
}
