//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"runtime"
	"sync"
	"time"
	"unsafe"
)

var healthRust struct {
	once                  sync.Once
	normalize, transition uintptr
}

func healthRustAvailable() bool {
	healthRust.once.Do(func() {
		if rustCoreAvailable() {
			healthRust.normalize, _ = nativeLookup(rustCore.handle, "smartvpn_health_normalize_v1")
			healthRust.transition, _ = nativeLookup(rustCore.handle, "smartvpn_health_transition_v1")
		}
	})
	return healthRust.normalize != 0 && healthRust.transition != 0
}
func (p healthParams) normalized() healthParams {
	if result, ok := rustHealthNormalized(p); ok {
		return result
	}
	return p.normalizedGo()
}
func rustHealthNormalized(p healthParams) (healthParams, bool) {
	if !healthRustAvailable() {
		return healthParams{}, false
	}
	input := [6]int64{int64(p.ConnectTimeoutMs), int64(p.PatrolIntervalSec), int64(p.FailureThreshold), int64(p.CooldownSec), int64(p.MinDwellSec), int64(p.RecoverySuccesses)}
	var out [6]int64
	status, _, _ := nativeCall(healthRust.normalize, uintptr(unsafe.Pointer(&input[0])), uintptr(unsafe.Pointer(&out[0])))
	runtime.KeepAlive(input)
	runtime.KeepAlive(out)
	if int32(status) != 0 {
		return healthParams{}, false
	}
	return healthParams{int(out[0]), int(out[1]), int(out[2]), int(out[3]), int(out[4]), int(out[5])}, true
}
func nextHealthState(current nodeHealth, ok bool, latencyMs int, now time.Time, params healthParams) nodeHealth {
	if result, used := rustNextHealthState(current, ok, latencyMs, now, params); used {
		return result
	}
	return nextHealthStateGo(current, ok, latencyMs, now, params)
}
func rustNextHealthState(current nodeHealth, ok bool, latencyMs int, now time.Time, params healthParams) (nodeHealth, bool) {
	if !healthRustAvailable() {
		return nodeHealth{}, false
	}
	var state, success, cooling int64
	if current.State == healthCooldown {
		state = 1
	} else if current.State == healthRecovery {
		state = 2
	}
	if ok {
		success = 1
	}
	if current.cooldownActive(now) {
		cooling = 1
	}
	input := [8]int64{state, int64(current.ConsecutiveFailures), int64(current.ConsecutiveSuccesses), int64(latencyMs), success, cooling, int64(params.FailureThreshold), int64(params.RecoverySuccesses)}
	var out [4]int64
	status, _, _ := nativeCall(healthRust.transition, uintptr(unsafe.Pointer(&input[0])), uintptr(unsafe.Pointer(&out[0])))
	runtime.KeepAlive(input)
	runtime.KeepAlive(out)
	if int32(status) != 0 || out[0] < 1 || out[0] > 5 {
		return nodeHealth{}, false
	}
	next := current
	next.UpdatedAt = now
	if ok {
		next.LastSuccessAt = now
	}
	next.State = []string{healthHealthy, healthSuspect, healthUnhealthy, healthCooldown, healthRecovery}[out[0]-1]
	next.ConsecutiveFailures = int(out[1])
	next.ConsecutiveSuccesses = int(out[2])
	next.LatencyMs = int(out[3])
	return next, true
}
