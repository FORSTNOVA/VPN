//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedQuotaService(r quotaServiceRequest, fx quotaServiceEffects) quotaServiceResult {
	return runGoQuotaService(r, fx)
}
