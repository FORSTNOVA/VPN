//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedAndroidResolver(q androidResolverRequest, fx androidResolverEffects) androidResolverResult {
	return runGoAndroidResolver(q, fx)
}
