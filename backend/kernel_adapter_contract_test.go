package main

import (
	"errors"
	"github.com/miekg/dns"
	"net/http"
	"testing"
	"time"
)

type migrationKernelStub struct {
	Starts, Stops         int
	StartError, StopError error
}

func (k *migrationKernelStub) Running() bool { return false }
func (k *migrationKernelStub) Start() error  { k.Starts++; return k.StartError }
func (k *migrationKernelStub) Stop() error   { k.Stops++; return k.StopError }

// Only snapshot and injected stub runner ports execute. No newKernel, DNS relay,
// file, listener, readiness, history, monitor or platform operation is reachable.
func TestMigrationKernelSafeAdapterPublicationAndBorrowedLock(t *testing.T) {
	for _, failStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failStart], func(t *testing.T) {
			a := testChinaApp(&app{started: time.Unix(7, 0)}, true)
			a.mu.Lock()
			defer a.mu.Unlock()
			fx := newAppKernelEffects(a)
			k := &migrationKernelStub{StopError: errors.New("synthetic stop")}
			if failStart {
				k.StartError = errors.New("synthetic start")
			}
			fx.runners[12] = k
			reply := fx.Call(kernelEffect{Kind: "runner-start", Token: 12})
			if !errors.Is(reply.Err, k.StartError) {
				t.Fatal("Start error identity changed")
			}
			if failStart {
				if a.kernel != nil || !a.started.Equal(time.Unix(7, 0)) {
					t.Fatal("failed Start published state")
				}
			} else {
				if a.kernel != k || a.started.Equal(time.Unix(7, 0)) {
					t.Fatal("successful Start did not publish before reply")
				}
				stamp := a.started
				out := fx.Call(kernelEffect{Kind: "runner-stop"})
				if out.Err != k.StopError || a.kernel != nil || a.chinaStateLocked().Active || !a.started.Equal(stamp) {
					t.Fatal("Stop error/state contract changed")
				}
			}
			fx.Release()
			fx.Release()
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Fatal("Release unlocked caller-owned mutex")
			}
			if k.Starts != 1 || k.Stops != map[bool]int{false: 1, true: 0}[failStart] {
				t.Fatal("runner operation repeated")
			}
		})
	}
}

type migrationKernelNoNetwork struct{ Calls, Closes int }

func (r *migrationKernelNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	r.Calls++
	return nil, errors.New("network forbidden in relay ownership test")
}
func (r *migrationKernelNoNetwork) CloseIdleConnections() { r.Closes++ }
func TestMigrationKernelSafeRelayOwnership(t *testing.T) {
	// miekg/dns v1.1.63 server.go ShutdownContext returns immediately when
	// !started, before accessing Listener/PacketConn. These servers are never activated.
	for _, scenario := range []string{"local-close", "release-only", "transfer", "transfer-zero"} {
		t.Run(scenario, func(t *testing.T) {
			oldCount, newCount := 0, 0
			oldHTTP, newHTTP := &migrationKernelNoNetwork{}, &migrationKernelNoNetwork{}
			old := &adaptiveDNS{server: &dns.Server{}, client: &http.Client{Transport: oldHTTP}, cancel: func() { oldCount++ }}
			local := &adaptiveDNS{server: &dns.Server{}, client: &http.Client{Transport: newHTTP}, cancel: func() { newCount++ }}
			a := &app{dnsRelay: old}
			a.mu.Lock()
			defer a.mu.Unlock()
			fx := newAppKernelEffects(a)
			fx.relays[99] = &kernelLocalRelay{relay: local, local: true, closeFn: local.close}
			switch scenario {
			case "local-close":
				fx.Call(kernelEffect{Kind: "close-local", Token: 99})
				fx.Call(kernelEffect{Kind: "close-local", Token: 99})
			case "transfer":
				fx.Call(kernelEffect{Kind: "relay-transfer", Token: 99})
			case "transfer-zero":
				fx.Call(kernelEffect{Kind: "relay-transfer", Token: 0})
			}
			fx.Release()
			fx.Release()
			if oldCount != 0 || oldHTTP.Closes != 0 || oldHTTP.Calls != 0 || newHTTP.Calls != 0 {
				t.Fatal("ownership cleanup touched old relay or made network request")
			}
			if scenario == "transfer" {
				if newCount != 0 || newHTTP.Closes != 0 || a.dnsRelay != local {
					t.Fatal("transferred relay incorrectly closed")
				}
				fx.Call(kernelEffect{Kind: "close-app-relay"})
				fx.Release()
				if newCount != 1 || a.dnsRelay != nil {
					t.Fatal("app-owned relay close/release repeated")
				}
			} else {
				if newCount != 1 || newHTTP.Closes != 1 {
					t.Fatal("local relay cleanup not exactly once")
				}
				if scenario == "transfer-zero" {
					if a.dnsRelay != nil {
						t.Fatal("zero transfer did not clear app relay")
					}
				} else if a.dnsRelay != old {
					t.Fatal("local cleanup replaced existing app relay")
				}
			}
			if a.mu.TryLock() {
				a.mu.Unlock()
				t.Fatal("relay cleanup unlocked caller")
			}
		})
	}
}
