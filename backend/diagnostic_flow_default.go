//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedDiagnoseService(r diagnoseServiceRequest, fx diagnoseServiceEffects) diagnoseServiceResult {
	return runGoDiagnoseService(r, fx)
}
