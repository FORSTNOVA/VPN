//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedRegionService(r regionServiceRequest, fx regionServiceEffects) regionServiceResult {
	return runGoRegionService(r, fx)
}
