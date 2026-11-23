//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedTrafficProjection(q trafficProjectionRequest) trafficProjectionResult {
	return runGoTrafficProjection(q)
}
