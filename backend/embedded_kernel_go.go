package main

import (
	"errors"
	"fmt"
)

func runGoEmbeddedKernel(q embeddedKernelRequest, fx embeddedKernelEffects) embeddedKernelResult {
	a := embeddedKernelOracle{fx}
	call := func(k string) embeddedKernelReply { return a.call(k, "", "", 0) }
	done := func(r embeddedKernelReply) embeddedKernelResult { return embeddedKernelResult{Err: r.Err} }
	switch q.Op {
	case "session-start", "start":
		if r := call("chdir"); r.Err != nil {
			return embeddedKernelResult{Err: fmt.Errorf(embeddedKernelTexts[0], r.Err)}
		}
		call("home")
		if r := a.call("mkdir", "", "", 0700); r.Err != nil {
			return embeddedKernelResult{Err: fmt.Errorf(embeddedKernelTexts[1], r.Err)}
		}
		if call("read-config").Err != nil {
			return embeddedKernelResult{Err: errors.New(embeddedKernelTexts[4])}
		}
		call("start-logging")
		if r := call("hub-parse"); r.Err != nil {
			call("stop-logging")
			return embeddedKernelResult{Err: fmt.Errorf(embeddedKernelTexts[2], r.Err)}
		}
		if r := call("refusal"); r.Err != nil {
			call("shutdown")
			call("stop-logging")
			return done(r)
		}
		call("handover")
		call("lock")
		a.call("set-running", "", "", 1)
		call("unlock")
		if q.Op == "session-start" {
			call("start-monitor")
		}
		return embeddedKernelResult{}
	case "session-stop", "stop":
		if q.Op == "session-stop" {
			call("stop-monitor")
		}
		call("shutdown")
		call("stop-logging")
		call("lock")
		a.call("set-running", "", "", 0)
		call("unlock")
		return embeddedKernelResult{}
	case "running":
		call("lock")
		r := call("running")
		call("unlock")
		if r.Flag {
			return embeddedKernelResult{Status: 1}
		}
		return embeddedKernelResult{}
	case "refusal":
		a.call("sleep", "", "", 200)
		r := call("tun-error")
		if r.Text != "" {
			return embeddedKernelResult{Err: fmt.Errorf(embeddedKernelTexts[3], r.Text)}
		}
		return embeddedKernelResult{}
	case "start-logging":
		if a.call("open-log", "", "", 0600).Err != nil {
			return embeddedKernelResult{}
		}
		call("redactor")
		call("subscribe")
		call("lock")
		call("publish-log")
		call("clear-tun-error")
		call("unlock")
		call("spawn-logs")
		return embeddedKernelResult{}
	case "logs":
		for call("next-event").Flag {
			call("handle-event")
		}
		return embeddedKernelResult{}
	case "event":
		if call("event-facts").Number >= 0 {
			call("lock")
			if call("tun-empty").Flag {
				call("capture-tun")
			}
			call("unlock")
		}
		call("write-log")
		return embeddedKernelResult{}
	case "stop-logging":
		call("lock")
		r := call("snapshot-log")
		call("clear-log")
		call("unlock")
		if r.Number&1 != 0 {
			call("unsubscribe")
		}
		if r.Number&2 != 0 {
			call("close-log")
		}
		return embeddedKernelResult{}
	case "check":
		if r := call("read-config"); r.Err != nil {
			return done(r)
		}
		return done(call("parse-config"))
	case "session-apply", "apply":
		if q.Op == "session-apply" {
			if r := call("check-generation"); r.Err != nil {
				return done(r)
			}
		}
		if r := call("parse-live"); r.Err != nil {
			return done(r)
		}
		a.call("apply-config", "", "", 0)
		return embeddedKernelResult{}
	}
	panic("unknown embedded kernel operation")
}
