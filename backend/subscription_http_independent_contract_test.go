package main

import "testing"

func TestSubscriptionHTTPIndependentSelectedMatrix(t *testing.T) {
	subHTTPIndependentMatrix(t, runSelectedSubscriptionHTTPService)
}
func TestSubscriptionHTTPIndependentSelectedGoldens(t *testing.T) {
	subHTTPIndependentGoldens(t, runSelectedSubscriptionHTTPService)
}
