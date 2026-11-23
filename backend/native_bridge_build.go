//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

const nativeCoreBuildEnabled = true

func packagedCoreLoaded() bool { return rustCoreAvailable() }
