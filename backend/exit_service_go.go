package main

import (
	"fmt"
)

func runGoExitService(_ exitServiceRequest, fx exitServiceEffects) exitServiceResult {
	defer fx.Release()
	state := fx.Call(exitEffect{Kind: "lock"}).State
	if !state.Matches {
		return exitServiceResult{}
	}
	call := func(q exitEffect) exitReply {
		r := fx.Call(q)
		if r.State != nil {
			state = r.State
		}
		return r
	}
	if state.RelayPresent {
		call(exitEffect{Kind: "close-relay"})
	}
	call(exitEffect{Kind: "history-stop"})
	call(exitEffect{Kind: "clear-kernel"})
	call(exitEffect{Kind: "remove-config"})
	if state.PatrolPresent {
		call(exitEffect{Kind: "close-patrol"})
	}
	call(exitEffect{Kind: "clear-block"})
	if state.ProxyOn {
		r := call(exitEffect{Kind: "proxy-off"})
		if r.Err != nil {
			call(exitEffect{Kind: "log", Message: fmt.Sprintf("could not restore system proxy after Mihomo exit: %v", r.Err)})
		}
		call(exitEffect{Kind: "clear-proxy"})
	}
	call(exitEffect{Kind: "drop-tunnel"})
	return exitServiceResult{}
}
