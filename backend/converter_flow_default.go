//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedConverterService(r converterServiceRequest, fx converterServiceEffects) converterServiceResult {
	return runGoConverterService(r, fx)
}
