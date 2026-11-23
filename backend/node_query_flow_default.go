//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedNodeQueryService(r nodeQueryServiceRequest, fx nodeQueryServiceEffects) nodeQueryServiceResult {
	return runGoNodeQueryService(r, fx)
}
