//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedChainService(r chainServiceRequest, fx chainServiceEffects) chainServiceResult {
	return runGoChainService(r, fx)
}
