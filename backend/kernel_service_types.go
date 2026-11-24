package main

import (
	"errors"
	"os"
	"time"
)

type kernelServiceRequest struct{ Op string }
type kernelServiceResult struct{ Err error }
type kernelServiceState struct {
	Mode, SubscriptionURL                                    string
	ManualCount, SocksPort, MixedPort, ControlPort           int
	KernelPresent, RelayPresent, HistoryPresent, ChinaActive bool
}
type kernelOptionFacts struct{ TUN, DNSProtection, Active, ChinaDirect, DomesticWeb bool }
type kernelEffect struct {
	Kind         string
	Token, Other uint64
	Port         int
	Bool         bool
	Body         []byte
	Text         string
}
type kernelReply struct {
	State   *kernelServiceState
	Err     error
	Token   uint64
	Port    int
	Bool    bool
	Body    []byte
	Text    string
	Options kernelOptionFacts
}
type kernelServiceEffects interface {
	Call(kernelEffect) kernelReply
	Release()
}
type kernelLocalRelay struct {
	relay         *adaptiveDNS
	local, closed bool
	closeFn       func()
}
type appKernelEffects struct {
	a            *app
	next         uint64
	runners      map[uint64]kernelRunner
	relays       map[uint64]*kernelLocalRelay
	options      map[uint64]configOptions
	makeRelay    func(int) (*adaptiveDNS, error)
	closeRelay   func(*adaptiveDNS)
	monitorRelay func(*adaptiveDNS)
}

func newAppKernelEffects(a *app) *appKernelEffects {
	return &appKernelEffects{a: a, runners: map[uint64]kernelRunner{}, relays: map[uint64]*kernelLocalRelay{}, options: map[uint64]configOptions{}, makeRelay: newAdaptiveDNS, closeRelay: func(r *adaptiveDNS) { r.close() }, monitorRelay: func(r *adaptiveDNS) { go r.monitor() }}
}
func (e *appKernelEffects) token() uint64 { e.next++; return e.next }
func kernelStateFromApp(a *app) *kernelServiceState {
	return &kernelServiceState{Mode: a.connectionModeLocked(), SubscriptionURL: a.controlMetaLocked().SubscriptionURL, ManualCount: a.manualCountLocked(), SocksPort: a.controlMetaLocked().SocksPort, MixedPort: a.mixedPort, ControlPort: a.ctrlPort, KernelPresent: a.kernel != nil, RelayPresent: a.dnsRelay != nil, HistoryPresent: a.trafficHistoryCancel != nil, ChinaActive: a.chinaStateLocked().Active}
}
func (e *appKernelEffects) closeLocal(token uint64) {
	slot := e.relays[token]
	if slot == nil {
		panic("unknown local relay token")
	}
	if slot.local && !slot.closed {
		slot.closed = true
		slot.local = false
		slot.closeFn()
	}
}
func (e *appKernelEffects) Release() {
	for token := range e.relays {
		e.closeLocal(token)
	}
}
func (e *appKernelEffects) Call(q kernelEffect) (r kernelReply) {
	// This invocation borrows its caller's app mutex. It never unlocks it.
	defer func() { r.State = kernelStateFromApp(e.a) }()
	switch q.Kind {
	case "snapshot":
	case "running":
		r.Bool = e.a.kernelRunningLocked()
	case "tun":
		r.Err = e.a.tunUnavailableLocked()
	case "profile-matches":
		r.Bool = e.a.profileMatchesSubscription()
	case "fetch":
		_, r.Err = e.a.fetchProfileLocked()
	case "write-manual":
		r.Err = e.a.writeManualProviderLocked()
	case "mixed-port":
		r.Port, r.Err = proxyListenPort(q.Port)
		e.a.mixedPort = r.Port
	case "control-port":
		r.Port, r.Err = freeLoopbackPort()
		e.a.ctrlPort = r.Port
	case "read-profile":
		r.Body, r.Err = os.ReadFile(e.a.profilePath())
	case "options":
		var options configOptions
		options, r.Err = e.a.kernelConfigOptions(q.Body, q.Bool)
		if r.Err == nil {
			r.Token = e.token()
			e.options[r.Token] = options
			r.Options = kernelOptionFacts{TUN: options.TUN, DNSProtection: options.DNSProtection, Active: options.active(), ChinaDirect: options.ChinaDirect, DomesticWeb: options.DomesticWeb}
		}
	case "china-active":
		e.a.setChinaActiveLocked(q.Bool)
	case "relay-new":
		var relay *adaptiveDNS
		relay, r.Err = e.makeRelay(q.Port)
		if r.Err == nil {
			r.Token = e.token()
			e.relays[r.Token] = &kernelLocalRelay{relay: relay, local: true, closeFn: func() { e.closeRelay(relay) }}
		}
	case "options-relay":
		slot := e.relays[q.Other]
		options, ok := e.options[q.Token]
		if !ok || slot == nil || slot.closed {
			panic("invalid options/relay token")
		}
		options.dnsRelayAddr = slot.relay.listener.Addr().String()
		e.options[q.Token] = options
	case "generate":
		options, ok := e.options[q.Token]
		if !ok {
			panic("unknown config token")
		}
		r.Text = generatedConfig(options)
	case "write-config":
		r.Err = os.WriteFile(e.a.configPath(), []byte(q.Text), 0600)
	case "runner-new":
		// A Rust worker can exit while the handle and original fd still need reaping.
		if _, ok := e.a.kernel.(interface{ standaloneTUN() }); ok {
			r.Err = e.a.kernel.Stop()
			e.a.kernel = nil
			if r.Err != nil {
				return
			}
		}
		var runner kernelRunner
		runner, r.Err = e.a.newKernel()
		if r.Err == nil {
			r.Token = e.token()
			e.runners[r.Token] = runner
			_, r.Bool = runner.(interface{ verifyPortOwner(int) error })
		}
	case "runner-start":
		runner, ok := e.runners[q.Token]
		if !ok {
			panic("unknown runner token")
		}
		r.Err = runner.Start()
		// Publish before acknowledging a successful Start, so no ABI gap can lose ownership.
		if r.Err == nil {
			_, r.Bool = runner.(interface{ standaloneTUN() })
			e.a.kernel = runner
			e.a.started = time.Now()
		}
	case "ready":
		r.Err = e.a.waitForMihomo(10 * time.Second)
	case "owner-check":
		runner, ok := e.runners[q.Token]
		if !ok {
			panic("unknown runner token")
		}
		r.Err = runner.(interface{ verifyPortOwner(int) error }).verifyPortOwner(q.Port)
	case "nodes-ready":
		r.Err = e.a.waitForNodesLocked(20 * time.Second)
	case "privacy":
		r.Err = e.a.verifyPrivacyTunnel()
	case "history-start":
		e.a.startTrafficHistoryLocked()
	case "relay-transfer":
		if q.Token == 0 {
			e.a.dnsRelay = nil
		} else {
			slot := e.relays[q.Token]
			if slot == nil || slot.closed {
				panic("unknown transfer token")
			}
			e.a.dnsRelay = slot.relay
			slot.local = false
		}
	case "relay-monitor":
		slot := e.relays[q.Token]
		if slot == nil || slot.local || slot.closed {
			panic("unowned relay monitor")
		}
		e.monitorRelay(slot.relay)
	case "close-local":
		e.closeLocal(q.Token)
	case "close-app-relay":
		e.a.dnsRelay.close()
		e.a.dnsRelay = nil
	case "history-collect":
		e.a.collectTrafficHistoryLocked()
	case "history-stop":
		e.a.stopTrafficHistoryLocked()
	case "runner-stop":
		r.Err = e.a.kernel.Stop()
		e.a.kernel = nil
		e.a.setChinaActiveLocked(false)
	case "remove-config":
		e.a.removeConfigLocked()
	default:
		panic("unknown kernel effect: " + q.Kind)
	}
	return
}
func (a *app) runKernelService(q kernelServiceRequest) (out kernelServiceResult) {
	defer func() {
		if recover() != nil {
			out = kernelServiceResult{Err: errors.New("kernel operation interrupted")}
		}
	}()
	fx := newAppKernelEffects(a)
	defer fx.Release()
	return runSelectedKernelService(q, fx)
}
