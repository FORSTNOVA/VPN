package main

import (
	"encoding/json"
	"net/http"
)

// Prepare and encode before marking a response. Authority failures must not turn
// a state query into a plausible disconnected/empty-health success.
func writeStateResponse(w http.ResponseWriter, prepare func() map[string]any) {
	started := false
	defer func() {
		if p := recover(); p != nil {
			if started {
				panic(p)
			}
			http.Error(w, "could not read service state", http.StatusInternalServerError)
		}
	}()
	payload := prepare()
	b, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "could not read service state", http.StatusInternalServerError)
		return
	}
	b = append(b, '\n')
	started = true
	_, _ = w.Write(b)
}

// This ledger is only used by non-streaming JSON health endpoints. A prepared
// authority error gets an explicit response; a writer failure is never rewritten.
type authorityHTTPWriter struct {
	http.ResponseWriter
	started bool
}

func (w *authorityHTTPWriter) WriteHeader(code int) {
	if code >= 200 {
		w.started = true
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *authorityHTTPWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}
func (w *authorityHTTPWriter) recoverFailure() {
	if p := recover(); p != nil {
		if w.started {
			panic(p)
		}
		http.Error(w.ResponseWriter, "health state unavailable", http.StatusInternalServerError)
	}
}
