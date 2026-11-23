//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedProfileService(s *profileServiceState, r profileServiceRequest, fx profileServiceEffects) profileServiceResult {
	return runGoProfileService(s, r, fx)
}
