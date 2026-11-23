//go:build windows && rustcore

package main

import "testing"

func subHTTPActualRunner(t *testing.T) subHTTPIndependentRunner {
	t.Helper()
	if !rustSubscriptionHTTPFlowAvailable() {
		t.Fatal("native subscription HTTP unavailable")
	}
	return func(q subscriptionHTTPServiceRequest, fx subscriptionHTTPServiceEffects) subscriptionHTTPServiceResult {
		before := subscriptionHTTPFlowRecoveries.Load()
		defer func() {
			if subscriptionHTTPFlowRecoveries.Load() != before {
				t.Errorf("native recovery op %s", q.Op)
			}
		}()
		r, ok := runRustSubscriptionHTTPService(q, fx)
		if !ok || subscriptionHTTPFlowRecoveries.Load() != before {
			t.Fatalf("native continuation op %s handled %v", q.Op, ok)
		}
		return r
	}
}
func TestSubscriptionHTTPActualIndependentMatrix(t *testing.T) {
	subHTTPIndependentMatrix(t, subHTTPActualRunner(t))
}
func TestSubscriptionHTTPActualIndependentGoldens(t *testing.T) {
	subHTTPIndependentGoldens(t, subHTTPActualRunner(t))
}
func TestSubscriptionHTTPActualIndependentRaw(t *testing.T) {
	subHTTPIndependentRaw(t, subHTTPActualRunner(t))
}
func TestSubscriptionHTTPActualIndependentDiagnostic(t *testing.T) {
	subHTTPIndependentDiagnostic(t, subHTTPActualRunner(t))
}
func TestSubscriptionHTTPActualIndependentChannels(t *testing.T) {
	subHTTPIndependentChannels(t, subHTTPActualRunner(t))
}
func TestSubscriptionHTTPActualIndependentRefreshResources(t *testing.T) {
	subHTTPIndependentRefreshResources(t, subHTTPActualRunner(t))
}
