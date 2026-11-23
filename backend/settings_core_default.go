//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func windowsGameRule(entry string) string { return windowsGameRuleGo(entry) }
func normalizeGameBypass(entries []string, android bool) ([]string, error) {
	return normalizeGameBypassGo(entries, android)
}
func validSocksSetting(port int, windows bool) bool { return validSocksSettingGo(port, windows) }
