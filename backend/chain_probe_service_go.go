package main

import (
	"errors"
	"time"
)

func runGoChainProbeService(q chainProbeServiceRequest, fx chainProbeServiceEffects) chainProbeServiceResult {
	defer fx.Release()
	call := fx.Call
	resultError := func(err error) chainProbeServiceResult { return chainProbeServiceResult{Err: err} }
	request := func(start time.Time, budget time.Duration, target string) (int64, bool, error) {
		call(chainProbeEffect{Kind: "stage", Text: chainProbeStage(8)})
		r := call(chainProbeEffect{Kind: "request-new", Time: start, Budget: budget, Target: target})
		if r.Err != nil {
			call(chainProbeEffect{Kind: "request-cancel"})
			return 0, false, r.Err
		}
		r = call(chainProbeEffect{Kind: "request-do"})
		if r.Err != nil {
			call(chainProbeEffect{Kind: "request-cancel"})
			if r.Deadline {
				return 0, false, errors.New(chainProbeError(3))
			}
			return 0, false, errors.New(chainProbeError(4))
		}
		elapsed := call(chainProbeEffect{Kind: "elapsed", Time: start}).Elapsed
		if elapsed < 1 {
			elapsed = 1
		}
		call(chainProbeEffect{Kind: "body-close"})
		if r.HTTPStatus != 204 {
			call(chainProbeEffect{Kind: "stage", Text: chainProbeStage(12)})
			call(chainProbeEffect{Kind: "request-cancel"})
			return 0, false, errors.New(chainProbeStatusError(r.HTTPStatus))
		}
		call(chainProbeEffect{Kind: "request-cancel"})
		return elapsed, r.Reused, nil
	}
	timed := func(start time.Time, budget time.Duration, target string) (int64, int64, error) {
		first, _, err := request(start, budget, target)
		if err != nil {
			return 0, 0, err
		}
		at := call(chainProbeEffect{Kind: "now"}).Now
		warm, reused, err := request(at, budget, target)
		if err != nil {
			return first, 0, err
		}
		if !reused {
			call(chainProbeEffect{Kind: "stage", Text: chainProbeStage(13)})
			return first, 0, errors.New(chainProbeError(5))
		}
		return first, warm, nil
	}
	measure := func(count int, urls []string, target string) (int64, int64, error) {
		started := call(chainProbeEffect{Kind: "now"}).Now
		call(chainProbeEffect{Kind: "stage", Text: chainProbeStage(0)})
		nodes := 0
		resolver, transport := false, false
		cleanup := func() {
			if transport {
				call(chainProbeEffect{Kind: "idle-close"})
			}
			for i := nodes - 1; i >= 0; i-- {
				call(chainProbeEffect{Kind: "node-close", Index: i})
			}
			if resolver {
				call(chainProbeEffect{Kind: "resolver-reset"})
			}
		}
		failed := func(err error) (int64, int64, error) { cleanup(); return 0, 0, err }
		for i := 0; i < count; i++ {
			f := call(chainProbeEffect{Kind: "node", Index: i}).Node
			if i == 0 {
				r := call(chainProbeEffect{Kind: "resolver", URLs: urls, Node: f})
				if r.Err != nil {
					call(chainProbeEffect{Kind: "stage", Text: chainProbeStage(1)})
					return failed(r.Err)
				}
				resolver = r.Bool
				if resolver && len(urls) > 0 && f.ECHPresent && f.ECHEnabled && f.ECHConfig == "" {
					name := f.ECHQuery
					if name == "" {
						name = f.ServerName
					}
					if name == "" {
						name = f.SNI
					}
					if name == "" {
						name = f.Server
					}
					call(chainProbeEffect{Kind: "stage", Text: chainProbeStage(2)})
					if call(chainProbeEffect{Kind: "ech", Time: started.Add(12 * time.Second), Name: name}).Err != nil {
						return failed(errors.New(chainProbeError(0)))
					}
					call(chainProbeEffect{Kind: "ech-apply"})
				}
				call(chainProbeEffect{Kind: "entry-dialer", Node: f})
			}
			if call(chainProbeEffect{Kind: "adapter", Index: i}).Err != nil {
				return failed(errors.New(chainProbeError(1)))
			}
			nodes++
		}
		if count == 0 {
			return failed(errors.New(chainProbeError(2)))
		}
		call(chainProbeEffect{Kind: "transport"})
		transport = true
		c, w, err := timed(started, 12*time.Second, target)
		cleanup()
		return c, w, err
	}
	if q.Op == "timed-requests" {
		call(chainProbeEffect{Kind: "snapshot"})
		c, w, err := timed(q.Started, q.Budget, q.Target)
		return chainProbeServiceResult{Cold: c, Warm: w, Err: err}
	}
	if q.Op == "measure-both" || q.Op == "measure-prefix" {
		s := call(chainProbeEffect{Kind: "snapshot"}).State
		c, w, err := measure(s.ProxyCount, q.EntryURLs, q.Target)
		if q.Op == "measure-prefix" && c > 0 {
			err = nil
		}
		return chainProbeServiceResult{Cold: c, Warm: w, Err: err}
	}
	parent := false
	respond := func(code int, msg string, r probeResult, rs []probeResult) chainProbeServiceResult {
		call(chainProbeEffect{Kind: "response", Status: code, Text: msg, Result: r, Results: rs})
		if parent {
			call(chainProbeEffect{Kind: "parent-cancel"})
		}
		return chainProbeServiceResult{Status: code, Message: msg, Result: r, Results: rs}
	}
	if q.Op == "list" {
		if !call(chainProbeEffect{Kind: "store-check"}).Bool {
			return respond(503, chainProbeHTTP(3), probeResult{}, nil)
		}
		call(chainProbeEffect{Kind: "lock"})
		r := call(chainProbeEffect{Kind: "index"})
		call(chainProbeEffect{Kind: "unlock"})
		if r.Err != nil {
			return respond(400, r.Err.Error(), probeResult{}, nil)
		}
		r = call(chainProbeEffect{Kind: "records"})
		if r.Err != nil {
			return respond(500, chainProbeHTTP(4), probeResult{}, nil)
		}
		results := []probeResult{}
		for i, record := range r.Records {
			initial := probeResult{Delay: record.Delay, Error: record.Error, Measured: record.Measured, Target: probeTarget}
			d := call(chainProbeEffect{Kind: "details-decode", Index: i, Result: initial})
			if d.Err != nil {
				return respond(500, chainProbeHTTP(5), probeResult{}, nil)
			}
			d = call(chainProbeEffect{Kind: "path-decode", Index: i, Result: d.Result})
			if d.Err != nil {
				continue
			}
			if call(chainProbeEffect{Kind: "compile", Hops: d.Result.Hops}).Err == nil && call(chainProbeEffect{Kind: "fingerprint"}).Text == record.Fingerprint {
				results = append(results, d.Result)
			}
		}
		return respond(200, "", probeResult{}, results)
	}
	if q.Op != "probe" {
		return resultError(errors.New("unknown chain probe operation"))
	}
	call(chainProbeEffect{Kind: "lock"})
	idx := call(chainProbeEffect{Kind: "index"})
	state := call(chainProbeEffect{Kind: "mode"}).State
	if state.Subscription {
		call(chainProbeEffect{Kind: "policy"})
	}
	call(chainProbeEffect{Kind: "unlock"})
	if idx.Err != nil {
		return respond(400, idx.Err.Error(), probeResult{}, nil)
	}
	compiled := call(chainProbeEffect{Kind: "compile", Hops: q.Hops})
	if compiled.Err != nil {
		return respond(400, compiled.Err.Error(), probeResult{}, nil)
	}
	fingerprint := call(chainProbeEffect{Kind: "fingerprint"}).Text
	call(chainProbeEffect{Kind: "parent-context"})
	parent = true
	policy := call(chainProbeEffect{Kind: "entry-policy"})
	urls := policy.URLs
	if len(urls) == 0 && (state.TUN || policy.Index > 0) {
		urls = []string{entryUDP1, entryUDP2}
		if state.StrictDNS {
			urls = []string{entryDoH}
		}
	}
	call(chainProbeEffect{Kind: "entry-select", URLs: urls})
	result := probeResult{Hops: q.Hops, Target: probeTarget, DiagnosticsVersion: 1}
	var err error
	result.Delay, result.WarmDelay, err = measure(compiled.State.ProxyCount, urls, result.Target)
	obs := call(chainProbeEffect{Kind: "observation"})
	result.EntryIP = obs.IP
	if result.EntryIP == "" {
		result.EntryIP = obs.Attempt
		result.EntryAttempt = result.EntryIP != ""
	}
	result.EntryFamily = obs.Family
	if err != nil {
		result.FailureStage = obs.Stage
		if result.Delay > 0 {
			result.WarmError = err.Error()
		} else {
			result.Error = err.Error()
		}
	}
	result.Measured = call(chainProbeEffect{Kind: "now"}).Now.UnixMilli()
	path := call(chainProbeEffect{Kind: "path-encode", Hops: q.Hops}).Text
	if !call(chainProbeEffect{Kind: "store-check"}).Bool {
		return respond(503, chainProbeHTTP(1), probeResult{}, nil)
	}
	details := call(chainProbeEffect{Kind: "result-encode", Result: result}).Text
	if call(chainProbeEffect{Kind: "save", Report: probeRecord{Path: path, Fingerprint: fingerprint, Delay: result.Delay, Error: result.Error, Measured: result.Measured, Details: details}}).Err != nil {
		return respond(500, chainProbeHTTP(2), probeResult{}, nil)
	}
	return respond(200, "", result, nil)
}
