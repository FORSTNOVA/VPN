package main

import (
	"net/http"
	"testing"
)

type driverIndependentWriter struct {
	t      *testing.T
	fx     *driverIndependentFX
	crash  bool
	writes int
}

func (w *driverIndependentWriter) Header() http.Header { return make(http.Header) }
func (w *driverIndependentWriter) WriteHeader(int) {
	w.writes++
	if !w.fx.owned {
		w.t.Error("install response outside lock")
	}
	if w.crash {
		panic("synthetic writer")
	}
}
func (w *driverIndependentWriter) Write(b []byte) (int, error) {
	w.writes++
	if !w.fx.owned {
		w.t.Error("install body outside lock")
	}
	if w.crash {
		panic("synthetic writer")
	}
	return len(b), nil
}
func TestWintunServiceIndependentSelectedHTTPWriter(t *testing.T) {
	for _, crash := range []bool{false, true} {
		name := "ok"
		if crash {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			s := driverIndependentBase()
			s.Source = "local"
			f := &driverIndependentFX{s: s}
			w := &driverIndependentWriter{t: t, fx: f, crash: crash}
			var p any
			func() {
				defer func() { p = recover() }()
				runDriverInstallHTTPWithEffects(w, driverInstallRequest{Op: "install"}, f)
			}()
			if f.owned || f.releases != 1 {
				t.Fatalf("release owned=%v count=%d", f.owned, f.releases)
			}
			if crash && (p == nil || w.writes != 1) {
				t.Fatalf("writer panic rewritten panic=%v writes=%d", p, w.writes)
			}
			if !crash && (p != nil || w.writes == 0) {
				t.Fatalf("response panic=%v writes=%d", p, w.writes)
			}
		})
	}
}
