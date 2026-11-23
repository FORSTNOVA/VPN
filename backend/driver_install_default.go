//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedDriverInstall(q driverInstallRequest, fx driverInstallEffects) driverInstallResult {
	return runGoDriverInstall(q, fx)
}
