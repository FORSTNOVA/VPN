//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

const nativeCoreBuildEnabled = false

func packagedCoreLoaded() bool { return false }
