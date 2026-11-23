//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedLabelsService(r labelsServiceRequest, fx labelsServiceEffects) labelsServiceResult {
	return runGoLabelsService(r, fx)
}
