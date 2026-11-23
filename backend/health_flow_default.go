//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedHealthService(s *healthServiceState, r healthServiceRequest, fx healthServiceEffects) {
	runGoHealthService(s, r, fx)
}
