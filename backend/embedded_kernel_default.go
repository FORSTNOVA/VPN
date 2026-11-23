//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedEmbeddedKernel(q embeddedKernelRequest, fx embeddedKernelEffects) embeddedKernelResult {
	return runGoEmbeddedKernel(q, fx)
}
