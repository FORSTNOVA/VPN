//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedManualService(r manualServiceRequest, fx manualServiceEffects) manualServiceResult {
	return runGoManualService(r, fx)
}
