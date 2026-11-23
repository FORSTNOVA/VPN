//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedPolicyDns(q policyDnsRequest, fx policyDnsEffects) policyDnsResult {
	return runGoPolicyDns(q, fx)
}
