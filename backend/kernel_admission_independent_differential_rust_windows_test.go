//go:build windows && rustcore

package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func admissionIndependentActual(t *testing.T, q kernelAdmissionRequest, fx kernelAdmissionEffects) kernelAdmissionResult {
	t.Helper()
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("native admission unavailable")
	}
	n := kernelAdmissionFlowRecoveries.Load()
	r, ok := runRustKernelAdmissionService(q, fx)
	if !ok || kernelAdmissionFlowRecoveries.Load() != n {
		t.Fatalf("native admission incomplete %s %v %v", q.Op, ok, kernelAdmissionFlowRecoveryReason.Load())
	}
	return r
}
func TestKernelAdmissionActualIndependentMatrix(t *testing.T) {
	for _, op := range []string{"path", "look-for", "new", "running", "start", "watch", "stop", "ready", "owner", "privacy", "check-config"} {
		for i := 0; i < 128; i++ {
			t.Run(fmt.Sprintf("%s/%d", op, i), func(t *testing.T) {
				g, r := admissionIndependentCase(i), admissionIndependentCase(i)
				q := kernelAdmissionRequest{Op: op, Port: 123, Name: "synthetic", Timeout: time.Second}
				gr := runGoKernelAdmissionService(q, g)
				rr := admissionIndependentActual(t, q, r)
				if gr.Bool != rr.Bool || gr.Text != rr.Text || fmt.Sprint(gr.Err) != fmt.Sprint(rr.Err) || !reflect.DeepEqual(g.Trace, r.Trace) || g.Released != r.Released {
					t.Fatalf("mismatch go=%#v rust=%#v trace=%#v / %#v", gr, rr, g.Trace, r.Trace)
				}
			})
		}
	}
}
func TestKernelAdmissionActualIndependentGoldens(t *testing.T) {
	admissionIndependentGolden(t, func(q kernelAdmissionRequest, f kernelAdmissionEffects) kernelAdmissionResult {
		return admissionIndependentActual(t, q, f)
	})
}

func TestKernelAdmissionActualIndependentResources(t *testing.T) {
	admissionIndependentResources(t, func(q kernelAdmissionRequest, f kernelAdmissionEffects) kernelAdmissionResult {
		return admissionIndependentActual(t, q, f)
	})
}

func TestKernelAdmissionActualIndependentUnpublishedCleanup(t *testing.T) {
	if !rustKernelAdmissionFlowAvailable() {
		t.Fatal("native admission unavailable")
	}
	admissionIndependentUnpublishedCleanup(t, func(q kernelAdmissionRequest, f kernelAdmissionEffects) kernelAdmissionResult {
		r, ok := runRustKernelAdmissionService(q, f)
		if !ok {
			t.Fatal("native not started")
		}
		return r
	})
}
