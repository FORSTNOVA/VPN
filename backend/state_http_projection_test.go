package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStateHTTPPrepareFailureIsNotDisconnected(t *testing.T) {
	w := httptest.NewRecorder()
	writeStateResponse(w, func() map[string]any { panic("authority unavailable secret") })
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "connected") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestStateHTTPEncodingFailureBeforeSuccess(t *testing.T) {
	w := httptest.NewRecorder()
	writeStateResponse(w, func() map[string]any { return map[string]any{"bad": make(chan int)} })
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}

type statePanicWriter struct {
	header http.Header
	body   string
	calls  int
}

func (w *statePanicWriter) Header() http.Header { return w.header }
func (w *statePanicWriter) WriteHeader(int)     {}
func (w *statePanicWriter) Write(b []byte) (int, error) {
	w.calls++
	w.body += string(b)
	panic("writer failure")
}
func TestStateHTTPWriterPanicNeverAppendsError(t *testing.T) {
	w := &statePanicWriter{header: http.Header{}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("writer panic swallowed")
			}
		}()
		writeStateResponse(w, func() map[string]any { return map[string]any{"connected": true} })
	}()
	if w.calls != 1 || strings.Contains(w.body, "could not") {
		t.Fatal(w)
	}
}
func TestStateHTTPPreparedJSONShape(t *testing.T) {
	w := httptest.NewRecorder()
	writeStateResponse(w, func() map[string]any {
		return map[string]any{"connected": false, "health": map[string]any{}, "regionJob": map[string]any{}}
	})
	if w.Code != 200 || w.Body.String() != "{\"connected\":false,\"health\":{},\"regionJob\":{}}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestStateHTTPAuthorityLedgerDoesNotRewriteStartedResponse(t *testing.T) {
	raw := &statePanicWriter{header: http.Header{}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected writer panic")
			}
		}()
		w := &authorityHTTPWriter{ResponseWriter: raw}
		defer w.recoverFailure()
		_, _ = w.Write([]byte("partial"))
	}()
	if raw.calls != 1 || raw.body != "partial" {
		t.Fatal("ledger added a second response", raw)
	}
}
