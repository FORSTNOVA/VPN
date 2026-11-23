//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedKernelAdmissionService(r kernelAdmissionRequest, fx kernelAdmissionEffects) kernelAdmissionResult {
	return runGoKernelAdmissionService(r, fx)
}
