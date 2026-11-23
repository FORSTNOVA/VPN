//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedSettingsService(s *settingsServiceState, r settingsServiceRequest, fx settingsServiceEffects) settingsServiceResult {
	return runGoSettingsService(s, r, fx)
}
