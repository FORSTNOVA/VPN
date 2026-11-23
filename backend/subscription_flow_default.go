//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func runSelectedSubscriptionService(s *subscriptionServiceState, r subscriptionServiceRequest, fx subscriptionServiceEffects) subscriptionServiceResult {
	return runGoSubscriptionService(s, r, fx)
}
