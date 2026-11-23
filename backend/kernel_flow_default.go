//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedKernelService(r kernelServiceRequest, fx kernelServiceEffects) kernelServiceResult {
	return runGoKernelService(r, fx)
}
