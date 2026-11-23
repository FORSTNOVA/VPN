package main

import (
	"net/http"
	"testing"
)

type tunIndependentWriter struct {
	t          *testing.T
	fx         *tunIndependentFX
	panicWrite bool
	calls      int
}

func (w *tunIndependentWriter) Header() http.Header { return make(http.Header) }
func (w *tunIndependentWriter) WriteHeader(int) {
	w.calls++
	if !w.fx.owned {
		w.t.Error("response outside application lock")
	}
	if w.panicWrite {
		panic("synthetic writer")
	}
}
func (w *tunIndependentWriter) Write(b []byte) (int, error) {
	w.calls++
	if !w.fx.owned {
		w.t.Error("body outside application lock")
	}
	if w.panicWrite {
		panic("synthetic writer")
	}
	return len(b), nil
}
func TestTunReportIndependentSelectedHTTPWriter(t *testing.T) {
	for _, crash := range []bool{false, true} {
		name := "ok"
		if crash {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			fx := &tunIndependentFX{}
			w := &tunIndependentWriter{t: t, fx: fx, panicWrite: crash}
			var p any
			func() {
				defer func() { p = recover() }()
				runTunReportHTTPWithEffects(w, tunReportRequest{Op: "windows-checks"}, fx)
			}()
			if fx.releases != 1 {
				t.Fatalf("Release count=%d", fx.releases)
			}
			if fx.owned {
				t.Fatal("writer retained lock")
			}
			if crash && (p == nil || w.calls != 1) {
				t.Fatalf("writer panic must propagate, no second 500: panic=%v writes=%d", p, w.calls)
			}
			if !crash && (p != nil || w.calls == 0) {
				t.Fatalf("normal writer panic=%v writes=%d", p, w.calls)
			}
		})
	}
}
