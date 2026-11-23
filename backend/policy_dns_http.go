package main

import "net/http"

func runPolicyDnsHTTPWithGraph(w http.ResponseWriter, q policyDnsRequest, fx *appPolicyDnsEffects) {
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
	r.Data["sections"] = fx.policy.Sections
	r.Data["groups"] = fx.policy.Groups
	writeJSON(tracked, r.Data)
}
