//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedConnectionService(r connectionServiceRequest, fx connectionServiceEffects) connectionServiceResult {
	return runGoConnectionService(r, fx)
}
