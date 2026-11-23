//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedNodeFront(q nodeFrontRequest, fx nodeFrontEffects) nodeFrontResult {
	return runGoNodeFront(q, fx)
}
