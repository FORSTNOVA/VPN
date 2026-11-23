package main

import "time"

type adaptiveOracle struct {
	q  adaptiveServiceRequest
	fx adaptiveServiceEffects
}

func (o *adaptiveOracle) call(q adaptiveServiceEffect) adaptiveServiceReply {
	r := o.fx.Call(q)
	if r.Err != nil {
		switch q.Kind {
		case "pack", "request", "do", "body-read", "unpack", "failure", "exchange-child", "reject", "listen":
		default:
			panic(r.Err)
		}
	}
	return r
}
func (o *adaptiveOracle) c(k string) adaptiveServiceReply {
	return o.call(adaptiveServiceEffect{Kind: k})
}
func (o *adaptiveOracle) i(k string, i int) adaptiveServiceReply {
	return o.call(adaptiveServiceEffect{Kind: k, Index: i})
}
func (o *adaptiveOracle) d(k string, d time.Duration) adaptiveServiceReply {
	return o.call(adaptiveServiceEffect{Kind: k, Duration: d})
}
func (o *adaptiveOracle) exchange() (out adaptiveServiceResult) {
	if r := o.c("pack"); r.Err != nil {
		return adaptiveServiceResult{Err: r.Err}
	}
	if r := o.i("request", o.q.Index); r.Err != nil {
		return adaptiveServiceResult{Err: r.Err}
	}
	r := o.c("do")
	if r.Err != nil {
		return adaptiveServiceResult{Err: r.Err}
	}
	defer o.c("body-close")
	if r.Code != 200 {
		return adaptiveServiceResult{Err: o.i("failure", r.Code).Err}
	}
	if r = o.c("body-read"); r.Err != nil {
		return adaptiveServiceResult{Err: r.Err}
	}
	r = o.c("unpack")
	if r.Err != nil {
		return adaptiveServiceResult{Err: r.Err}
	}
	if r.Value > 65535 || r.Bits != 7 || r.QCount != 1 || r.ACount != 1 || r.Code == 2 || r.Code == 5 {
		return adaptiveServiceResult{Err: o.i("failure", -1).Err}
	}
	o.c("answer-accept")
	return adaptiveServiceResult{Bool: true}
}
func (o *adaptiveOracle) change(from, to int) {
	if from != to && o.call(adaptiveServiceEffect{Kind: "cas", Index: from, Count: to}).Flag {
		o.call(adaptiveServiceEffect{Kind: "switch-log", Index: from, Count: to})
	}
}
func (o *adaptiveOracle) resolve() bool {
	o.d("context", 5*time.Second)
	defer o.c("context-cancel")
	o.c("completion-new")
	first := o.c("preferred").Index
	o.i("resolve-start", first)
	o.d("timer-new", 500*time.Millisecond)
	defer o.c("timer-stop")
	pending := 2
	r := o.c("head-wait")
	switch r.Text {
	case "done":
		if r.Flag {
			return true
		}
		pending--
	case "cancel":
		return false
	}
	o.i("resolve-start", 1-first)
	for ; pending > 0; pending-- {
		r = o.c("resolve-wait")
		if r.Text == "cancel" {
			return false
		}
		if r.Flag {
			o.change(first, r.Index)
			return true
		}
	}
	return false
}
func (o *adaptiveOracle) check() {
	o.d("context", 5*time.Second)
	defer o.c("context-cancel")
	first := o.c("preferred").Index
	o.c("probe-name")
	o.c("completion-new")
	o.i("check-start", 0)
	o.i("check-start", 1)
	var healthy [2]bool
	var elapsed [2]int64
	for i := 0; i < 2; i++ {
		r := o.c("check-wait")
		healthy[r.Index] = r.Flag
		elapsed[r.Index] = r.Value
	}
	if healthy[1-first] && (!healthy[first] || (elapsed[first] > int64(500*time.Millisecond) && elapsed[1-first] < elapsed[first])) {
		o.change(first, 1-first)
	}
}
func (o *adaptiveOracle) confirm() bool {
	if o.c("custom-present").Flag {
		return o.c("custom-confirm").Flag
	}
	if o.c("blocked").Flag {
		return false
	}
	if !o.c("running").Flag {
		return false
	}
	r := o.c("probe-config")
	if r.Index == 0 {
		return false
	}
	timeout := confirmProbeTimeout
	if r.Flag {
		timeout = 6 * time.Second
	}
	for i := 0; i < r.Count; i++ {
		v := o.call(adaptiveServiceEffect{Kind: "site", Index: i, Duration: timeout})
		if v.Text != "" && v.Text != "error" && v.Text != "skipped" {
			return true
		}
	}
	return false
}
func (o *adaptiveOracle) note() {
	if !o.c("note-present").Flag {
		o.c("anomaly-log")
	}
	o.c("note-anomaly")
}
func runGoAdaptiveService(q adaptiveServiceRequest, fx adaptiveServiceEffects) (out adaptiveServiceResult) {
	defer fx.Release()
	defer func() {
		if p := recover(); p != nil {
			panic(p)
		}
		if f, ok := fx.(adaptiveFinalizer); ok {
			out = f.FinalizeAdaptiveResult(q, out)
		}
	}()
	o := adaptiveOracle{q, fx}
	switch q.Op {
	case "exchange":
		return o.exchange()
	case "resolve":
		out.Bool = o.resolve()
	case "switch":
		o.change(q.From, q.To)
	case "serve":
		if !o.resolve() {
			o.c("answer-failure")
		}
		o.c("answer-write")
	case "monitor":
		o.d("ticker-new", time.Minute)
		defer o.c("ticker-stop")
		for o.c("monitor-wait").Flag {
			o.c("check-child")
		}
	case "check":
		o.check()
	case "check-worker":
		o.c("clock-start")
		o.c("probe-question")
		r := o.c("exchange-child")
		out.Bool = r.Err == nil
		if out.Bool {
			out.Value = o.c("elapsed").Value
		}
	case "confirm":
		out.Bool = o.confirm()
	case "note":
		o.note()
	case "block":
		if o.confirm() {
			o.note()
			return
		}
		if o.c("reject").Err != nil {
			o.c("reject-log")
			return
		}
		o.c("set-blocked")
		o.c("set-reason")
		o.c("set-switch-time")
		o.c("clear-note")
		o.c("event")
	case "create":
		if r := o.c("listen"); r.Err != nil {
			return adaptiveServiceResult{Err: r.Err}
		}
		o.c("relay-context")
		o.c("client")
		o.c("server")
		o.c("server-start")
		o.c("server-ready")
	case "close":
		o.c("relay-cancel")
		o.c("server-shutdown")
		o.c("idle-close")
	}
	return
}
