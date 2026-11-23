//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import "time"

func (p healthParams) normalized() healthParams { return p.normalizedGo() }
func nextHealthState(current nodeHealth, ok bool, latencyMs int, now time.Time, params healthParams) nodeHealth {
	return nextHealthStateGo(current, ok, latencyMs, now, params)
}
