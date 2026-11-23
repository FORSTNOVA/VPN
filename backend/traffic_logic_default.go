//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func counterDelta(current, previous int64) int64             { return counterDeltaGo(current, previous) }
func rateFor(previous, current trafficSample) (int64, int64) { return rateForGo(previous, current) }
func sortTrafficRows(rows []connectionRow)                   { sortTrafficRowsGo(rows) }
