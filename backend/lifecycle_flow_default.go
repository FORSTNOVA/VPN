//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedLifecycleService(r lifecycleServiceRequest, fx lifecycleServiceEffects) lifecycleServiceResult {
	return runGoLifecycleService(r, fx)
}
