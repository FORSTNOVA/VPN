package main

import (
	"errors"
	"reflect"
	"testing"
)

type kernelMemoryEffects struct {
	State                                 kernelServiceState
	Options                               kernelOptionFacts
	Trace                                 []kernelEffect
	Fail                                  map[string]error
	Controls                              []int
	Local, Transferred, Config, Started   bool
	LocalCloses, OldCloses, Starts, Stops int
	Owner                                 bool
}

func kernelFixture() *kernelMemoryEffects {
	return &kernelMemoryEffects{State: kernelServiceState{Mode: modeSystemProxy, SubscriptionURL: mergedSubscriptionURL, SocksPort: 7777}, Options: kernelOptionFacts{TUN: true, DNSProtection: true, Active: true, ChinaDirect: true}, Owner: true}
}
func (f *kernelMemoryEffects) Release() {
	if f.Local {
		f.Local = false
		f.LocalCloses++
	}
}
func (f *kernelMemoryEffects) Call(q kernelEffect) (r kernelReply) {
	f.Trace = append(f.Trace, q)
	r.Err = f.Fail[q.Kind]
	switch q.Kind {
	case "running":
		r.Bool = false
	case "profile-matches":
		r.Bool = true
	case "mixed-port":
		r.Port = 7777
		f.State.MixedPort = r.Port
	case "control-port":
		r.Port = 8888
		if len(f.Controls) > 0 {
			r.Port = f.Controls[0]
			f.Controls = f.Controls[1:]
		}
		f.State.ControlPort = r.Port
	case "read-profile":
		r.Body = []byte("synthetic")
	case "options":
		r.Token = 11
		r.Options = f.Options
	case "relay-new":
		if r.Err == nil {
			r.Token = 22
			f.Local = true
		}
	case "generate":
		r.Text = "synthetic config"
	case "write-config":
		if r.Err == nil {
			f.Config = true
		}
	case "runner-new":
		r.Token = 33
		r.Bool = f.Owner
	case "runner-start":
		f.Starts++
		if r.Err == nil {
			f.State.KernelPresent = true
			f.Started = true
		}
	case "china-active":
		f.State.ChinaActive = q.Bool
	case "runner-stop":
		f.Stops++
		f.State.KernelPresent = false
		f.State.ChinaActive = false
	case "close-app-relay":
		f.OldCloses++
		f.State.RelayPresent = false
	case "history-stop":
		f.State.HistoryPresent = false
	case "history-start":
		f.State.HistoryPresent = true
	case "relay-transfer":
		f.State.RelayPresent = q.Token != 0
		f.Local = false
		f.Transferred = q.Token != 0
	case "close-local":
		f.Release()
	case "remove-config":
		f.Config = false
	}
	state := f.State
	r.State = &state
	return
}
func TestKernelServiceFailureMatrix(t *testing.T) {
	for _, kind := range []string{"write-manual", "mixed-port", "control-port", "read-profile", "options", "relay-new", "write-config", "runner-new", "runner-start", "ready", "owner-check", "nodes-ready", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			f := kernelFixture()
			injected := errors.New("injected:" + kind)
			f.Fail = map[string]error{kind: injected}
			r := runGoKernelService(kernelServiceRequest{Op: "start"}, f)
			if r.Err == nil {
				t.Fatal("missing failure", kind)
			}
			afterStart := kind == "ready" || kind == "owner-check" || kind == "nodes-ready" || kind == "privacy"
			if afterStart && f.Stops != 1 {
				t.Fatal("published failure must stop once", f.Stops)
			}
			if !afterStart && f.Stops != 0 {
				t.Fatal("prepublication failure stopped runner", f.Stops)
			}
			if (kind == "runner-new" || kind == "runner-start") && !f.Config {
				t.Fatal("prepublication failure removed config")
			}
			if afterStart && f.Config {
				t.Fatal("published failure kept config")
			}
			if f.Local || f.LocalCloses > 1 {
				t.Fatal("relay leak/doubleclose", f.Local, f.LocalCloses)
			}
			if kind == "owner-check" || kind == "privacy" || kind == "runner-start" {
				if r.Err != injected {
					t.Fatal("original error replaced", r.Err)
				}
			}
		})
	}
}
func TestKernelServiceRelayOwnershipAndDuplicatePort(t *testing.T) {
	f := kernelFixture()
	f.Controls = []int{7777, 7777, 8888}
	f.State.RelayPresent = true
	r := runGoKernelService(kernelServiceRequest{Op: "start"}, f)
	f.Release()
	f.Release()
	if r.Err != nil || f.Starts != 1 || f.Stops != 0 || !f.Transferred || f.LocalCloses != 0 || f.OldCloses != 0 || !f.State.RelayPresent {
		t.Fatal(r, f)
	}
	controls := 0
	for _, q := range f.Trace {
		if q.Kind == "control-port" {
			controls++
		}
	}
	if controls != 3 {
		t.Fatal(controls)
	}
	f = kernelFixture()
	f.State.RelayPresent = true
	f.Fail = map[string]error{"ready": errors.New("failed")}
	runGoKernelService(kernelServiceRequest{Op: "start"}, f)
	if f.LocalCloses != 1 || f.OldCloses != 1 {
		t.Fatal("old/new relay ownership confused", f.LocalCloses, f.OldCloses)
	}
}
func TestKernelServiceStopNilAndError(t *testing.T) {
	f := kernelFixture()
	f.State.KernelPresent = false
	f.State.RelayPresent = true
	f.State.HistoryPresent = true
	f.State.ChinaActive = true
	f.Config = true
	f.Started = true
	r := runGoKernelService(kernelServiceRequest{Op: "stop"}, f)
	if r.Err != nil || f.Stops != 0 || !f.State.ChinaActive || !f.Config || !f.Started || f.State.HistoryPresent || f.State.RelayPresent {
		t.Fatal(r, f)
	}
	kinds := []string{}
	for _, q := range f.Trace {
		kinds = append(kinds, q.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"snapshot", "close-app-relay", "history-collect", "history-stop"}) {
		t.Fatal(kinds)
	}
	f.State.KernelPresent = true
	failure := errors.New("stop rejected")
	f.Fail = map[string]error{"runner-stop": failure}
	r = runGoKernelService(kernelServiceRequest{Op: "stop"}, f)
	if r.Err != failure || f.State.KernelPresent || f.State.ChinaActive || f.Config || !f.Started {
		t.Fatal(r, f)
	}
}
func TestKernelServiceExecutorReleaseIsIdempotentAndBorrowsLock(t *testing.T) {
	a := &app{}
	a.mu.Lock()
	defer a.mu.Unlock()
	fx := newAppKernelEffects(a)
	closed := 0
	fx.relays[1] = &kernelLocalRelay{local: true, closeFn: func() { closed++ }}
	fx.Release()
	fx.Release()
	if closed != 1 {
		t.Fatal(closed)
	}
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("released caller lock")
	}
	fx.relays[2] = &kernelLocalRelay{local: false, closeFn: func() { t.Fatal("closed transferred relay") }}
	fx.Release()
	fx.relays[3] = &kernelLocalRelay{local: true, closeFn: func() { closed++; panic("close panic") }}
	func() { defer func() { _ = recover() }(); fx.Release() }()
	fx.Release()
	if closed != 2 {
		t.Fatal("panic close retried", closed)
	}
}

func TestKernelServiceExecutorRelayHandoffFaults(t *testing.T) {
	for _, stage := range []string{"created", "transferred", "monitored"} {
		t.Run(stage, func(t *testing.T) {
			a := &app{}
			a.mu.Lock()
			defer a.mu.Unlock()
			fx := newAppKernelEffects(a)
			closed, monitored := 0, 0
			relay := &adaptiveDNS{}
			fx.makeRelay = func(int) (*adaptiveDNS, error) { return relay, nil }
			fx.closeRelay = func(r *adaptiveDNS) {
				if r != relay {
					t.Fatal("wrong resource")
				}
				closed++
			}
			fx.monitorRelay = func(r *adaptiveDNS) {
				if r != relay {
					t.Fatal("wrong monitor")
				}
				monitored++
			}
			func() {
				defer func() { _ = recover() }()
				defer fx.Release()
				reply := fx.Call(kernelEffect{Kind: "relay-new"})
				if stage == "created" {
					panic("ABI reply lost before caller stores token")
				}
				fx.Call(kernelEffect{Kind: "relay-transfer", Token: reply.Token})
				// An oracle whose transfer acknowledgment was lost may still attempt its defer cleanup.
				defer fx.Call(kernelEffect{Kind: "close-local", Token: reply.Token})
				if stage == "transferred" {
					panic("transfer response lost")
				}
				fx.Call(kernelEffect{Kind: "relay-monitor", Token: reply.Token})
				panic("monitor response lost")
			}()
			fx.Release()
			fx.Release()
			if stage == "created" {
				if closed != 1 || a.dnsRelay != nil {
					t.Fatal(closed, a.dnsRelay)
				}
			} else if closed != 0 || a.dnsRelay != relay {
				t.Fatal("transferred ownership closed", closed, a.dnsRelay)
			}
			if (stage == "monitored" && monitored != 1) || (stage != "monitored" && monitored != 0) {
				t.Fatal(monitored)
			}
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Fatal("caller lock released")
			}
		})
	}
}
