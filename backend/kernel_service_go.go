package main

import (
	"errors"
)

type kernelOracle struct {
	s  *kernelServiceState
	fx kernelServiceEffects
}

func (a *kernelOracle) call(q kernelEffect) kernelReply {
	r := a.fx.Call(q)
	if r.State != nil {
		a.s = r.State
	}
	return r
}
func (a *kernelOracle) stop() error {
	if a.s.RelayPresent {
		a.call(kernelEffect{Kind: "close-app-relay"})
	}
	if a.s.HistoryPresent {
		a.call(kernelEffect{Kind: "history-collect"})
		a.call(kernelEffect{Kind: "history-stop"})
	}
	if !a.s.KernelPresent {
		return nil
	}
	err := a.call(kernelEffect{Kind: "runner-stop"}).Err
	a.call(kernelEffect{Kind: "remove-config"})
	return err
}
func runGoKernelService(q kernelServiceRequest, fx kernelServiceEffects) (result kernelServiceResult) {
	defer fx.Release()
	a := kernelOracle{fx: fx}
	a.call(kernelEffect{Kind: "snapshot"})
	if q.Op == "stop" {
		result.Err = a.stop()
		return
	}
	if q.Op != "start" {
		panic("unknown kernel operation: " + q.Op)
	}
	if a.call(kernelEffect{Kind: "running"}).Bool {
		return
	}
	if a.s.Mode != modeSystemProxy {
		if r := a.call(kernelEffect{Kind: "tun"}); r.Err != nil {
			result.Err = r.Err
			return
		}
	}
	if a.s.SubscriptionURL == "" && a.s.ManualCount == 0 {
		result.Err = errors.New(kernelServiceNoNodes)
		return
	}
	if a.s.SubscriptionURL != "" && a.s.SubscriptionURL != mergedSubscriptionURL && !a.call(kernelEffect{Kind: "profile-matches"}).Bool {
		if r := a.call(kernelEffect{Kind: "fetch"}); r.Err != nil {
			result.Err = r.Err
			return
		}
	}
	if a.call(kernelEffect{Kind: "write-manual"}).Err != nil {
		result.Err = errors.New("could not write the file for the nodes added by hand")
		return
	}
	if r := a.call(kernelEffect{Kind: "mixed-port", Port: a.s.SocksPort}); r.Err != nil {
		result.Err = r.Err
		return
	}
	for {
		r := a.call(kernelEffect{Kind: "control-port"})
		if r.Err != nil {
			result.Err = errors.New("could not allocate a local control port")
			return
		}
		if a.s.ControlPort != a.s.MixedPort {
			break
		}
	}
	profile := a.call(kernelEffect{Kind: "read-profile"})
	if profile.Err != nil && a.s.SubscriptionURL != "" {
		result.Err = errors.New("could not read the downloaded subscription")
		return
	}
	opts := a.call(kernelEffect{Kind: "options", Body: profile.Body, Bool: a.s.SubscriptionURL != ""})
	if opts.Err != nil {
		result.Err = opts.Err
		return
	}
	options := opts.Options
	optionToken := opts.Token
	a.call(kernelEffect{Kind: "china-active", Bool: options.ChinaDirect && (!options.TUN || !options.Active || options.DomesticWeb)})
	var relayToken uint64
	transferred := false
	// Explicit normal cleanup is observable; Release is an idempotent exception/ABI guard.
	defer func() {
		if relayToken != 0 && !transferred {
			a.call(kernelEffect{Kind: "close-local", Token: relayToken})
		}
	}()
	if options.TUN && options.DNSProtection {
		r := a.call(kernelEffect{Kind: "relay-new", Port: a.s.MixedPort})
		if r.Err != nil {
			result.Err = r.Err
			return
		}
		relayToken = r.Token
		a.call(kernelEffect{Kind: "options-relay", Token: optionToken, Other: relayToken})
	}
	config := a.call(kernelEffect{Kind: "generate", Token: optionToken})
	if a.call(kernelEffect{Kind: "write-config", Text: config.Text}).Err != nil {
		result.Err = errors.New("could not write Mihomo configuration")
		return
	}
	runner := a.call(kernelEffect{Kind: "runner-new"})
	if runner.Err != nil {
		result.Err = runner.Err
		return
	}
	if r := a.call(kernelEffect{Kind: "runner-start", Token: runner.Token}); r.Err != nil {
		result.Err = r.Err
		return
	}
	if a.call(kernelEffect{Kind: "ready"}).Err != nil {
		_ = a.stop()
		result.Err = errors.New("Mihomo did not become ready; see the local log")
		return
	}
	if runner.Bool && a.s.SocksPort != 0 {
		if r := a.call(kernelEffect{Kind: "owner-check", Token: runner.Token, Port: a.s.MixedPort}); r.Err != nil {
			_ = a.stop()
			result.Err = r.Err
			return
		}
	}
	if a.call(kernelEffect{Kind: "nodes-ready"}).Err != nil {
		_ = a.stop()
		result.Err = errors.New("subscription returned no selectable nodes; check the subscription and network")
		return
	}
	if options.TUN && options.Active {
		if r := a.call(kernelEffect{Kind: "privacy"}); r.Err != nil {
			_ = a.stop()
			result.Err = r.Err
			return
		}
	}
	a.call(kernelEffect{Kind: "history-start"})
	a.call(kernelEffect{Kind: "relay-transfer", Token: relayToken})
	transferred = true
	if relayToken != 0 {
		a.call(kernelEffect{Kind: "relay-monitor", Token: relayToken})
	}
	return
}

const kernelServiceNoNodes = "请先添加订阅地址，或手动添加至少一个节点"
