//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedBootstrapService(r bootstrapServiceRequest, fx bootstrapServiceEffects) bootstrapServiceResult {
	return runGoBootstrapService(r, fx)
}
