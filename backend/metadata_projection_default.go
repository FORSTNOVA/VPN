//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedMetadataProjection(q metadataProjectionRequest, fx metadataProjectionEffects) metadataProjectionResult {
	return runGoMetadataProjection(q, fx)
}
