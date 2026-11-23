//go:build windows && rustcore

package main

import (
	"fmt"
	"reflect"
	"testing"
)

func embeddedIndependentActual(t *testing.T, q embeddedKernelRequest, f embeddedKernelEffects) embeddedKernelResult {
	t.Helper()
	if !rustEmbeddedKernelAvailable() {
		t.Fatal("embedded native unavailable")
	}
	n := embeddedKernelRecoveries.Load()
	r, ok := runRustEmbeddedKernel(q, f)
	if !ok || embeddedKernelRecoveries.Load() != n {
		t.Fatalf("embedded incomplete %s %v %v", q.Op, ok, embeddedKernelRecoveryReason.Load())
	}
	return r
}
func TestEmbeddedKernelActualIndependentMatrix(t *testing.T) {
	for _, op := range []string{"start", "stop", "running", "refusal", "start-logging", "logs", "event", "stop-logging", "check", "apply", "session-start", "session-stop", "session-apply"} {
		for i := 0; i < 128; i++ {
			t.Run(fmt.Sprintf("%s/%d", op, i), func(t *testing.T) {
				g, r := embeddedIndependentCase(i), embeddedIndependentCase(i)
				q := embeddedKernelRequest{Op: op}
				gr := runGoEmbeddedKernel(q, g)
				rr := embeddedIndependentActual(t, q, r)
				if gr.Status != rr.Status || fmt.Sprint(gr.Err) != fmt.Sprint(rr.Err) || !reflect.DeepEqual(g.Trace, r.Trace) || g.ErrText != r.ErrText || g.Running != r.Running || g.Events != r.Events {
					t.Fatalf("embedded mismatch go=%#v rust=%#v trace=%v/%v", gr, rr, g.Trace, r.Trace)
				}
			})
		}
	}
}
func TestEmbeddedKernelActualIndependentGoldens(t *testing.T) {
	embeddedIndependentGoldens(t, func(q embeddedKernelRequest, f embeddedKernelEffects) embeddedKernelResult {
		return embeddedIndependentActual(t, q, f)
	})
}
