//go:build windows && rustcore

package main

import "testing"

func adaptiveActualRunner(t *testing.T) adaptiveIndependentRunner {
	if !rustAdaptiveFlowAvailable() {
		t.Fatal("adaptive native unavailable")
	}
	return func(q adaptiveServiceRequest, fx adaptiveServiceEffects) adaptiveServiceResult {
		n := adaptiveFlowRecoveries.Load()
		r, ok := runRustAdaptiveService(q, fx)
		if !ok || adaptiveFlowRecoveries.Load() != n {
			t.Fatalf("adaptive continuation %s", q.Op)
		}
		return r
	}
}
func TestAdaptiveActualIndependentMatrix(t *testing.T) {
	adaptiveIndependentMatrix(t, adaptiveActualRunner(t))
}
func TestAdaptiveActualIndependentChannels(t *testing.T) {
	adaptiveIndependentChannels(t, adaptiveActualRunner(t))
}
func TestAdaptiveActualIndependentGoldens(t *testing.T) {
	adaptiveIndependentGoldens(t, adaptiveActualRunner(t))
}
func TestAdaptiveActualIndependentHealthComposition(t *testing.T) {
	if !rustAppFlowAvailable() || !rustHealthFlowAvailable() {
		t.Fatal("outer app/health native unavailable")
	}
	ar := func(q appServiceRequest, fx appServiceEffects) appServiceResult {
		n := appFlowRecoveries.Load()
		r, ok := runRustAppService(q, fx)
		if !ok || appFlowRecoveries.Load() != n {
			t.Fatal("outer app fallback")
		}
		return r
	}
	hr := func(s *healthServiceState, q healthServiceRequest, fx healthServiceEffects) {
		n := healthFlowRecoveries.Load()
		if !runRustHealthService(s, q, fx) || healthFlowRecoveries.Load() != n {
			t.Fatal("outer health fallback")
		}
	}
	adaptiveIndependentHealthComposition(t, adaptiveActualRunner(t), ar, hr)
}
func TestAdaptiveActualIndependentExchangeGoldens(t *testing.T) {
	adaptiveIndependentExchangeGoldens(t, adaptiveActualRunner(t))
}
func TestAdaptiveActualIndependentMonitor(t *testing.T) {
	adaptiveIndependentMonitor(t, adaptiveActualRunner(t))
}
