//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedTunReport(q tunReportRequest, fx tunReportEffects) tunReportResult {
	return runGoTunReport(q, fx)
}
