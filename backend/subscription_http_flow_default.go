//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedSubscriptionHTTPService(r subscriptionHTTPServiceRequest, fx subscriptionHTTPServiceEffects) subscriptionHTTPServiceResult {
	return runGoSubscriptionHTTPService(r, fx)
}
