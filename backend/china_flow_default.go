//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedChinaService(r chinaServiceRequest, fx chinaServiceEffects) chinaServiceResult {
	return runGoChinaService(r, fx)
}
