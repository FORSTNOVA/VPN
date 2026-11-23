package main

import (
	"errors"
	"time"
)

type kernelAdmissionOracle struct{ fx kernelAdmissionEffects }

func (o kernelAdmissionOracle) call(q kernelAdmissionEffect) kernelAdmissionReply {
	r := o.fx.Call(q)
	if r.Err != nil {
		switch q.Kind {
		case "stat", "runner-new", "log-open", "process-start", "process-wait", "log-close", "process-kill", "dial", "conn-close", "netstat", "interface", "route4", "route6", "config-command", "error":
		default:
			panic(r.Err)
		}
	}
	return r
}
func (o kernelAdmissionOracle) c(k string) kernelAdmissionReply {
	return o.call(kernelAdmissionEffect{Kind: k})
}
func (o kernelAdmissionOracle) look(name string) string {
	if o.c("bundle-present").Bool {
		p := o.call(kernelAdmissionEffect{Kind: "bundle-path", Text: name}).Text
		s := o.call(kernelAdmissionEffect{Kind: "stat", Text: p})
		if s.Err == nil && !s.Bool {
			return p
		}
	}
	return o.call(kernelAdmissionEffect{Kind: "home-path", Text: name}).Text
}
func (o kernelAdmissionOracle) path() string {
	p := o.c("saved-path")
	if p.Err != nil {
		panic(p.Err)
	}
	if p.Text != "" {
		return p.Text
	}
	return o.look("mihomo.exe")
}
func (o kernelAdmissionOracle) err(code int) error {
	return o.call(kernelAdmissionEffect{Kind: "error", Index: code}).Err
}
func runGoKernelAdmissionService(q kernelAdmissionRequest, fx kernelAdmissionEffects) (r kernelAdmissionResult) {
	defer fx.Release()
	o := kernelAdmissionOracle{fx}
	switch q.Op {
	case "path":
		r.Text = o.path()
	case "look-for":
		r.Text = o.look(q.Name)
	case "new":
		p := o.path()
		s := o.call(kernelAdmissionEffect{Kind: "stat", Text: p})
		if s.Err != nil {
			r.Err = o.call(kernelAdmissionEffect{Kind: "error", Index: 1, Text: p}).Err
			return
		}
		r.Err = o.call(kernelAdmissionEffect{Kind: "runner-new", Text: p}).Err
	case "running":
		r.Bool = o.c("running").Bool
	case "start":
		o.c("command-new")
		if x := o.c("log-open"); x.Err != nil {
			r.Err = o.err(2)
			return
		}
		o.c("command-log")
		if x := o.c("process-start"); x.Err != nil {
			o.c("log-close")
			r.Err = o.err(3)
			return
		}
		o.c("publish")
		o.c("exited-new")
		o.c("watch-spawn")
	case "watch":
		o.c("process-wait")
		o.c("log-close")
		o.c("exited-close")
		o.c("exit-callback")
	case "stop":
		if !o.c("running").Bool {
			return
		}
		r.Err = o.c("process-kill").Err
		if r.Err == nil {
			o.c("exited-join")
		}
	case "ready":
		o.call(kernelAdmissionEffect{Kind: "deadline", Duration: q.Timeout})
		for o.c("before").Bool {
			d := o.call(kernelAdmissionEffect{Kind: "dial", Duration: 300 * time.Millisecond})
			if d.Err == nil {
				o.c("conn-close")
				return
			}
			o.call(kernelAdmissionEffect{Kind: "sleep", Duration: 50 * time.Millisecond})
		}
		r.Err = errors.New("startup timeout")
	case "owner":
		o.call(kernelAdmissionEffect{Kind: "context", Duration: 5 * time.Second})
		defer o.c("context-cancel")
		x := o.c("netstat")
		if x.Err != nil {
			r.Err = o.call(kernelAdmissionEffect{Kind: "error", Index: 4}).Err
			return
		}
		rows := o.c("row-count").Count
		addr := o.call(kernelAdmissionEffect{Kind: "owner-address", Index: q.Port}).Text
		for i := 0; i < rows; i++ {
			f := o.call(kernelAdmissionEffect{Kind: "row", Index: i}).Names
			if len(f) == 5 && f[1] == addr && f[2] == "0.0.0.0:0" {
				if f[4] == o.c("pid").Text {
					return
				}
			}
		}
		r.Err = o.call(kernelAdmissionEffect{Kind: "error", Index: 5, Text: addr}).Err
	case "privacy":
		x := o.c("interface")
		if x.Err != nil || !x.Bool {
			r.Err = o.err(6)
			return
		}
		idx := x.Count
		x = o.c("route4")
		if x.Err != nil || x.Count != idx {
			r.Err = o.err(7)
			return
		}
		x = o.c("route6")
		if x.Bool {
			return
		}
		if x.Err != nil || x.Count != idx {
			r.Err = o.err(8)
			return
		}
	case "check-config":
		p := o.path()
		if p == "" {
			r.Err = errors.New("no kernel to check the configuration with")
			return
		}
		o.call(kernelAdmissionEffect{Kind: "context", Duration: 20 * time.Second})
		defer o.c("context-cancel")
		x := o.call(kernelAdmissionEffect{Kind: "config-command", Text: p})
		if x.Err != nil {
			lines := o.c("output-split").Names
			tail := ""
			if len(lines) > 0 {
				tail = lines[len(lines)-1]
			}
			if len(tail) > 300 {
				tail = tail[:300] + "…"
			}
			r.Err = o.call(kernelAdmissionEffect{Kind: "error", Index: 9, Text: tail}).Err
		}
	default:
		r.Err = errors.New("invalid kernel admission operation")
	}
	return
}
