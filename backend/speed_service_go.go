package main

import (
	"strings"
	"time"
)

type speedOracle struct{ fx speedServiceEffects }

func (o speedOracle) call(k string) speedServiceReply { return o.fx.Call(speedServiceEffect{Kind: k}) }
func (o speedOracle) measure(upload bool, allowance int64, budget time.Duration) (int64, time.Duration, error) {
	o.fx.Call(speedServiceEffect{Kind: "measure-begin", Upload: upload, Bytes: allowance, Budget: budget})
	defer o.call("measure-end")
	count := speedTestStreams
	chunk := allowance
	if upload {
		count = speedUploadStreams
		chunk = allowance / int64(count)
	}
	o.fx.Call(speedServiceEffect{Kind: "batch-start", Upload: upload, Count: count, Bytes: chunk, Budget: budget})
	o.call("batch-join")
	r := o.call("totals")
	if r.Started == 0 || r.Bytes == 0 {
		return 0, 0, speedEmptyError(upload)
	}
	// The original observes total twice after joining, then asks the clock.
	n := o.call("total-load").Bytes
	elapsed := o.fx.Call(speedServiceEffect{Kind: "elapsed", Bytes: r.Started}).Elapsed
	return n, elapsed, nil
}
func runGoSpeedService(q speedServiceRequest, fx speedServiceEffects) (r speedServiceResult) {
	defer fx.Release()
	o := speedOracle{fx}
	switch q.Op {
	case "zero-read":
		if q.Remaining <= 0 {
			r.EOF = true
			return
		}
		n := int64(q.Length)
		if n > q.Remaining {
			n = q.Remaining
		}
		o.fx.Call(speedServiceEffect{Kind: "zero-fill", Bytes: n})
		o.fx.Call(speedServiceEffect{Kind: "zero-remaining", Bytes: q.Remaining - n})
		o.fx.Call(speedServiceEffect{Kind: "zero-count", Bytes: n})
		r.Bytes = n
	case "download", "upload":
		r.Bytes, r.Elapsed, r.Err = o.measure(q.Op == "upload", q.Allowance, q.Budget)
	case "down-stream", "up-stream":
		upload := q.Op == "up-stream"
		if o.fx.Call(speedServiceEffect{Kind: "request-new", Upload: upload, Bytes: q.Allowance}).Err != nil {
			return
		}
		if o.call("request-do").Err != nil {
			return
		}
		defer o.call("body-close")
		if upload {
			o.call("upload-drain")
			return
		}
		for {
			v := o.call("body-read")
			if v.Bytes > 0 {
				o.fx.Call(speedServiceEffect{Kind: "download-count", Bytes: v.Bytes})
			}
			if v.Err != nil {
				return
			}
			if o.call("total-load").Bytes >= q.Allowance {
				return
			}
		}
	case "handler":
		mb := q.MegaBytes
		if mb == 0 {
			mb = 30
		}
		if mb < 10 {
			mb = 10
		}
		if mb > 100 {
			mb = 100
		}
		o.call("lock")
		defer o.call("unlock")
		e := o.call("ensure")
		if e.Err != nil {
			r.Status = 502
			r.Err = e.Err
			o.fx.Call(speedServiceEffect{Kind: "response", Status: r.Status, Text: e.Err.Error()})
			return
		}
		r.Temporary = e.Flag
		if r.Temporary {
			defer o.call("stop")
		}
		r.Node = o.call("active").Text
		if r.Node == "" || strings.EqualFold(r.Node, blockedChoice) {
			r.Status = 409
			r.Err = speedSelectionError()
			o.fx.Call(speedServiceEffect{Kind: "response", Status: r.Status, Text: r.Err.Error()})
			return
		}
		o.call("endpoint")
		r.Speed = speedTestResult{Target: speedTestHost, MegaBytes: mb}
		n, d, err := o.measure(false, int64(mb)<<20, speedTestSeconds)
		if err != nil {
			r.Speed.Error = err.Error()
		} else {
			r.Speed.DownBytes = n
			r.Speed.DownMs = d.Milliseconds()
			r.Speed.DownMbps = o.fx.Call(speedServiceEffect{Kind: "rate", Bytes: n, Budget: d}).Rate
		}
		n, d, err = o.measure(true, int64(mb/speedUploadShare)<<20, speedTestSeconds)
		if err != nil {
			if r.Speed.Error == "" {
				r.Speed.Error = err.Error()
			}
		} else {
			r.Speed.UpBytes = n
			r.Speed.UpMs = d.Milliseconds()
			r.Speed.UpMbps = o.fx.Call(speedServiceEffect{Kind: "rate", Bytes: n, Budget: d}).Rate
		}
		r.Status = 200
		o.fx.Call(speedServiceEffect{Kind: "response", Status: 200, Node: r.Node, Temporary: r.Temporary, Result: r.Speed})
	}
	return
}

func speedRejectedNode(n string) bool { return n == "" || strings.EqualFold(n, blockedChoice) }
