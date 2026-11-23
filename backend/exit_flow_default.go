//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedExitService(r exitServiceRequest, fx exitServiceEffects) exitServiceResult {
	return runGoExitService(r, fx)
}
