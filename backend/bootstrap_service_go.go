package main

import "time"

type bootstrapOracle struct {
	q  bootstrapServiceRequest
	fx bootstrapServiceEffects
}

func (o *bootstrapOracle) c(k string) bootstrapServiceReply {
	return o.fx.Call(bootstrapServiceEffect{Kind: k})
}
func (o *bootstrapOracle) n(k string, n int) bootstrapServiceReply {
	return o.fx.Call(bootstrapServiceEffect{Kind: k, Count: n})
}
func (o *bootstrapOracle) duration(k string, d time.Duration) bootstrapServiceReply {
	return o.fx.Call(bootstrapServiceEffect{Kind: k, Duration: d})
}
func (o *bootstrapOracle) fail(n int) error { return o.n("failure", n).Err }
func (o *bootstrapOracle) identity() bootstrapServiceReply {
	if o.c("executable").Err != nil {
		return bootstrapServiceReply{}
	}
	r := o.c("executable-stat")
	if r.Err != nil {
		return bootstrapServiceReply{}
	}
	return r
}
func (o *bootstrapOracle) probe() bool {
	if !o.c("record-valid").Flag {
		return false
	}
	if o.n("service-request", 0).Err != nil {
		return false
	}
	defer o.c("body-close")
	r := o.c("service-read")
	if r.Count != 200 {
		return false
	}
	o.c("service-decode")
	return true
}
func (o *bootstrapOracle) running() bool {
	if o.c("record-read").Err != nil {
		return false
	}
	if o.c("record-decode").Err != nil {
		return false
	}
	return o.probe()
}
func (o *bootstrapOracle) takeover() bool {
	r := o.c("service-facts")
	if r.Value != 0 {
		s := o.identity()
		if s.Value != 0 && (s.Value != r.Value || s.OtherValue != r.OtherValue) && !r.Connected {
			return true
		}
	}
	return o.q.Elevated && !r.Elevated
}
func (o *bootstrapOracle) lock() error {
	if r := o.c("lock-open"); r.Err != nil {
		return r.Err
	}
	o.duration("deadline", o.q.Wait)
	for {
		r := o.c("lock-try")
		if r.Err == nil {
			o.c("lock-acquired")
			return nil
		}
		if !r.Flag {
			o.c("lock-close")
			return o.fail(1)
		}
		if !o.c("before").Flag {
			o.c("lock-close")
			return o.fail(2)
		}
		o.duration("sleep", 50*time.Millisecond)
	}
}
func (o *bootstrapOracle) handover() bool {
	if o.n("service-request", 1).Err == nil {
		o.c("body-close")
	}
	o.duration("deadline", o.q.ShutdownWait)
	for {
		if !o.probe() {
			return true
		}
		if !o.c("before").Flag {
			return false
		}
		o.duration("sleep", 200*time.Millisecond)
	}
}
func (o *bootstrapOracle) join() bootstrapServiceResult {
	if o.running() && !o.takeover() {
		return bootstrapServiceResult{Reuse: true}
	}
	if err := o.lock(); err != nil {
		return bootstrapServiceResult{Err: err}
	}
	if !o.running() {
		return bootstrapServiceResult{Lock: true}
	}
	if !o.takeover() {
		o.c("lock-release")
		return bootstrapServiceResult{Reuse: true}
	}
	o.n("log", 0)
	if !o.handover() {
		o.c("lock-release")
		return bootstrapServiceResult{Err: o.fail(3)}
	}
	return bootstrapServiceResult{Lock: true}
}
func (o *bootstrapOracle) identify() bool {
	o.c("identify-client")
	defer o.c("idle-close")
	if o.c("identify-request").Err != nil {
		return false
	}
	defer o.c("body-close")
	if o.c("response-status").Count != 200 {
		return false
	}
	r := o.c("identify-decode")
	return r.Err == nil && r.Flag
}
func (o *bootstrapOracle) owner() bool {
	r := o.c("proxy-state")
	if r.Err != nil || !r.Flag || !r.Other {
		return false
	}
	r = o.c("owner-read")
	if !r.Flag || r.Other {
		return false
	}
	if !o.c("owner-address").Flag {
		return false
	}
	return o.identify()
}
func (o *bootstrapOracle) clearOwner() {
	r := o.c("owner-read")
	if r.Flag && !r.Other {
		return
	}
	if o.c("owner-key").Err != nil {
		return
	}
	defer o.c("owner-key-close")
	o.c("owner-delete")
}
func (o *bootstrapOracle) proxy() error {
	if o.c("proxy-script").Err != nil {
		o.c("proxy-script-fallback")
	}
	r := o.c("proxy-port")
	port := r.Count
	if port == 0 {
		port = 7890
	}
	if o.fx.Call(bootstrapServiceEffect{Kind: "proxy-execute", Flag: o.q.Enable, Count: port}).Err != nil {
		return o.fail(4)
	}
	if o.q.Enable {
		if o.c("owner-write").Err != nil {
			o.n("log", 1)
		}
	} else {
		o.clearOwner()
	}
	return nil
}
func (o *bootstrapOracle) portable() bool {
	if !o.c("bundle-present").Flag {
		return false
	}
	if o.c("portable-stat").Err != nil {
		return false
	}
	if o.c("portable-mkdir").Err != nil {
		o.n("log", 2)
		return false
	}
	if o.c("portable-write").Err != nil {
		o.n("log", 3)
		return false
	}
	o.c("portable-remove")
	o.c("portable-select")
	return true
}
func (o *bootstrapOracle) writeRecord() error {
	if r := o.c("record-encode"); r.Err != nil {
		return r.Err
	}
	if r := o.c("record-temp"); r.Err != nil {
		return r.Err
	}
	return o.c("record-rename").Err
}
func (o *bootstrapOracle) removeRecord() {
	if o.c("record-read").Err != nil {
		return
	}
	r := o.c("record-decode")
	if r.Err == nil && !o.c("record-own").Flag {
		return
	}
	o.c("record-remove")
}
func (o *bootstrapOracle) start(android bool) error {
	if android {
		o.c("android-home")
	} else {
		if r := o.c("config-home"); r.Err != nil {
			return r.Err
		}
		o.c("bundle-dir")
		o.c("default-home")
		o.portable()
	}
	if r := o.c("home-mkdir"); r.Err != nil {
		return r.Err
	}
	o.c("open-log")
	if !android {
		o.q.Elevated = o.c("elevated").Flag
		o.n("log", 4)
		r := o.join()
		if r.Err != nil {
			o.n("announce", 1)
			return r.Err
		}
		if r.Reuse {
			o.n("announce", 2)
			o.n("log", 5)
			return nil
		}
	}
	if r := o.c("new-service"); r.Err != nil {
		return r.Err
	}
	if r := o.c("prepare"); r.Err != nil {
		return r.Err
	}
	if r := o.c("token"); r.Err != nil {
		return r.Err
	}
	if r := o.c("serve"); r.Err != nil {
		return r.Err
	}
	if android {
		o.c("android-publish")
	} else {
		o.identity()
	}
	o.c("record-build")
	if o.writeRecord() != nil {
		o.n("log", 6)
	}
	if android {
		o.n("log", 7)
		return nil
	}
	o.c("lock-release")
	o.n("announce", 0)
	o.c("signal-wait")
	o.c("server-close")
	o.c("cleanup")
	o.removeRecord()
	return nil
}
func (o *bootstrapOracle) androidStop() {
	o.c("service-lock")
	r := o.c("android-detach")
	o.c("service-unlock")
	if !r.Flag {
		return
	}
	o.c("app-lock")
	o.c("drop")
	o.c("app-unlock")
	o.c("cleanup")
	if r.Other {
		o.c("server-close")
	}
	o.removeRecord()
}
func (o *bootstrapOracle) jniStart() error {
	if !o.c("bridge-init").Flag {
		return o.fail(5)
	}
	o.c("socket-hook")
	o.c("service-lock")
	defer o.c("service-unlock")
	if o.c("android-current").Flag {
		o.c("android-reuse")
		return nil
	}
	if err := o.start(true); err != nil {
		o.n("log", 8)
		return err
	}
	return nil
}
func (o *bootstrapOracle) tunnel() error {
	if o.c("tunnel-decode").Err != nil {
		o.n("log", 9)
	}
	o.c("service-lock")
	current := o.c("android-current").Flag
	if current {
		o.c("app-lock")
		o.c("service-unlock")
	}
	if o.q.Authorized {
		o.c("tunnel-grant")
	} else {
		o.c("tunnel-release")
	}
	if !current {
		o.c("service-unlock")
		if o.q.Authorized {
			return o.fail(6)
		}
		return nil
	}
	o.fx.Call(bootstrapServiceEffect{Kind: "permission", Flag: o.q.Authorized})
	running := o.c("kernel-running").Flag
	var dropErr error
	if !o.q.Authorized && running {
		dropErr = o.c("drop").Err
	}
	o.c("app-unlock")
	if dropErr != nil {
		o.n("log", 10)
	}
	return nil
}
func runGoBootstrapService(q bootstrapServiceRequest, fx bootstrapServiceEffects) (out bootstrapServiceResult) {
	defer fx.Release()
	defer func() {
		if p := recover(); p != nil {
			panic(p)
		}
		if f, ok := fx.(bootstrapFinalizer); ok {
			out = f.FinalizeBootstrapResult(q, out)
		}
	}()
	o := bootstrapOracle{q, fx}
	switch q.Op {
	case "join":
		return o.join()
	case "lock":
		out.Err = o.lock()
		out.Lock = out.Err == nil
	case "running":
		out.Bool = o.running()
	case "probe":
		out.Bool = o.probe()
	case "handover":
		out.Bool = o.handover()
	case "identity":
		o.identity()
	case "owner":
		out.Bool = o.owner()
	case "identify":
		out.Bool = o.identify()
	case "proxy":
		out.Err = o.proxy()
	case "clear-owner":
		o.clearOwner()
	case "portable":
		out.Bool = o.portable()
	case "write-record":
		out.Err = o.writeRecord()
	case "remove-record":
		o.removeRecord()
	case "windows":
		out.Err = o.start(false)
	case "android-start":
		out.Err = o.start(true)
	case "android-stop":
		o.androidStop()
	case "jni-start":
		out.Err = o.jniStart()
	case "tunnel":
		out.Err = o.tunnel()
	}
	return
}
