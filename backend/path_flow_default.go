//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedPathService(r pathServiceRequest, fx pathServiceEffects) pathServiceResult {
	return runGoPathService(r, fx)
}
