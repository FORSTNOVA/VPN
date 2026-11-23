//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedAppService(r appServiceRequest, fx appServiceEffects) appServiceResult {
	return runGoAppService(r, fx)
}
