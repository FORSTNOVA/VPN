//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedSpeedService(r speedServiceRequest, fx speedServiceEffects) speedServiceResult {
	return runGoSpeedService(r, fx)
}
