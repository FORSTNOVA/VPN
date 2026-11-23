//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func redactLogBytes(body, secret []byte) []byte  { return redactLogBytesGo(body, secret) }
func tailFromLine(body []byte, limit int) []byte { return tailFromLineGo(body, limit) }
func serviceTakeoverDecision(stale, connected, existingElevated, elevated bool) bool {
	return serviceTakeoverDecisionGo(stale, connected, existingElevated, elevated)
}
