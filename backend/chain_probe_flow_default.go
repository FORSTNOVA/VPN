//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedChainProbeService(r chainProbeServiceRequest, fx chainProbeServiceEffects) chainProbeServiceResult {
	return runGoChainProbeService(r, fx)
}
