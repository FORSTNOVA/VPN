package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type tunOwnedFixture struct {
	actual          *appTunReportEffects
	calls, releases int
	fail            bool
}

func (f *tunOwnedFixture) Call(q tunReportEffect) tunReportReply {
	f.calls++
	if q.Kind == "lock" {
		return f.actual.Call(q)
	}
	if f.fail {
		panic("synthetic adapter failure")
	}
	switch q.Kind {
	case "elevated", "running":
		return tunReportReply{}
	case "wintun-path":
		return tunReportReply{Text: "synthetic"}
	case "wintun-stat":
		return tunReportReply{Flag: true}
	case "mode":
		return tunReportReply{Text: modeSystemProxy}
	default:
		panic("unexpected resource " + q.Kind)
	}
}
func (f *tunOwnedFixture) Release() { f.releases++; f.actual.Release() }

type tunOwnedWriter struct {
	*httptest.ResponseRecorder
	a      *app
	t      *testing.T
	crash  bool
	writes int
}

func (w *tunOwnedWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("real mutex released before body")
	}
	if w.crash {
		panic("synthetic writer")
	}
	return w.ResponseRecorder.Write(b)
}
func (w *tunOwnedWriter) WriteHeader(n int) {
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("real mutex released before status")
	}
	w.ResponseRecorder.WriteHeader(n)
}
func TestTunReportRealOwnershipUntilWriterCompletes(t *testing.T) {
	for _, mode := range []string{"success", "effectpanic", "writerpanic"} {
		t.Run(mode, func(t *testing.T) {
			a := settingsDocumentTestApp(t, t.TempDir())
			q := tunReportRequest{Op: "windows-status"}
			f := &tunOwnedFixture{actual: newAppTunReportEffects(a, q), fail: mode == "effectpanic"}
			w := &tunOwnedWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t, crash: mode == "writerpanic"}
			var p any
			func() { defer func() { p = recover() }(); runTunReportHTTPWithEffects(w, q, f) }()
			if (p != nil) != (mode == "writerpanic") {
				t.Fatalf("panic mismatch %v", p)
			}
			if f.releases != 1 || f.actual.owned {
				t.Fatal("ownership not released exactly once")
			}
			if !a.mu.TryLock() {
				t.Fatal("mutex leaked")
			}
			a.mu.Unlock()
			if mode == "effectpanic" && w.Code != http.StatusInternalServerError {
				t.Fatal("fault not 500")
			}
			if mode == "writerpanic" && w.writes != 1 {
				t.Fatal("writer replayed")
			}
		})
	}
}
