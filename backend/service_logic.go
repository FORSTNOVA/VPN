package main

import "bytes"

func redactLogBytesGo(body, secret []byte) []byte {
	return bytes.ReplaceAll(body, secret, []byte(redacted))
}

// Pure decision only: the caller still owns identity checks and side effects.
func serviceTakeoverDecisionGo(stale, connected, existingElevated, elevated bool) bool {
	if stale && !connected {
		return true
	}
	if existingElevated && !elevated {
		return false
	}
	return elevated && !existingElevated
}
