//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedPathMonitorService(r pathMonitorServiceRequest, fx pathMonitorServiceEffects) pathMonitorServiceResult {
	return runGoPathMonitorService(r, fx)
}
