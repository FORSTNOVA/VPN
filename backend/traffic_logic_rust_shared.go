//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"runtime"
	"sync"
	"unsafe"
)

var trafficRust struct {
	once            sync.Once
	values, ranking uintptr
}

func trafficRustAvailable() bool {
	trafficRust.once.Do(func() {
		if rustCoreAvailable() {
			trafficRust.values, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_values_v1")
			trafficRust.ranking, _ = nativeLookup(rustCore.handle, "smartvpn_traffic_ranking_v1")
		}
	})
	return trafficRust.values != 0 && trafficRust.ranking != 0
}
func rustTrafficValues(input [7]int64) ([2]int64, bool) {
	var output [2]int64
	if !trafficRustAvailable() {
		return output, false
	}
	status, _, _ := nativeCall(trafficRust.values, uintptr(unsafe.Pointer(&input[0])), uintptr(unsafe.Pointer(&output[0])))
	runtime.KeepAlive(input)
	runtime.KeepAlive(output)
	return output, int32(status) == 0
}
func counterDelta(current, previous int64) int64 {
	if out, ok := rustTrafficValues([7]int64{previous, 0, current, 0, 0, 0, 0}); ok {
		return out[0]
	}
	return counterDeltaGo(current, previous)
}
func rateFor(previous, current trafficSample) (int64, int64) {
	var first int64
	if previous.at.IsZero() {
		first = 1
	}
	input := [7]int64{previous.up, previous.down, current.up, current.down, int64(current.at.Sub(previous.at)), first, 1}
	if out, ok := rustTrafficValues(input); ok {
		return out[0], out[1]
	}
	return rateForGo(previous, current)
}
func sortTrafficRows(rows []connectionRow) {
	if !rustSortTrafficRows(rows) {
		sortTrafficRowsGo(rows)
	}
}
func rustSortTrafficRows(rows []connectionRow) bool {
	if !trafficRustAvailable() || len(rows) > 65536 {
		return false
	}
	if len(rows) == 0 {
		return true
	}
	totals := make([]int64, len(rows))
	order := make([]uintptr, len(rows))
	for i := range rows {
		totals[i] = rows[i].Upload + rows[i].Download
	}
	status, _, _ := nativeCall(trafficRust.ranking, uintptr(unsafe.Pointer(&totals[0])), uintptr(len(totals)), uintptr(unsafe.Pointer(&order[0])))
	runtime.KeepAlive(totals)
	runtime.KeepAlive(order)
	if int32(status) != 0 {
		return false
	}
	seen := make([]bool, len(rows))
	sorted := make([]connectionRow, len(rows))
	for i, index := range order {
		if index >= uintptr(len(rows)) || seen[index] {
			return false
		}
		seen[index] = true
		sorted[i] = rows[index]
	}
	copy(rows, sorted)
	return true
}
