//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedTrafficService(r trafficServiceRequest, fx trafficServiceEffects) trafficServiceResult {
	return runGoTrafficService(r, fx)
}
