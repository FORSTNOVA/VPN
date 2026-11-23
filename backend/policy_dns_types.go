package main

import (
	"errors"
	"net/http"
)

type policyDnsRequest struct {
	Op, Path string
	Body     []byte
}
type policyDnsEffect struct {
	Kind, Text, Other string
	Number            int64
}
type policyDnsReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type policyDnsResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type policyDnsEffects interface {
	Call(policyDnsEffect) policyDnsReply
	Release()
}
type policyDnsOracle struct{ fx policyDnsEffects }

func (a *policyDnsOracle) call(k, t, o string, n int64) policyDnsReply {
	return a.fx.Call(policyDnsEffect{Kind: k, Text: t, Other: o, Number: n})
}
func runPolicyDnsWithEffects(q policyDnsRequest, fx policyDnsEffects) (out policyDnsResult) {
	defer func() {
		if recover() != nil {
			out = policyDnsResult{Status: 500, Err: errors.New("policy DNS operation interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedPolicyDns(q, fx)
}
func runPolicyDnsHTTPWithEffects(w http.ResponseWriter, q policyDnsRequest, fx policyDnsEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedPolicyDns(q, fx)
	if r.Err != nil {
		status := r.Status
		if status < 400 {
			status = 500
		}
		http.Error(tracked, r.Err.Error(), status)
		return
	}
	writeJSON(tracked, r.Data)
}
