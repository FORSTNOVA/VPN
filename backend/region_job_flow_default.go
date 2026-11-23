//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedRegionJobService(r regionJobServiceRequest, fx regionJobServiceEffects) regionJobServiceResult {
	return runGoRegionJobService(r, fx)
}
