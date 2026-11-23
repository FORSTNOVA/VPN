//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedAdaptiveService(r adaptiveServiceRequest, fx adaptiveServiceEffects) adaptiveServiceResult {
	return runGoAdaptiveService(r, fx)
}
